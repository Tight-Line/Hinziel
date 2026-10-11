package claudecode

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/core"
)

// fakeClaude behaves according to $FAKE_SCENARIO and records how it was run.
const fakeClaude = `#!/bin/sh
echo "$PWD" > "$FAKE_LOG.pwd"
printf '%s\n' "$@" > "$FAKE_LOG.args"
cat > "$FAKE_LOG.stdin"
say() { printf '%s\n' "$1"; }
say '{"type":"system","subtype":"init"}'
say '{"type":"assistant","message":{"content":[{"type":"text","text":"Looking at the ticket."},{"type":"tool_use","name":"Bash","input":{"command":"git push origin HEAD"}}]}}'
say '{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"rejected"}]}}'
case "$FAKE_SCENARIO" in
ok)
  printf '%s' '{"summary":"Added CSV export.","commit_message":"T-1: CSV","manual_steps":["Set EXPORT_BUCKET"],"question":""}' > "$HINZIEL_OUTCOME"
  say '{"type":"result","subtype":"success","is_error":false,"result":"All done.","num_turns":7,"total_cost_usd":1.25,"usage":{"input_tokens":100,"cache_read_input_tokens":900,"output_tokens":50}}' ;;
question)
  printf '%s' '{"summary":"","question":"Which columns should the export have?"}' > "$HINZIEL_OUTCOME"
  say '{"type":"result","subtype":"success","is_error":false,"result":"I need input."}' ;;
background)
  sleep 300 >/dev/null 2>&1 &
  echo $! > "$FAKE_LOG.bgpid"
  say '{"type":"result","subtype":"success","is_error":false,"result":"Left a server running."}' ;;
nooutcome)
  say '{"type":"result","subtype":"success","is_error":false,"result":"Done without a file."}' ;;
maxturns)
  say '{"type":"result","subtype":"error_max_turns","is_error":true,"result":""}' ;;
crash)
  echo 'Invalid API key' >&2; exit 1 ;;
esac
`

func setup(t *testing.T, scenario string, repos int) (*Agent, *agent.Task, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(dir, "fake")
	work := t.TempDir()
	task := &agent.Task{
		Workdir: work,
		Prompt:  "Implement ticket T-1: Add CSV export",
		Env:     []string{"PATH=" + os.Getenv("PATH"), "FAKE_SCENARIO=" + scenario, "FAKE_LOG=" + log},
		Log:     &strings.Builder{},
	}
	for _, name := range []string{"api", "web"}[:repos] {
		p := filepath.Join(work, name)
		os.Mkdir(p, 0o755)
		task.Repos = append(task.Repos, agent.Checkout{Name: name, Path: p})
	}
	return New(Options{Model: "claude-opus-5-5", MaxTurns: 200, Instructions: "Run ./toolkit check.", Command: []string{bin}}), task, log
}

func read(t *testing.T, path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestImplement(t *testing.T) {
	a, task, log := setup(t, "ok", 1)
	out, err := a.Implement(context.Background(), task)
	if err != nil {
		t.Fatal(err)
	}
	if out.Summary != "Added CSV export." || out.CommitMessage != "T-1: CSV" || len(out.ManualSteps) != 1 {
		t.Errorf("outcome %+v", out)
	}
	if out.Usage.InputTokens != 1000 || out.Usage.OutputTokens != 50 || out.Usage.CostUSD != 1.25 {
		t.Errorf("usage %+v", out.Usage)
	}

	if got := strings.TrimSpace(read(t, log+".pwd")); got != task.Repos[0].Path {
		t.Errorf("with one repo, claude must run in it, ran in %s", got)
	}
	if read(t, log+".stdin") != task.Prompt {
		t.Error("the prompt must arrive on stdin")
	}
	args := read(t, log+".args")
	for _, want := range []string{"-p\n", "--output-format\nstream-json\n", "--permission-mode\nbypassPermissions\n", "--model\nclaude-opus-5-5\n", "--max-turns\n200\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args must contain %q:\n%s", want, args)
		}
	}
	if !strings.Contains(args, "override anything the project's instructions") || !strings.Contains(args, "Run ./toolkit check.") {
		t.Error("the system prompt must carry Hinziel's rules and the extra instructions")
	}

	transcript := task.Log.(*strings.Builder).String()
	for _, want := range []string{"claude: Looking at the ticket.", `claude → Bash {"command":"git push origin HEAD"}`, "claude ← error: \"rejected\"", "finished (success) after 7 turns, $1.25"} {
		if !strings.Contains(transcript, want) {
			t.Errorf("transcript must contain %q:\n%s", want, transcript)
		}
	}
}

func TestSeveralReposRunAboveThem(t *testing.T) {
	a, task, log := setup(t, "ok", 2)
	if _, err := a.Implement(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(read(t, log+".pwd")); got != task.Workdir {
		t.Errorf("ran in %s", got)
	}
	if args := read(t, log+".args"); !strings.Contains(args, "--add-dir\n"+task.Repos[1].Path+"\n") {
		t.Errorf("every repo must be added:\n%s", args)
	}
}

func TestQuestionNeedsInput(t *testing.T) {
	a, task, _ := setup(t, "question", 1)
	_, err := a.Implement(context.Background(), task)
	var needs *core.NeedsInputError
	if !errors.As(err, &needs) || needs.Question != "Which columns should the export have?" {
		t.Fatalf("want NeedsInputError, got %v", err)
	}
}

func TestWithoutOutcomeFileTheLastMessageIsTheSummary(t *testing.T) {
	a, task, _ := setup(t, "nooutcome", 1)
	out, err := a.Implement(context.Background(), task)
	if err != nil || out.Summary != "Done without a file." {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestFailures(t *testing.T) {
	a, task, _ := setup(t, "maxturns", 1)
	if _, err := a.Implement(context.Background(), task); err == nil || !strings.Contains(err.Error(), "error_max_turns") {
		t.Errorf("max turns: %v", err)
	}
	a, task, _ = setup(t, "crash", 1)
	if _, err := a.Implement(context.Background(), task); err == nil || !strings.Contains(err.Error(), "Invalid API key") {
		t.Errorf("crash must carry stderr: %v", err)
	}
}

func TestCheck(t *testing.T) {
	if New(Options{Command: []string{"/nonexistent/claude"}}).Check(context.Background()) == nil {
		t.Error("a missing claude must fail the check")
	}
	if New(Options{Command: []string{"true"}}).Check(context.Background()) != nil {
		t.Error("a working command must pass")
	}
}

// On a host with nothing installed, Check names the tools it needs.
func TestCheckNamesMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := New(Options{}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not installed on this host (or not on PATH): claude") {
		t.Errorf("want missing claude, got %v", err)
	}
}

// TestIntegration runs real Claude Code on a tiny task in a scratch git repo.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_CLAUDECODE") == "" {
		t.Skip("set HINZIEL_IT_CLAUDECODE=1 (uses Claude Code as logged in, and costs a little)")
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	var log strings.Builder
	out, err := New(Options{MaxTurns: 10}).Implement(context.Background(), &agent.Task{
		Workdir: dir,
		Repos:   []agent.Checkout{{Name: "scratch", Path: dir}},
		Prompt:  "Create a file hello.txt containing exactly the word hello. Don't commit or push; there is no remote.",
		Env:     os.Environ(),
		Log:     &log,
	})
	t.Log(log.String())
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dir, "hello.txt")); strings.TrimSpace(string(data)) != "hello" {
		t.Errorf("hello.txt: %q", data)
	}
	if out.Summary == "" {
		t.Error("no summary")
	}
}
