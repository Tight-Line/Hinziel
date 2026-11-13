package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/notify/notifytest"
	"github.com/Tight-Line/hinziel/run"
)

// progressFixture is a fixture with a second notifier that follows the
// run's steps.
func progressFixture(t *testing.T) (*fixture, *notifytest.Fake) {
	f := newFixture(t)
	progress := &notifytest.Fake{}
	f.project.Notifiers = append(f.project.Notifiers, Notifier{On: notify.Progress, Notifier: progress})
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}
	return f, progress
}

// startedSteps are the steps progress was told about, in order.
func startedSteps(progress *notifytest.Fake) []string {
	var out []string
	for _, ev := range progress.Events() {
		if ev.Type == notify.EventRunStepStarted {
			out = append(out, ev.Run.CurrentStep)
		}
	}
	return out
}

func TestEveryStepThatRunsIsAnnouncedToProgressNotifiers(t *testing.T) {
	f, progress := progressFixture(t)

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %s", r.Status, r.Error)
	}

	var ran []string
	for _, s := range r.Steps {
		if s.Status == stepDone {
			ran = append(ran, s.Name)
		}
	}
	if got := startedSteps(progress); !slices.Equal(got, ran) {
		t.Errorf("announced steps %v, want the ones that ran: %v", got, ran)
	}
	if got := progress.Types(); slices.ContainsFunc(got, func(typ notify.EventType) bool { return typ != notify.EventRunStepStarted }) {
		t.Errorf("a Progress notifier got other events: %v", got)
	}
	if slices.Contains(f.notifier.Types(), notify.EventRunStepStarted) {
		t.Errorf("a notifier with no events listed got step events: %v", f.notifier.Types())
	}

	for _, ev := range progress.Events() {
		if ev.Run.CurrentStep != StepImplement {
			continue
		}
		msg := ev.Message
		if msg.Title != "shop T-1: the agent is working on the change" {
			t.Errorf("title %q", msg.Title)
		}
		if !strings.Contains(msg.Body, "Step 5 of 10") || !strings.Contains(msg.Body, string(r.ID)) {
			t.Errorf("body %q", msg.Body)
		}
	}
}

func TestAResumedStepIsAnnouncedAgain(t *testing.T) {
	f, progress := progressFixture(t)
	f.env.EnsureErrs = []error{errors.New("droplet quota exceeded")}

	r, _ := f.start()
	if r.Status != run.StatusFailed {
		t.Fatalf("status %s", r.Status)
	}
	if got := startedSteps(progress); got[len(got)-1] != StepProvisionEnvironment {
		t.Fatalf("announced %v, want it to end at the failed step", got)
	}
	before := len(startedSteps(progress))

	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	after := startedSteps(progress)[before:]
	if len(after) == 0 || after[0] != StepProvisionEnvironment {
		t.Errorf("after resume announced %v, want it to start again at %s", after, StepProvisionEnvironment)
	}
}
