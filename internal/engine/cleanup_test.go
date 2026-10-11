package engine

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/cleanup"
	"github.com/Tight-Line/hinziel/cleanup/cleanuptest"
	"github.com/Tight-Line/hinziel/run"
)

func TestCleanupRunsWhenImplementFails(t *testing.T) {
	t.Setenv("HINZIEL_TEST_FACTORY_SECRET", "do-not-leak")
	f := newFixture(t)
	c := &cleanuptest.Fake{}
	f.project.Cleaners = []cleanup.Cleaner{c}
	f.project.Settings.AgentEnv = map[string]string{"REPLICA_URL": "postgres://ro@replica/app"}
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		return nil, errors.New("agent gave up")
	}
	r, err := f.start()
	if err == nil {
		t.Fatal("the run should fail")
	}

	calls := c.Calls()
	if len(calls) != 1 {
		t.Fatalf("%d cleanups, want 1", len(calls))
	}
	w := calls[0]
	repos := filepath.Join(f.eng.Store().Dir(r.ID), "repos")
	if w.Dir != repos || len(w.Repos) != 2 || w.Repos[0] != (cleanup.Checkout{Name: "api", Path: filepath.Join(repos, "api")}) {
		t.Errorf("workspace %+v", w)
	}
	if !slices.Contains(w.Env, "REPLICA_URL=postgres://ro@replica/app") {
		t.Errorf("the cleaner must get the agent's environment: %q", w.Env)
	}
	for _, kv := range w.Env {
		if strings.Contains(kv, "do-not-leak") || strings.HasPrefix(kv, "HINZIEL_") {
			t.Errorf("the cleaner got %s", kv)
		}
	}
}

func TestAFailingCleanerDoesNotFailTheRun(t *testing.T) {
	f := newFixture(t)
	c := &cleanuptest.Fake{CleanupErrs: []error{errors.New("docker is gone")}}
	f.project.Cleaners = []cleanup.Cleaner{c}
	f.agentChangesAPI()
	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %v", r.Status, err)
	}
	if len(c.Calls()) != 1 {
		t.Errorf("%d cleanups, want 1", len(c.Calls()))
	}
}

func TestCancelRunsTheCleaners(t *testing.T) {
	f := newFixture(t)
	c := &cleanuptest.Fake{}
	f.project.Cleaners = []cleanup.Cleaner{c}
	f.env.EnsureErrs = []error{errors.New("boom")}
	r, _ := f.start() // fails before implement: no cleanup yet
	if n := len(c.Calls()); n != 0 {
		t.Fatalf("%d cleanups before cancel", n)
	}
	if err := f.eng.Cancel(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if len(c.Calls()) != 1 {
		t.Errorf("%d cleanups after cancel, want 1", len(c.Calls()))
	}
}
