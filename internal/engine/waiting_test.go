package engine

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/deploy/deploytest"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/notify"
	"github.com/Tight-Line/heinzel/run"
	"github.com/Tight-Line/heinzel/tracker"
)

// waitingDeployer waits (core.WaitError) a number of times before deploying
// a commit of the run branch, as a deployer waiting for CI's image would.
// Target-branch commits are "already built" and deploy at once.
type waitingDeployer struct {
	deploytest.Fake
	mu    sync.Mutex
	waits int
	after time.Duration
	calls int
}

func (d *waitingDeployer) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	d.mu.Lock()
	d.calls++
	if src.Branch != "main" && d.waits > 0 {
		d.waits--
		d.mu.Unlock()
		return nil, &core.WaitError{Reason: "CI to build " + src.Repo + "@" + short(src.Commit), After: d.after}
	}
	d.mu.Unlock()
	return d.Fake.Deploy(ctx, e, src)
}

func TestWaitingParksTheRunUntilItIsCheckedAgain(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	f.eng.opts.Now = func() time.Time { return now }
	dep := &waitingDeployer{waits: 2, after: 10 * time.Minute}
	f.project.Deployers[0].Deployer = dep
	f.agentChangesAPI()

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusWaiting || r.CurrentStep != StepDeploy || f.step(r, StepDeploy).Status != stepWaiting {
		t.Fatalf("status %s at %s", r.Status, r.CurrentStep)
	}
	if !strings.HasPrefix(r.WaitingFor, "CI to build api@") || !r.WaitUntil.Equal(now.Add(10*time.Minute)) {
		t.Errorf("waiting for %q until %s", r.WaitingFor, r.WaitUntil)
	}
	if r.Error != "" {
		t.Errorf("a wait is not an error: %q", r.Error)
	}
	if err := f.eng.Execute(f.ctx, r.ID); err == nil {
		t.Error("a waiting run must not execute without resume")
	}
	if _, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", Ticket: tracker.TicketRef{ID: "T-1"}}); err == nil {
		t.Error("a waiting run blocks a second run for the ticket")
	}

	// Checking again on the same reason waits again, quietly.
	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusWaiting {
		t.Fatalf("second check: %s", r.Status)
	}
	if n := len(eventsOf(f, notify.EventRunWaiting)); n != 1 {
		t.Errorf("announced %d times, want once for the same reason", n)
	}
	if !strings.Contains(eventsOf(f, notify.EventRunWaiting)[0].Message.Body, "resume "+string(r.ID)) {
		t.Error("the waiting message must say how to check now")
	}

	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded || r.WaitingFor != "" || !r.WaitUntil.IsZero() {
		t.Fatalf("after the wait: %s, waiting for %q", r.Status, r.WaitingFor)
	}
}

func TestAgentDeployWaitsInline(t *testing.T) {
	f := newFixture(t)
	dep := &waitingDeployer{waits: 2, after: time.Millisecond}
	f.project.Deployers[0].Deployer = dep
	var out string
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		var err error
		out, err = agentDeploy(ctx, task)
		return nil, err
	}

	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %v", r.Status, err)
	}
	if strings.Count(out, "waiting for CI to build api@") != 2 || !strings.Contains(out, "api: deployed") {
		t.Errorf("the agent must see the waits and the deploy:\n%s", out)
	}
	// docs is unchanged: deploy_base deployed its commit, and the retries
	// after api's waits skip it rather than deploying it again.
	if n := len(f.docsDep.Deployed()); n != 1 {
		t.Errorf("docs deployed %d times, want 1", n)
	}
}

func TestDeployBaseDoesNotRedeployAfterAWait(t *testing.T) {
	f := newFixture(t)
	// docs waits on deploy_base (its own "build" isn't ready), api doesn't.
	f.project.Deployers[1].Deployer = &baseWaiter{waitingDeployer: &waitingDeployer{waits: 1}}
	f.agentChangesAPI()

	r, _ := f.start()
	if r.Status != run.StatusWaiting || r.CurrentStep != StepDeployBase {
		t.Fatalf("status %s at %s", r.Status, r.CurrentStep)
	}
	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(f.apiDep.Deployed()); n != 2 { // deploy_base once, then the final deploy
		t.Errorf("api deployed %d times, want 2", n)
	}
}

// baseWaiter waits on target-branch deploys too.
type baseWaiter struct{ *waitingDeployer }

func (b *baseWaiter) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	b.mu.Lock()
	if b.waits > 0 {
		b.waits--
		b.mu.Unlock()
		return nil, &core.WaitError{Reason: "a booking"}
	}
	b.mu.Unlock()
	return b.Fake.Deploy(ctx, e, src)
}

func TestWaitingRunCanBeCancelled(t *testing.T) {
	f := newFixture(t)
	f.project.Deployers[0].Deployer = &waitingDeployer{waits: 1}
	f.agentChangesAPI()
	r, _ := f.start()
	if err := f.eng.Cancel(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusCancelled {
		t.Errorf("status %s", r.Status)
	}
}
