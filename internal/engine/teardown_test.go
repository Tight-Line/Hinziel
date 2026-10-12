package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/deploy/deploytest"
	"github.com/Tight-Line/hinziel/dns"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// withDestroyer replaces the api deployer with one that implements deploy.Destroyer.
func (f *fixture) withDestroyer() *deploytest.FakeDestroyer {
	d := &deploytest.FakeDestroyer{Fake: deploytest.Fake{URL: "https://api.test"}}
	f.project.Deployers[0].Deployer = d
	return d
}

func (f *fixture) environments() []env.Environment {
	f.t.Helper()
	envs, err := f.env.List(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	return envs
}

func TestDestroyTearsDownAndRecordsIt(t *testing.T) {
	f := newFixture(t)
	d := f.withDestroyer()
	f.agentChangesAPI()
	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.dns.Records()) != 1 || len(f.environments()) != 1 {
		t.Fatalf("before: records %v, environments %v", f.dns.Records(), f.environments())
	}

	en, err := f.eng.Destroy(f.ctx, "", "T-1", "asked on the ticket")
	if err != nil {
		t.Fatal(err)
	}
	if en.Environment.Name != r.Environment.Name {
		t.Errorf("destroyed %s, want %s", en.Environment.Name, r.Environment.Name)
	}
	if len(f.environments()) != 0 || len(f.dns.Records()) != 0 {
		t.Errorf("after: records %v, environments %v", f.dns.Records(), f.environments())
	}
	if !slices.Equal(d.Destroyed(), []string{r.Environment.Name}) {
		t.Errorf("deployer destroyed %v", d.Destroyed())
	}
	if r = f.load(r.ID); r.EnvironmentDestroyed.IsZero() {
		t.Error("the run must record that its environment was destroyed")
	}
	evs := f.notifier.Events()
	last := evs[len(evs)-1]
	if last.Type != notify.EventEnvironmentDestroyed || !strings.Contains(last.Message.Body, "asked on the ticket") {
		t.Errorf("last event %s: %q", last.Type, last.Message.Body)
	}

	if envs, _ := f.eng.Environments(f.ctx, false); len(envs) != 0 {
		t.Errorf("a destroyed environment is not listed: %+v", envs)
	}
	if envs, _ := f.eng.Environments(f.ctx, true); len(envs) != 1 || envs[0].Destroyed.IsZero() {
		t.Errorf("with all, it is, as destroyed: %+v", envs)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "again"); err == nil {
		t.Error("destroying it twice must say there's nothing to destroy")
	}
}

func TestDestroySaysWhenTheEnvironmentWasAlreadyGone(t *testing.T) {
	f := newFixture(t)
	f.agentChangesAPI()
	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.env.Destroy(f.ctx, r.Environment); err != nil { // by hand, outside Hinziel
		t.Fatal(err)
	}

	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "the ticket is done"); err != nil {
		t.Fatal(err)
	}
	if len(f.dns.Records()) != 0 {
		t.Errorf("what was left must still be removed: records %v", f.dns.Records())
	}
	if r = f.load(r.ID); r.EnvironmentDestroyed.IsZero() {
		t.Error("the run must record that its environment is gone")
	}
	evs := f.notifier.Events()
	if body := evs[len(evs)-1].Message.Body; !strings.Contains(body, "already gone") {
		t.Errorf("the notification must say it was already gone: %q", body)
	}
}

func TestDestroyByRunIDAndEnvironmentName(t *testing.T) {
	for _, target := range []func(r *run.Run) string{
		func(r *run.Run) string { return string(r.ID) },
		func(r *run.Run) string { return r.Environment.Name },
	} {
		f := newFixture(t)
		f.agentChangesAPI()
		r, _ := f.start()
		if _, err := f.eng.Destroy(f.ctx, "shop", target(r), "test"); err != nil {
			t.Fatal(err)
		}
		if len(f.environments()) != 0 {
			t.Errorf("%s: not destroyed", target(r))
		}
	}
}

func TestDestroyRefusesWhileARunMayUseIt(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Gates = []string{StepPublish}
	f.agentChangesAPI()
	r, _ := f.start()
	if r.Status != run.StatusAwaitingApproval {
		t.Fatalf("status %s", r.Status)
	}
	_, err := f.eng.Destroy(f.ctx, "", "T-1", "test")
	if err == nil || !strings.Contains(err.Error(), "cancel it first") {
		t.Fatalf("got %v", err)
	}
	if len(f.environments()) != 1 {
		t.Error("the environment must stay")
	}
}

func TestAFailingDeployerKeepsTheEnvironment(t *testing.T) {
	f := newFixture(t)
	d := f.withDestroyer()
	d.DestroyErrs = []error{errors.New("certificate API down")}
	f.agentChangesAPI()
	r, _ := f.start()

	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "test"); err == nil || !strings.Contains(err.Error(), "certificate API down") {
		t.Fatalf("got %v", err)
	}
	if len(f.environments()) != 1 || !f.load(r.ID).EnvironmentDestroyed.IsZero() {
		t.Fatal("after a failure, the environment stays, to try again")
	}
	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "test"); err != nil {
		t.Fatalf("trying again: %v", err)
	}
	if len(f.environments()) != 0 {
		t.Error("not destroyed on the second try")
	}
}

