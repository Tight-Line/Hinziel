// Package clitest tests package hinziel from the outside, through Run: Go
// keeps a package's own tests next to it, and the root stays free of them.
package clitest

import (
	"bytes"
	"context"
	"github.com/Tight-Line/hinziel"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/agent/agenttest"
	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/deploy/deploytest"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/env/envtest"
	"github.com/Tight-Line/hinziel/forge/forgetest"
	"github.com/Tight-Line/hinziel/internal/envcheck"
	"github.com/Tight-Line/hinziel/internal/gittest"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trackertest"
)

func cliFactory(t *testing.T, agentFunc func(context.Context, *agent.Task) (*agent.Outcome, error)) hinziel.Factory {
	t.Helper()
	repo, err := forgetest.NewFake(filepath.Join(t.TempDir(), "app.git"), "main", map[string]string{"README.md": "app\n"})
	if err != nil {
		t.Fatal(err)
	}
	return hinziel.Factory{
		Name:        "demo",
		Tracker:     trackertest.NewFake(tracker.Ticket{Key: "D-1", Title: "Say hello"}),
		Repos:       []hinziel.Repo{{Name: "app", Forge: repo, TargetBranch: "main"}},
		Agent:       &agenttest.Fake{Func: agentFunc},
		Environment: envtest.NewFake(),
		Endpoints: []hinziel.Endpoint{{Name: "web", URL: func(e *env.Environment) string {
			return "https://" + e.Name + ".preview.test"
		}}},
		Deployers: []hinziel.Deployer{{Name: "web", Repo: "app", Deployer: &deploytest.Fake{}}},
	}
}

func runCLI(t *testing.T, f hinziel.Factory, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := hinziel.Run(context.Background(), args, &out, f)
	return out.String(), err
}

func TestCLIRunStatusLogs(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
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
	for _, want := range []string{"succeeded", "change request (app): https://forge.test/", "web: https://hz-demo-d-1.preview.test"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output lacks %q:\n%s", want, out)
		}
	}

	out, err = runCLI(t, f, "status")
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "succeeded") {
		t.Errorf("status: %v\n%s", err, out)
	}
	out, err = runCLI(t, f, "status", id)
	if err != nil || !strings.Contains(out, "hinziel/d-1-say-hello") || !strings.Contains(out, "finalize_branch") {
		t.Errorf("status %s: %v\n%s", id, err, out)
	}
	out, err = runCLI(t, f, "logs", id)
	if err != nil || !strings.Contains(out, "[implement] done") {
		t.Errorf("logs: %v\n%s", err, out)
	}
}

func TestCLINeedsInputAndResume(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
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
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	_, err := runCLI(t, cliFactory(t, nil), "run", "D-1", "--gate", "deploy_everything")
	if err == nil || !strings.Contains(err.Error(), "unknown step") {
		t.Fatalf("got %v", err)
	}
}

// The agent's environment holds none of the factory's variables, so its
// commands must not need them.
func TestCLIAgentCommandsDontNeedTheFactorysEnvironment(t *testing.T) {
	envcheck.Reset()
	defer envcheck.Reset()
	hinziel.Env("HINZIEL_TEST_FACTORY_SECRET") // unset, as in the agent's environment
	t.Setenv("HINZIEL_SOCKET", "")
	for _, cmd := range []string{"deploy", "retrieve"} {
		_, err := runCLI(t, cliFactory(t, nil), cmd)
		if err == nil || !strings.Contains(err.Error(), "only works inside a run") {
			t.Errorf("%s: got %v, want it to reach the socket, not stop on missing variables", cmd, err)
		}
	}
}

func TestCLIAgentCommandsNeedARun(t *testing.T) {
	t.Setenv("HINZIEL_SOCKET", "")
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
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
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

func TestCLIEnvListAndDestroy(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		os.WriteFile(filepath.Join(task.Repos[0].Path, "hello.txt"), []byte("hello\n"), 0o644)
		return nil, nil
	})
	if out, err := runCLI(t, f, "run", "D-1"); err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}

	out, err := runCLI(t, f, "env", "list")
	if err != nil || !regexp.MustCompile(`hz-demo-d-1\s+demo\s+D-1\s+\S+ \(succeeded\)\s+exists`).MatchString(out) {
		t.Fatalf("env list: %v\n%s", err, out)
	}
	out, err = runCLI(t, f, "env", "destroy", "hz-demo-d-1")
	if err != nil || !strings.Contains(out, "environment hz-demo-d-1 destroyed (asked for by cli:") {
		t.Fatalf("env destroy: %v\n%s", err, out)
	}
	if out, _ := runCLI(t, f, "env", "list"); !strings.Contains(out, "no environments") {
		t.Errorf("env list after destroy:\n%s", out)
	}
	if out, _ := runCLI(t, f, "env", "list", "--all"); !strings.Contains(out, "destroyed") {
		t.Errorf("env list --all after destroy:\n%s", out)
	}
	if _, err := runCLI(t, f, "env", "destroy", "hz-demo-d-1"); err == nil {
		t.Error("destroying it again must fail")
	}
}

func TestCLIEnvUp(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		t.Error("env up must not start the agent")
		return nil, nil
	})
	out, err := runCLI(t, f, "env", "up")
	if err != nil || !strings.Contains(out, "succeeded") || !strings.Contains(out, "web: https://hz-demo-base.preview.test") {
		t.Fatalf("env up: %v\n%s", err, out)
	}
	if out, err := runCLI(t, f, "env", "up", "staging"); err != nil || !strings.Contains(out, "hz-demo-staging.preview.test") {
		t.Fatalf("env up staging: %v\n%s", err, out)
	}
	out, err = runCLI(t, f, "env", "list")
	if err != nil || !regexp.MustCompile(`hz-demo-base\s+demo\s+-\s+\S+ \(succeeded\)\s+exists`).MatchString(out) {
		t.Fatalf("env list: %v\n%s", err, out)
	}
	if out, err := runCLI(t, f, "status"); err != nil || strings.Count(out, "succeeded") != 2 {
		t.Errorf("status: %v\n%s", err, out)
	}
	if out, err := runCLI(t, f, "env", "destroy", "hz-demo-base"); err != nil {
		t.Fatalf("env destroy: %v\n%s", err, out)
	}
	if _, err := runCLI(t, f, "env", "up", "a", "b"); err == nil {
		t.Error("env up takes one key at most")
	}
}
