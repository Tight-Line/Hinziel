package engine

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

func googleAction(e *env.Environment) []env.Action {
	return []env.Action{{
		ID:     "google-oauth",
		Title:  "Allow Google login on " + e.Name,
		Body:   "Add " + e.Endpoints["api"] + "/callback to the OAuth client.",
		Before: StepPublish,
	}}
}

func (f *fixture) agentChangesAPI() {
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(f.t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}
}

func eventsOf(f *fixture, typ notify.EventType) []notify.Event {
	var out []notify.Event
	for _, ev := range f.notifier.Events() {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func TestActionIsAnnouncedEarlyAndHoldsBackItsStep(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.EnvironmentActions = googleAction
	var announcedBeforeImplement bool
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		announcedBeforeImplement = len(eventsOf(f, notify.EventRunActionRequired)) == 1
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if !announcedBeforeImplement {
		t.Error("the action must be announced before the agent starts")
	}
	if r.Status != run.StatusAwaitingApproval || r.CurrentStep != StepPublish {
		t.Fatalf("status %s at %s, want awaiting_approval at publish", r.Status, r.CurrentStep)
	}
	if f.step(r, StepDeploy).Status != stepDone {
		t.Error("steps before the action's Before step must run")
	}

	ev := eventsOf(f, notify.EventRunActionRequired)[0]
	if ev.Action == nil || ev.Action.ID != "google-oauth" {
		t.Fatalf("event action: %+v", ev.Action)
	}
	if !strings.Contains(ev.Message.Body, "-api.preview.test/callback") || !strings.Contains(ev.Message.Body, "approve "+string(r.ID)) {
		t.Errorf("action message: %q", ev.Message.Body)
	}
	waiting := eventsOf(f, notify.EventRunAwaitingApproval)
	if len(waiting) != 1 || !strings.Contains(waiting[0].Message.Body, "Allow Google login") {
		t.Errorf("awaiting_approval must list the pending action: %+v", waiting)
	}
	cr := f.api.ChangeRequests()[r.Branch]
	if !strings.Contains(f.api.Input(cr.ID).Description.Body, "- [ ] Allow Google login") {
		t.Errorf("MR description must list the action: %q", f.api.Input(cr.ID).Description.Body)
	}

	if err := f.eng.Approve(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = f.load(r.ID)
	if r.Status != run.StatusSucceeded || !r.Actions[0].Done {
		t.Fatalf("after approve: %s, actions %+v", r.Status, r.Actions)
	}
	comments := f.tracker.Comments("T-1")
	if last := comments[len(comments)-1]; !strings.Contains(last.Body, "- [x] Allow Google login") {
		t.Errorf("publish must list the confirmed action: %q", last.Body)
	}
	if n := len(eventsOf(f, notify.EventRunActionRequired)); n != 1 {
		t.Errorf("announced %d times, want once", n)
	}
}

func TestActionWithoutBeforeDoesNotWait(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.EnvironmentActions = func(e *env.Environment) []env.Action {
		return []env.Action{{ID: "fyi", Title: "Tell the testers"}}
	}
	f.agentChangesAPI()
	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusSucceeded {
		t.Fatalf("status %s", r.Status)
	}
	if len(eventsOf(f, notify.EventRunActionRequired)) != 1 {
		t.Error("the action must still be announced")
	}
}

func TestConfirmedActionIsNotAskedAgainForTheSameEnvironment(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.EnvironmentActions = googleAction
	f.agentChangesAPI()
	r, _ := f.start()
	if err := f.eng.Approve(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}

	// A second run for the same ticket reuses the environment.
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "more.go", "package main\n")
		return nil, nil
	}
	second, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", Ticket: tracker.TicketRef{ID: "T-1"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.eng.Execute(f.ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if r := f.load(second.ID); r.Status != run.StatusSucceeded || !r.Actions[0].Done {
		t.Fatalf("second run: %s, actions %+v", r.Status, r.Actions)
	}
	if n := len(eventsOf(f, notify.EventRunActionRequired)); n != 1 {
		t.Errorf("announced %d times over two runs, want once", n)
	}
}

// actionProvisioner adds a provider action to every environment.
type actionProvisioner struct{ env.Provisioner }

func (p actionProvisioner) Ensure(ctx context.Context, spec env.Spec) (*env.Environment, error) {
	e, err := p.Provisioner.Ensure(ctx, spec)
	if err == nil {
		e.Actions = []env.Action{{ID: "lease", Title: "Approve the sandbox lease", Before: StepImplement}}
	}
	return e, err
}

func TestProvisionerActionsComeFirstAndCanHoldBackImplement(t *testing.T) {
	f := newFixture(t)
	f.project.Provisioner = actionProvisioner{f.env}
	f.project.Settings.EnvironmentActions = googleAction
	f.agentChangesAPI()

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusAwaitingApproval || r.CurrentStep != StepImplement {
		t.Fatalf("status %s at %s, want awaiting_approval at implement", r.Status, r.CurrentStep)
	}
	ids := []string{r.Actions[0].ID, r.Actions[1].ID}
	if !slices.Equal(ids, []string{"lease", "google-oauth"}) {
		t.Errorf("actions %v", ids)
	}
	// One approve confirms every action, so the run doesn't stop again at publish.
	if err := f.eng.Approve(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded {
		t.Fatalf("after approve: %s at %s", r.Status, r.CurrentStep)
	}
}

func TestInvalidActionFailsProvisioning(t *testing.T) {
	for name, a := range map[string]env.Action{
		"no ID":        {Title: "x"},
		"unknown step": {ID: "a", Before: "lunch"},
		"too early":    {ID: "a", Before: StepPrepareWorkspace},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.project.Settings.EnvironmentActions = func(*env.Environment) []env.Action { return []env.Action{a} }
			r, err := f.start()
			if err == nil || r.Status != run.StatusFailed || r.CurrentStep != StepProvisionEnvironment {
				t.Fatalf("status %s at %s, err %v", r.Status, r.CurrentStep, err)
			}
		})
	}
}

// An action done while the run executes is confirmed through the store, and
// the run doesn't stop for it.
func TestActionConfirmedWhileExecutingDoesNotStopTheRun(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.EnvironmentActions = googleAction
	var confirmErr error
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		runs, err := f.eng.Store().List(ctx, run.Filter{})
		if err != nil || len(runs) != 1 {
			t.Fatalf("runs %v, err %v", runs, err)
		}
		confirmErr = f.eng.Approve(ctx, runs[0].ID) // the run is executing: its lock is held
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if confirmErr != nil {
		t.Fatalf("confirming during the run: %v", confirmErr)
	}
	if r.Status != run.StatusSucceeded || !r.Actions[0].Done {
		t.Fatalf("status %s, actions %+v; want succeeded without stopping", r.Status, r.Actions)
	}
	if n := len(eventsOf(f, notify.EventRunAwaitingApproval)); n != 0 {
		t.Errorf("awaiting_approval emitted %d times, want none", n)
	}
}

func TestApproveWhileExecutingWithNothingToConfirmIsRefused(t *testing.T) {
	f := newFixture(t)
	var confirmErr error
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		runs, _ := f.eng.Store().List(ctx, run.Filter{})
		confirmErr = f.eng.Approve(ctx, runs[0].ID)
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}
	if _, err := f.start(); err != nil {
		t.Fatal(err)
	}
	if confirmErr == nil || !strings.Contains(confirmErr.Error(), "no human actions to confirm") {
		t.Fatalf("got %v", confirmErr)
	}
}
