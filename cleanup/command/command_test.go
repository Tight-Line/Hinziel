package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/cleanup"
)

func TestRunsInTheRepoWithTheAgentsEnvironment(t *testing.T) {
	t.Setenv("FACTORY_SECRET", "s3cret")
	dir := t.TempDir()
	api := filepath.Join(dir, "api")
	os.Mkdir(api, 0o755)
	var log strings.Builder
	w := &cleanup.Workspace{
		Dir:   dir,
		Repos: []cleanup.Checkout{{Name: "api", Path: api}},
		Env:   []string{"PATH=" + os.Getenv("PATH"), "AGENT_VAR=yes"},
		Log:   &log,
	}
	c := New(Options{Repo: "api", Command: `pwd; echo "agent=$AGENT_VAR secret=$FACTORY_SECRET"`})
	if err := c.Cleanup(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if got := log.String(); got != api+"\nagent=yes secret=\n" {
		t.Errorf("output %q", got)
	}
}

func TestWithoutRepoItRunsAboveTheCheckouts(t *testing.T) {
	dir := t.TempDir()
	var log strings.Builder
	w := &cleanup.Workspace{Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH")}, Log: &log}
	if err := New(Options{Command: "pwd"}).Cleanup(context.Background(), w); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(log.String()) != dir {
		t.Errorf("ran in %q", log.String())
	}
}

func TestFailures(t *testing.T) {
	w := &cleanup.Workspace{Dir: t.TempDir(), Log: &strings.Builder{}}
	if err := New(Options{Repo: "web", Command: "true"}).Cleanup(context.Background(), w); err == nil || !strings.Contains(err.Error(), `repo "web"`) {
		t.Errorf("unknown repo: %v", err)
	}
	if err := New(Options{Command: "exit 3"}).Cleanup(context.Background(), w); err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("failing command: %v", err)
	}
	if err := New(Options{Command: " "}).Check(context.Background()); err == nil {
		t.Error("Check must reject an empty command")
	}
	if err := New(Options{Command: "x", Tools: []string{"hinziel-test-no-such-tool"}}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "hinziel-test-no-such-tool") {
		t.Errorf("missing tool: %v", err)
	}
}
