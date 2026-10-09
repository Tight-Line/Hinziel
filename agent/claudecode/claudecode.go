// Package claudecode is an agent.Agent that runs Claude Code
// (https://claude.com/claude-code) headless on the Heinzel host.
//
// The engine's task becomes the prompt. Heinzel's own rules (work unattended,
// branch, push and deploy only as the task says, report back in a file) go
// into the system prompt, so they take precedence over a repository's
// CLAUDE.md, which is usually written for a person working with Claude by
// hand. Everything else in the project's instructions still applies.
//
// The agent reports its result by writing JSON to $HEINZEL_OUTCOME; a
// question there becomes core.NeedsInputError. The session is rendered into
// a readable transcript in the run log.
//
// Claude Code must be installed and logged in (or ANTHROPIC_API_KEY set) for
// the user running the factory. It runs with permission prompts off
// (bypassPermissions), as Heinzel's agents have full access by design.
//
// # Credentials
//
//   - Claude Code installed on the Heinzel host (https://claude.com/claude-code).
//   - One of: CLAUDE_CODE_OAUTH_TOKEN, from running `claude setup-token`
//     once with a Claude subscription; or ANTHROPIC_API_KEY, from
//     https://console.anthropic.com → API keys. On a desktop where you're
//     already logged in to Claude Code, neither is needed.
//   - Whatever the agent itself needs for the project, such as SSH access to
//     environments, comes through the factory's environment.
package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/core"
)

// Options configure Claude Code.
type Options struct {
	Model    string // e.g. "claude-opus-5-5"; "" = Claude Code's default
	MaxTurns int    // 0 = Claude Code's default
	// Instructions are appended to Heinzel's system prompt.
	Instructions string
	// Args are extra command-line arguments, e.g. {"--mcp-config", "mcp.json"}.
	Args []string
	// Command is the claude executable; nil = {"claude"}.
	Command []string
}

// Agent runs Claude Code.
type Agent struct{ opts Options }

var (
	_ agent.Agent  = (*Agent)(nil)
	_ core.Checker = (*Agent)(nil)
)

// New returns a Claude Code agent.
func New(opts Options) *Agent { return &Agent{opts: opts} }

// systemPrompt is appended to Claude Code's own system prompt.
const systemPrompt = `You are running unattended inside Heinzel, a software factory. No human is watching this session, and nobody can answer you while it runs.

These rules come from Heinzel and override anything the project's instructions (CLAUDE.md, AGENTS.md and similar) say about git branches, committing, pushing, deploying, merge requests or asking for confirmation:
- Work only on the branch the task names. Commit and push there as the task says, and deploy only with the command the task gives you.
- Never ask for confirmation and never wait for approval: decide and do the work.
- Never open merge requests; Heinzel does that.
Everything else in the project's instructions still applies, such as coding conventions, how to run tests and checks, and documentation the project expects.

When you have finished, write a JSON object to the file named by the HEINZEL_OUTCOME environment variable:
{"summary": "...", "commit_message": "...", "manual_steps": ["..."], "question": ""}
- summary: Markdown for the reviewers of the merge request: what you changed, why, and how you tested it.
- commit_message: optional; used for anything Heinzel commits after you.
- manual_steps: things a human must do that the change can't carry, such as a secret to set in production. Empty if none.
- question: leave empty, unless you can't continue without a decision from a human. Then don't guess: commit and push whatever is useful, put your question here, and stop. Heinzel posts it on the ticket and runs you again once it's answered.`

// outcomeFile is what the agent writes to $HEINZEL_OUTCOME.
type outcomeFile struct {
	Summary       string   `json:"summary"`
	CommitMessage string   `json:"commit_message"`
	ManualSteps   []string `json:"manual_steps"`
	Question      string   `json:"question"`
}

