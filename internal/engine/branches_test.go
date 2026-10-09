package engine

import (
	"context"
	"testing"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/run"
)

func TestBranchesAreCreatedOnlyWhereSomethingChanges(t *testing.T) {
	f := newFixture(t)
	var branch string
	var remoteBeforePush bool
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		branch = f.load(run.ID(envVar(task, "HEINZEL_RUN_ID"))).Branch
		remoteBeforePush = f.api.BranchExists(branch) || f.docs.BranchExists(branch)
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %v", r.Status, err)
	}
	if remoteBeforePush {
		t.Error("prepare_workspace must not push branches")
	}
	if !f.api.BranchExists(branch) {
		t.Error("the changed repo must have the run branch")
	}
	if f.docs.BranchExists(branch) {
		t.Error("the unchanged repo must never get the run branch")
	}
	if len(f.docs.ChangeRequests()) != 0 {
		t.Error("the unchanged repo must get no change request")
	}

	// deploy_base deployed both from their target branch; the final deploy
	// of api comes from the run branch, while docs stays on main.
	api, docs := f.apiDep.Deployed(), f.docsDep.Deployed()
	if api[0].Branch != "main" || api[len(api)-1].Branch != branch {
		t.Errorf("api deployed from %q then %q", api[0].Branch, api[len(api)-1].Branch)
	}
	for _, d := range docs {
		if d.Branch != "main" {
			t.Errorf("docs deployed from %q, want main", d.Branch)
		}
	}
}
