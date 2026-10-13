package engine

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/internal/gittest"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// envUp submits and executes a run of `env up` with key.
func (f *fixture) envUp(key string) (*run.Run, error) {
	f.t.Helper()
	r, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", EnvKey: key})
	if err != nil {
		f.t.Fatal(err)
	}
	execErr := f.eng.Execute(f.ctx, r.ID)
	return f.load(r.ID), execErr
}

func TestEnvUpDeploysTheTargetBranchesWithoutATicket(t *testing.T) {
	f := newFixture(t)
	r, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %s", r.Status, r.Error)
	}
	for _, s := range r.Steps {
		want := stepSkipped
		if slices.Contains(envUpSteps, s.Name) {
			want = stepDone
		}
		if s.Status != want {
			t.Errorf("step %s is %s, want %s", s.Name, s.Status, want)
		}
	}
	if r.Environment == nil || r.Environment.Name != "hz-shop-base" {
		t.Fatalf("environment %+v, want hz-shop-base, named as a ticket with key base would be", r.Environment)
	}
	if r.Environment.Endpoints["api"] != "https://hz-shop-base-api.preview.test" || len(f.dns.Records()) != 1 {
		t.Errorf("endpoints %v, records %v", r.Environment.Endpoints, f.dns.Records())
	}
	for _, d := range append(f.apiDep.Deployed(), f.docsDep.Deployed()...) {
		if d.Branch != "main" || d.Env != "hz-shop-base" {
			t.Errorf("deployed %+v, want the target branch main", d)
		}
	}
	if len(f.apiDep.Deployed()) != 1 || len(f.docsDep.Deployed()) != 1 {
		t.Errorf("deploys: api %d, docs %d", len(f.apiDep.Deployed()), len(f.docsDep.Deployed()))
	}
	if len(f.agent.Tasks()) != 0 || f.tracker.Fetches() != 0 {
		t.Errorf("env up must not fetch a ticket (%d) or start the agent (%d)", f.tracker.Fetches(), len(f.agent.Tasks()))
	}
	if len(f.notifier.Events()) != 0 || len(f.api.ChangeRequests()) != 0 {
		t.Errorf("env up notifies nobody and opens nothing: events %v", f.notifier.Types())
	}

	envs, err := f.eng.Environments(f.ctx, false)
	if err != nil || len(envs) != 1 || envs[0].Latest.ID != r.ID {
		t.Fatalf("env list: %+v, %v", envs, err)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "hz-shop-base", "done with it"); err != nil {
		t.Fatal(err)
	}
	if len(f.environments()) != 0 || len(f.notifier.Events()) != 0 {
		t.Errorf("after destroy: environments %v, events %v", f.environments(), f.notifier.Types())
	}
}

func TestEnvUpAgainDeploysTheLatestCommits(t *testing.T) {
	f := newFixture(t)
	first, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}

	remote, _ := f.api.Remote(f.ctx)
	clone := filepath.Join(t.TempDir(), "api")
	if err := gittest.Git(filepath.Dir(clone), "clone", "--quiet", remote.URL, clone); err != nil {
		t.Fatal(err)
	}
	commitAndPush(t, clone, "new.go", "package main\n")

	second, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}
	if second.Environment.Name != first.Environment.Name || f.env.Ensures() != 2 || len(f.environments()) != 1 {
		t.Errorf("the same key must reuse environment %s, got %s", first.Environment.Name, second.Environment.Name)
	}
	deps := f.apiDep.Deployed()
	if len(deps) != 2 || deps[0].Commit == deps[1].Commit {
		t.Errorf("the second env up must deploy main's new commit: %+v", deps)
	}

	other, err := f.envUp("demo")
	if err != nil {
		t.Fatal(err)
	}
	if other.Environment.Name != "hz-shop-demo" || len(f.environments()) != 2 {
		t.Errorf("another key gets another environment: %s", other.Environment.Name)
	}
}

func TestEnvUpRefusesASecondUnfinishedRunForTheKey(t *testing.T) {
	f := newFixture(t)
	f.apiDep.DeployErrs = []error{errors.New("compose failed")}
	r, err := f.envUp("base")
	if err == nil || r.Status != run.StatusFailed {
		t.Fatalf("status %s, err %v", r.Status, err)
	}
	if _, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", EnvKey: "base"}); err == nil {
		t.Error("a failed env up for the key must be resumed or cancelled first")
	}
	if _, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", EnvKey: "demo"}); err != nil {
		t.Errorf("another key is independent: %v", err)
	}
	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded {
		t.Errorf("resumed: %s", r.Status)
	}
	if _, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", EnvKey: "x", Ticket: tracker.TicketRef{ID: "T-1"}}); err == nil {
		t.Error("a run is for a ticket or for env up, not both")
	}
}

func TestEnvUpEnvironmentsAreSweptByAfterOnly(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Teardown = Teardown{WhenDone: true, After: time.Hour}
	if _, err := f.envUp("base"); err != nil {
		t.Fatal(err)
	}
	if d, err := f.eng.Sweep(f.ctx); err != nil || len(d) != 0 {
		t.Fatalf("an env up with no ticket can't be done: destroyed %v, %v", d, err)
	}
	f.eng.opts.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if d, err := f.eng.Sweep(f.ctx); err != nil || len(d) != 1 {
		t.Errorf("After applies to env up: destroyed %v, %v", d, err)
	}
}
