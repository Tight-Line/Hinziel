package skrat

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Tight-Line/skrat/agent"
	"github.com/Tight-Line/skrat/agent/agenttest"
	"github.com/Tight-Line/skrat/core"
	"github.com/Tight-Line/skrat/deploy"
	"github.com/Tight-Line/skrat/deploy/deploytest"
	"github.com/Tight-Line/skrat/env"
	"github.com/Tight-Line/skrat/env/envtest"
	"github.com/Tight-Line/skrat/forge/forgetest"
	"github.com/Tight-Line/skrat/internal/gittest"
	"github.com/Tight-Line/skrat/tracker"
	"github.com/Tight-Line/skrat/tracker/trackertest"
)

func cliFactory(t *testing.T, agentFunc func(context.Context, *agent.Task) (*agent.Outcome, error)) Factory {
	t.Helper()
	repo, err := forgetest.NewFake(filepath.Join(t.TempDir(), "app.git"), "main", map[string]string{"README.md": "app\n"})
	if err != nil {
		t.Fatal(err)
	}
	return Factory{
		Name:        "demo",
		Tracker:     trackertest.NewFake(tracker.Ticket{Key: "D-1", Title: "Say hello"}),
		Repos:       []Repo{{Name: "app", Forge: repo, TargetBranch: "main"}},
		Agent:       &agenttest.Fake{Func: agentFunc},
		Environment: envtest.NewFake(),
		Endpoints: []Endpoint{{Name: "web", URL: func(e *env.Environment) string {
			return "https://" + e.Name + ".preview.test"
		}}},
		Deployers: []Deployer{{Name: "web", Repo: "app", Deployer: &deploytest.Fake{}}},
	}
}

func runCLI(t *testing.T, f Factory, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := Run(context.Background(), args, &out, f)
	return out.String(), err
}

func TestCLIRunStatusLogs(t *testing.T) {
	t.Setenv("SKRAT_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		dir := task.Repos[0].Path
		os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644)
		return nil, nil // left uncommitted: finalize_branch commits it
	})

	out, err := runCLI(t, f, "run", "D-1")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^run (\S+)$`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no run id in output:\n%s", out)
	}
	id := m[1]
	for _, want := range []string{"succeeded", "change request (app): https://forge.test/", "web: https://sk-demo-d-1.preview.test"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output lacks %q:\n%s", want, out)
		}
	}

	out, err = runCLI(t, f, "status")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "succeeded") {
		t.Errorf("status: %v\n%s", err, out)
	}
	out, err = runCLI(t, f, "status", id)
	if err != nil || !strings.Contains(out, "skrat/d-1-say-hello") || !strings.Contains(out, "finalize_branch") {
		t.Errorf("status %s: %v\n%s", id, err, out)
	}
	out, err = runCLI(t, f, "logs", id)
	if err != nil || !strings.Contains(out, "[implement] done") {
		t.Errorf("logs: %v\n%s", err, out)
	}
}

func TestCLINeedsInputAndResume(t *testing.T) {
	t.Setenv("SKRAT_STATE_DIR", t.TempDir())
	asked := false
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		if !asked {
			asked = true
			return nil, &core.NeedsInputError{Question: "Hello in which language?"}
		}
		dir := task.Repos[0].Path
		os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hallo\n"), 0o644)
		gittest.Git(dir, "add", "--all")
		gittest.Git(dir, "commit", "--quiet", "-m", "hello")
		gittest.Git(dir, "push", "--quiet")
		return nil, nil
	})

	out, err := runCLI(t, f, "run", "D-1")
	if err != nil {
		t.Fatalf("a run that needs input is not an error: %v", err)
	}
	if !strings.Contains(out, "needs_input") || !strings.Contains(out, "Hello in which language?") {
		t.Fatalf("output:\n%s", out)
	}
	id := regexp.MustCompile(`(?m)^run (\S+)$`).FindStringSubmatch(out)[1]

	if _, err := runCLI(t, f, "run", "D-1"); err == nil || !strings.Contains(err.Error(), "resume or cancel") {
		t.Errorf("second run for the same ticket: %v", err)
	}
	out, err = runCLI(t, f, "resume", id)
	if err != nil || !strings.Contains(out, "succeeded") {
		t.Fatalf("resume: %v\n%s", err, out)
	}
}

func TestCLIRejectsUnknownGate(t *testing.T) {
	t.Setenv("SKRAT_STATE_DIR", t.TempDir())
	_, err := runCLI(t, cliFactory(t, nil), "run", "D-1", "--gate", "deploy_everything")
	if err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("got %v", err)
	}
}

func TestCLIAgentCommandsNeedARun(t *testing.T) {
	t.Setenv("SKRAT_SOCKET", "")
	_, err := runCLI(t, cliFactory(t, nil), "deploy")
	if err == nil || !strings.Contains(err.Error(), "only works inside a run") {
		t.Fatalf("got %v", err)
	}
}

// onceWaiting waits once, briefly, before deploying a run-branch commit.
type onceWaiting struct {
	deploytest.Fake
	waited bool
}

func (d *onceWaiting) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	if src.Branch != "main" && !d.waited {
		d.waited = true
		return nil, &core.WaitError{Reason: "CI", After: 20 * time.Millisecond}
	}
	return d.Fake.Deploy(ctx, e, src)
}

func TestCLIRunFollowsAWait(t *testing.T) {
	t.Setenv("SKRAT_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		os.WriteFile(filepath.Join(task.Repos[0].Path, "hello.txt"), []byte("hello\n"), 0o644)
		return nil, nil
	})
	f.Deployers[0].Deployer = &onceWaiting{}

	out, err := runCLI(t, f, "run", "D-1")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "waiting for CI; checking again at") || !strings.Contains(out, ": succeeded") {
		t.Errorf("run must follow the wait to the end:\n%s", out)
	}
}