func TestResumeAfterTeardownProvisionsANewEnvironment(t *testing.T) {
	f := newFixture(t)
	f.apiDep.DeployErrs = []error{nil, errors.New("disk full")} // deploy_base works, deploy doesn't
	f.agentChangesAPI()
	r, err := f.start()
	if err == nil || f.step(r, StepDeploy).Status != stepFailed {
		t.Fatalf("expected deploy to fail: %v", err)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "test"); err != nil {
		t.Fatal(err)
	}

	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = f.load(r.ID)
	if r.Status != run.StatusSucceeded || !r.EnvironmentDestroyed.IsZero() {
		t.Fatalf("status %s, destroyed %v", r.Status, r.EnvironmentDestroyed)
	}
	if len(f.environments()) != 1 || len(f.dns.Records()) != 1 {
		t.Errorf("environments %v, records %v", f.environments(), f.dns.Records())
	}
	deployed := f.apiDep.Deployed()
	if last := deployed[len(deployed)-1]; last.Commit != r.Commits["api"] {
		t.Errorf("the new environment must get the run's commit, got %+v", last)
	}
}

func TestSweepTearsDownDoneTickets(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Teardown = Teardown{WhenDone: true}
	f.agentChangesAPI()
	r, _ := f.start()

	if destroyed, err := f.eng.Sweep(f.ctx); err != nil || len(destroyed) != 0 {
		t.Fatalf("a ticket in review is not done: %v %v", destroyed, err)
	}
	f.tracker.MoveTo(f.ctx, "T-1", tracker.StageDone)
	destroyed, err := f.eng.Sweep(f.ctx)
	if err != nil || len(destroyed) != 1 {
		t.Fatalf("destroyed %v: %v", destroyed, err)
	}
	if len(f.environments()) != 0 || f.load(r.ID).EnvironmentDestroyed.IsZero() {
		t.Error("not torn down")
	}
	if destroyed, _ := f.eng.Sweep(f.ctx); len(destroyed) != 0 {
		t.Error("a second sweep has nothing left to do")
	}
}

func TestSweepTearsDownIdleEnvironments(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Teardown = Teardown{After: 24 * time.Hour}
	f.agentChangesAPI()
	f.start()

	if destroyed, _ := f.eng.Sweep(f.ctx); len(destroyed) != 0 {
		t.Fatal("a fresh environment stays")
	}
	f.eng.opts.Now = func() time.Time { return time.Now().Add(25 * time.Hour) }
	if destroyed, err := f.eng.Sweep(f.ctx); err != nil || len(destroyed) != 1 {
		t.Fatalf("destroyed %v: %v", destroyed, err)
	}
}

func TestSweepLeavesUnfinishedRunsAlone(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Teardown = Teardown{WhenDone: true}
	f.project.Settings.Gates = []string{StepPublish}
	f.agentChangesAPI()
	f.start()
	f.tracker.MoveTo(f.ctx, "T-1", tracker.StageDone)
	if destroyed, err := f.eng.Sweep(f.ctx); err != nil || len(destroyed) != 0 {
		t.Fatalf("destroyed %v: %v", destroyed, err)
	}
}

func TestOrphansAreListedAndCanBeDestroyed(t *testing.T) {
	f := newFixture(t)
	if _, err := f.env.Ensure(f.ctx, env.Spec{Name: "hz-shop-lost"}); err != nil {
		t.Fatal(err)
	}
	f.dns.Ensure(f.ctx, dnsRecord("hz-shop-lost-api.preview.test"))
	envs, err := f.eng.Environments(f.ctx, false)
	if err != nil || len(envs) != 1 || !envs[0].Orphan {
		t.Fatalf("%+v: %v", envs, err)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "hz-shop-lost", "test"); err != nil {
		t.Fatal(err)
	}
	if len(f.environments()) != 0 || len(f.dns.Records()) != 0 {
		t.Errorf("environments %v, records %v (an orphan's DNS names come from the endpoints)", f.environments(), f.dns.Records())
	}
}

func dnsRecord(name string) dns.Record {
	return dns.Record{Name: name, Type: "A", Value: "203.0.113.10"}
}

func TestTeardownRemindsOfHumanActions(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.EnvironmentActions = googleAction
	f.agentChangesAPI()
	r, _ := f.start()
	if err := f.eng.Approve(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "T-1", "test"); err != nil {
		t.Fatal(err)
	}
	evs := eventsOf(f, notify.EventEnvironmentDestroyed)
	if len(evs) != 1 || !strings.Contains(evs[0].Message.Body, "undo what shouldn't outlive it:\n- ") {
		t.Errorf("events %+v", evs)
	}
}
