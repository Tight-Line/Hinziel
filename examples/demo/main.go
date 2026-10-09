// Command demo is a factory built entirely from fakes, to try the CLI
// without any accounts:
//
//	go build -o /tmp/demo ./examples/demo
//	SKRAT_STATE_DIR=/tmp/skrat-demo /tmp/demo run DEMO-1
//	SKRAT_STATE_DIR=/tmp/skrat-demo /tmp/demo status
//
// The "agent" writes a file, commits and pushes it, then runs
// `$SKRAT_BIN deploy` as a separate process, exactly like a real agent.
// The git remote is a fresh bare repository in a temp directory per process,
// so resuming a run from another process won't find it; everything else is
// as real as it gets.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/Tight-Line/skrat"
	"github.com/Tight-Line/skrat/agent"
	"github.com/Tight-Line/skrat/agent/agenttest"
	"github.com/Tight-Line/skrat/deploy/deploytest"
	"github.com/Tight-Line/skrat/dns/dnstest"
	"github.com/Tight-Line/skrat/env"
	"github.com/Tight-Line/skrat/env/envtest"
	"github.com/Tight-Line/skrat/forge/forgetest"
	"github.com/Tight-Line/skrat/notify/notifytest"
	"github.com/Tight-Line/skrat/tracker"
	"github.com/Tight-Line/skrat/tracker/trackertest"
)

func main() {
	tmp, err := os.MkdirTemp("", "skrat-demo-remote-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	repo, err := forgetest.NewFake(filepath.Join(tmp, "app.git"), "main", map[string]string{"README.md": "# Demo app\n"})
	if err != nil {
		log.Fatal(err)
	}

	skrat.Main(skrat.Factory{
		Name: "demo",
		Tracker: trackertest.NewFake(tracker.Ticket{
			Key:         "DEMO-1",
			Title:       "Add a greeting",
			Description: "Create hello.txt saying hello.",
			URL:         "https://tracker.test/DEMO-1",
		}),
		Repos:       []skrat.Repo{{Name: "app", Forge: repo, TargetBranch: "main"}},
		Agent:       &agenttest.Fake{Func: scriptedAgent},
		Environment: envtest.NewFake(),
		DNS:         dnstest.NewFake(),
		Endpoints: []skrat.Endpoint{{
			Name: "web",
			URL:  func(e *env.Environment) string { return "https://" + e.Name + ".preview.test" },
			DNS:  skrat.HostAddress(0),
		}},
		Deployers: []skrat.Deployer{{Name: "web", Repo: "app", Deployer: &deploytest.Fake{URL: "https://demo.preview.test"}}},
		Notifiers: []skrat.Notifier{{Notifier: &notifytest.Fake{}}},
	})
}

// scriptedAgent does what a coding agent would: change, commit, push, deploy.
func scriptedAgent(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
	dir := task.Repos[0].Path
	if err := os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello\n"), 0o644); err != nil {
		return nil, err
	}
	for _, args := range [][]string{{"git", "add", "--all"}, {"git", "commit", "--quiet", "-m", "Add a greeting"}, {"git", "push", "--quiet"}} {
		if err := runIn(ctx, task, dir, args...); err != nil {
			return nil, err
		}
	}
	if err := runIn(ctx, task, dir, "$SKRAT_BIN", "deploy"); err != nil {
		return nil, err
	}
	return &agent.Outcome{Summary: "Added hello.txt."}, nil
}

func runIn(ctx context.Context, task *agent.Task, dir string, args ...string) error {
	name := args[0]
	if name == "$SKRAT_BIN" {
		for _, kv := range task.Env {
			if v, ok := cutPrefix(kv, "SKRAT_BIN="); ok {
				name = v
			}
		}
	}
	cmd := exec.CommandContext(ctx, name, args[1:]...)
	cmd.Dir = dir
	cmd.Env = task.Env
	cmd.Stdout, cmd.Stderr = task.Log, task.Log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %w", args, err)
	}
	return nil
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}