// Implement runs Claude Code on the task.
func (a *Agent) Implement(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
	tmp, err := os.MkdirTemp("", "heinzel-claudecode-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	outcomePath := filepath.Join(tmp, "outcome.json")

	cmdline := a.command()
	args := append(cmdline[1:],
		"-p",
		"--output-format", "stream-json", "--verbose",
		"--permission-mode", "bypassPermissions",
		"--append-system-prompt", a.systemPrompt(),
	)
	if a.opts.Model != "" {
		args = append(args, "--model", a.opts.Model)
	}
	if a.opts.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprint(a.opts.MaxTurns))
	}
	// With one repository, run in it, so its CLAUDE.md, settings and skills
	// load as they do for its developers. With several, run above them.
	dir := task.Workdir
	if len(task.Repos) == 1 {
		dir = task.Repos[0].Path
	} else {
		for _, c := range task.Repos {
			args = append(args, "--add-dir", c.Path)
		}
	}
	args = append(args, a.opts.Args...)

	cmd := exec.CommandContext(ctx, cmdline[0], args...)
	cmd.Dir = dir
	cmd.Env = append(append([]string{}, task.Env...), "HEINZEL_OUTCOME="+outcomePath)
	cmd.Stdin = strings.NewReader(task.Prompt)
	stderr := &tailWriter{max: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	log := task.Log
	if log == nil {
		log = io.Discard
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting claude: %w", err)
	}
	res := transcribe(stdout, log)
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if res == nil {
		return nil, fmt.Errorf("claude ended without a result: %v\n%s", waitErr, strings.TrimSpace(stderr.String()))
	}
	if res.IsError || res.Subtype != "success" {
		return nil, fmt.Errorf("claude stopped (%s): %s", res.Subtype, firstLine(res.Result))
	}

	out := &agent.Outcome{
		Summary: strings.TrimSpace(res.Result),
		Usage: agent.Usage{
			InputTokens:  res.Usage.InputTokens + res.Usage.CacheReadInputTokens + res.Usage.CacheCreationInputTokens,
			OutputTokens: res.Usage.OutputTokens,
			CostUSD:      res.TotalCostUSD,
			Duration:     time.Since(started),
		},
	}
	data, err := os.ReadFile(outcomePath)
	if errors.Is(err, os.ErrNotExist) {
		fmt.Fprintln(log, "claudecode: the agent wrote no outcome file; using its last message as the summary")
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var of outcomeFile
	if err := json.Unmarshal(data, &of); err != nil {
		fmt.Fprintf(log, "claudecode: the outcome file isn't valid JSON (%v); using the last message as the summary\n", err)
		return out, nil
	}
	if q := strings.TrimSpace(of.Question); q != "" {
		return nil, &core.NeedsInputError{Question: q}
	}
	if s := strings.TrimSpace(of.Summary); s != "" {
		out.Summary = s
	}
	out.CommitMessage = strings.TrimSpace(of.CommitMessage)
	out.ManualSteps = of.ManualSteps
	return out, nil
}

func (a *Agent) command() []string {
	if len(a.opts.Command) > 0 {
		return append([]string{}, a.opts.Command...)
	}
	return []string{"claude"}
}

func (a *Agent) systemPrompt() string {
	if s := strings.TrimSpace(a.opts.Instructions); s != "" {
		return systemPrompt + "\n\n" + s
	}
	return systemPrompt
}

// Check verifies that Claude Code is installed.
func (a *Agent) Check(ctx context.Context) error {
	cmdline := a.command()
	if err := core.NeedTools(cmdline[0]); err != nil {
		return fmt.Errorf("%w (install Claude Code: https://claude.com/claude-code)", err)
	}
	out, err := exec.CommandContext(ctx, cmdline[0], append(cmdline[1:], "--version")...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("claude --version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// result is the final event of a stream-json session.
type result struct {
	Subtype      string  `json:"subtype"`
	IsError      bool    `json:"is_error"`
	Result       string  `json:"result"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

type event struct {
	Type    string `json:"type"`
	Message struct {
		Content []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
			IsError bool            `json:"is_error"`
		} `json:"content"`
	} `json:"message"`
}

// transcribe renders the stream-json session readably into log and returns
// the final result, or nil if the session ended without one.
func transcribe(r io.Reader, log io.Writer) *result {
	var res *result
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1024*1024), 64*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		var ev event
		if json.Unmarshal(line, &ev) != nil {
			fmt.Fprintf(log, "claude: %s\n", line)
			continue
		}
		switch ev.Type {
		case "assistant":
			for _, c := range ev.Message.Content {
				switch c.Type {
				case "text":
					fmt.Fprintf(log, "claude: %s\n", strings.TrimSpace(c.Text))
				case "tool_use":
					fmt.Fprintf(log, "claude → %s %s\n", c.Name, clip(string(c.Input), 300))
				}
			}
		case "user":
			for _, c := range ev.Message.Content {
				if c.Type == "tool_result" && c.IsError {
					fmt.Fprintf(log, "claude ← error: %s\n", clip(string(c.Content), 300))
				}
			}
		case "result":
			res = &result{}
			json.Unmarshal(line, res)
			fmt.Fprintf(log, "claude: finished (%s) after %d turns, $%.2f\n", res.Subtype, res.NumTurns, res.TotalCostUSD)
		}
	}
	return res
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

type tailWriter struct {
	max int
	buf []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailWriter) String() string { return string(t.buf) }
