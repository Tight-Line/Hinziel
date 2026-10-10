package engine

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Tight-Line/hinzele/core"
	"github.com/Tight-Line/hinzele/env"
	"github.com/Tight-Line/hinzele/notify"
	"github.com/Tight-Line/hinzele/run"
)

// collectActions gathers the human actions of the run's environment: the
// provisioner's and the factory's. Progress is kept from earlier attempts of
// this step and from earlier runs that used the same environment, so nobody
// is asked twice.
func (x *execution) collectActions(ctx context.Context, e *env.Environment) error {
	actions := slices.Clone(e.Actions)
	if f := x.p.Settings.EnvironmentActions; f != nil {
		actions = append(actions, f(e)...)
	}
	if err := validateActions(actions); err != nil {
		return err
	}

	known := map[string]run.Action{}
	for _, a := range x.r.Actions {
		known[a.ID] = a
	}
	confirmed, err := x.confirmedElsewhere(ctx, e.Name)
	if err != nil {
		return err
	}
	var out []run.Action
	for _, a := range actions {
		prev := known[a.ID]
		out = append(out, run.Action{
			Action:    a,
			Announced: prev.Announced || confirmed[a.ID],
			Done:      prev.Done || confirmed[a.ID],
		})
	}
	x.r.Actions = out
	return nil
}

func validateActions(actions []env.Action) error {
	provision := slices.Index(Pipeline, StepProvisionEnvironment)
	seen := map[string]bool{}
	for _, a := range actions {
		switch {
		case a.ID == "":
			return fmt.Errorf("action %q has no ID", a.Title)
		case seen[a.ID]:
			return fmt.Errorf("two actions have the ID %q", a.ID)
		case a.Before == "":
		case !slices.Contains(Pipeline, a.Before):
			return fmt.Errorf("action %q: Before names unknown step %q", a.ID, a.Before)
		case slices.Index(Pipeline, a.Before) <= provision:
			return fmt.Errorf("action %q: Before must be a step after %s, not %q", a.ID, StepProvisionEnvironment, a.Before)
		}
		seen[a.ID] = true
	}
	return nil
}

// confirmedElsewhere returns the IDs of actions confirmed by earlier runs of
// this ticket that used the environment named envName.
func (x *execution) confirmedElsewhere(ctx context.Context, envName string) (map[string]bool, error) {
	runs, err := x.e.store.List(ctx, run.Filter{Factory: x.r.Factory, Ticket: &x.r.Request.Ticket})
	if err != nil {
		return nil, err
	}
	done := map[string]bool{}
	for _, s := range runs {
		if s.ID == x.r.ID || s.Environment == nil || s.Environment.Name != envName {
			continue
		}
		for _, a := range s.Actions {
			if a.Done {
				done[a.ID] = true
			}
		}
	}
	return done, nil
}

// announceActions emits run.action_required for every action not announced yet.
func (x *execution) announceActions(ctx context.Context) error {
	for i := range x.r.Actions {
		a := &x.r.Actions[i]
		if a.Announced {
			continue
		}
		x.logf(StepProvisionEnvironment, "action needed: %s", a.Title)
		action := a.Action
		x.emitEvent(ctx, &notify.Event{
			Type:    notify.EventRunActionRequired,
			Run:     x.r.Snapshot,
			Message: actionMessage(x.r, action),
			Action:  &action,
		})
		a.Announced = true
		if err := x.save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// pendingActions returns the unconfirmed actions that hold back step.
func pendingActions(r *run.Run, step string) []run.Action {
	var out []run.Action
	for _, a := range r.Actions {
		if a.Before == step && !a.Done {
			out = append(out, a)
		}
	}
	return out
}

func actionMessage(r *run.Run, a env.Action) core.Message {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(a.Body))
	b.WriteString("\n\n")
	if a.Before != "" {
		fmt.Fprintf(&b, "_Run `%s` will wait before `%s` until this is done. Then confirm it with `approve %s`._\n", r.ID, a.Before, r.ID)
	} else {
		fmt.Fprintf(&b, "_Run `%s` doesn't wait for this, but the environment isn't fully usable until it's done._\n", r.ID)
	}
	return core.Message{Title: subject(r) + ": action needed: " + a.Title, Body: b.String()}
}

// writeActions lists the run's human actions with their state.
func writeActions(b *strings.Builder, r *run.Run) {
	if len(r.Actions) == 0 {
		return
	}
	b.WriteString("\n**Human actions**\n")
	for _, a := range r.Actions {
		mark := " "
		if a.Done {
			mark = "x"
		}
		fmt.Fprintf(b, "- [%s] %s\n", mark, a.Title)
	}
}
