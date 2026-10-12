package engine

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// Teardown says when the daemon destroys an environment on its own.
type Teardown struct {
	WhenDone bool          // the ticket is done (tracker.DoneChecker)
	After    time.Duration // this long after the ticket's latest run last changed; 0 = never
}

// Environment is an environment as the engine knows it: from the runs that
// used it or, for an orphan, from the provisioner alone.
type Environment struct {
	Factory     string
	Environment *env.Environment
	// Latest is the newest run that used the environment; nil for an orphan.
	Latest    *run.Snapshot
	Destroyed time.Time // zero while it exists
	Orphan    bool      // the provisioner has it, but no run that knows it does
}

// active statuses are those of a run that may still use its environment.
var active = []run.Status{run.StatusQueued, run.StatusRunning, run.StatusAwaitingApproval, run.StatusNeedsInput, run.StatusWaiting}

// Environments returns every environment the runs know, newest first, then
// the orphans the factories' provisioners report (env.Lister). Destroyed
// environments are left out unless all is set.
func (e *Engine) Environments(ctx context.Context, all bool) ([]Environment, error) {
	runs, err := e.store.List(ctx, run.Filter{})
	if err != nil {
		return nil, err
	}
	var out []Environment
	known := map[string]bool{} // factory/name of environments that exist
	seen := map[string]bool{}
	for i := range runs {
		s := &runs[i]
		if s.Environment == nil || seen[s.Factory+"/"+s.Environment.Name] {
			continue
		}
		seen[s.Factory+"/"+s.Environment.Name] = true
		if s.EnvironmentDestroyed.IsZero() {
			known[s.Factory+"/"+s.Environment.Name] = true
		} else if !all {
			continue
		}
		out = append(out, Environment{Factory: s.Factory, Environment: s.Environment, Latest: s, Destroyed: s.EnvironmentDestroyed})
	}
	for _, p := range e.sortedProjects() {
		l, ok := p.Provisioner.(env.Lister)
		if !ok {
			continue
		}
		listed, err := l.List(ctx)
		if err != nil {
			return out, fmt.Errorf("factory %s: listing environments: %w", p.Name, err)
		}
		for i := range listed {
			if !known[p.Name+"/"+listed[i].Name] {
				out = append(out, Environment{Factory: p.Name, Environment: &listed[i], Orphan: true})
			}
		}
	}
	return out, nil
}

func (e *Engine) sortedProjects() []*Project {
	var ps []*Project
	for _, name := range sortedKeys(e.projects) {
		ps = append(ps, e.projects[name])
	}
	return ps
}

// Destroy tears down an environment: target is a run ID, an environment
// name or a ticket (its ID or key), and factory, if set, narrows the search.
// The deployers' Destroy runs first (deploy.Destroyer), then the endpoints'
// DNS records are deleted, then the provisioner's Destroy. The runs that
// used the environment record it as destroyed, and a resumed or new run for
// the ticket provisions a new one. reason says why, for the log and the
// environment.destroyed notification.
//
// It refuses while a run that may still use the environment is unfinished
// (failed runs aside): cancel it first. A run of this process that reaches
// provisioning meanwhile waits until the teardown is over.
func (e *Engine) Destroy(ctx context.Context, factory, target, reason string) (*Environment, error) {
	found, err := e.find(ctx, factory, target)
	if err != nil {
		return nil, err
	}
	p := e.projects[found.Factory]
	name := found.Environment.Name

	key := found.Factory + "/" + name
	e.mu.Lock()
	if e.tearingDown[key] {
		e.mu.Unlock()
		return nil, fmt.Errorf("environment %s is already being torn down", name)
	}
	e.tearingDown[key] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.tearingDown, key)
		e.mu.Unlock()
	}()

	runs, err := e.runsOf(ctx, found)
	if err != nil {
		return nil, err
	}
	for _, s := range runs {
		if slices.Contains(active, s.Status) {
			return nil, fmt.Errorf("run %s is %s and may still use environment %s; cancel it first", s.ID, s.Status, name)
		}
	}

	if err := e.teardown(ctx, p, found.Environment); err != nil {
		return nil, fmt.Errorf("tearing down environment %s: %w", name, err)
	}
	found.Destroyed = e.opts.Now()
	fmt.Fprintf(e.opts.Out, "environment %s destroyed (%s)\n", name, reason)
	if found.Orphan {
		return found, nil
	}

	for _, s := range runs {
		if s.Environment == nil || s.Environment.Name != name || !s.EnvironmentDestroyed.IsZero() {
			continue
		}
		if err := e.prepare(ctx, s.ID, func(r *run.Run) error {
			r.EnvironmentDestroyed = found.Destroyed
			return nil
		}); err != nil {
			return found, fmt.Errorf("environment %s is destroyed, but recording it in run %s failed: %w", name, s.ID, err)
		}
	}
	r, err := e.store.Load(ctx, found.Latest.ID)
	if err != nil {
		return found, err
	}
	found.Latest = &r.Snapshot
	x, err := e.newExecution(p, r)
	if err != nil {
		return found, err
	}
	defer x.close()
	x.logf("", "environment %s destroyed: %s", name, reason)
	msg := destroyedMessage(r, reason)
	x.emitEvent(ctx, &notify.Event{Type: notify.EventEnvironmentDestroyed, Run: r.Snapshot, Message: msg})
	fmt.Fprint(e.opts.Out, undoActions(r))
	return found, nil
}

// find resolves Destroy's target among the environments that exist.
func (e *Engine) find(ctx context.Context, factory, target string) (*Environment, error) {
	if factory != "" && e.projects[factory] == nil {
		return nil, fmt.Errorf("unknown factory %q", factory)
	}
	envs, err := e.Environments(ctx, false)
	if err != nil {
		return nil, err
	}
	var matches []Environment
	for _, en := range envs {
		if factory != "" && en.Factory != factory {
			continue
		}
		if en.Environment.Name == target || (en.Latest != nil && (string(en.Latest.ID) == target || isTicket(en.Latest, target))) {
			matches = append(matches, en)
		}
	}
	if len(matches) == 0 {
		// A run ID of an older run of the ticket names the same environment.
		if r, err := e.store.Load(ctx, run.ID(target)); err == nil && r.Environment != nil && (factory == "" || r.Factory == factory) {
			for _, en := range envs {
				if en.Factory == r.Factory && en.Environment.Name == r.Environment.Name {
					return &en, nil
				}
			}
		}
		return nil, fmt.Errorf("no environment %q: name a run, an environment or a ticket (see `env list`)", target)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("%q names an environment in several factories; pick one with --factory", target)
	}
	return &matches[0], nil
}

// isTicket reports whether target is the run's ticket: the ID it was
// requested with, the tracker's own ID or the ticket's key.
func isTicket(s *run.Snapshot, target string) bool {
	return s.Ticket != nil && (s.Ticket.Ref.ID == target || s.Ticket.Key == target)
}

// runsOf returns the factory's runs that used the environment or are for
// its ticket, newest first.
func (e *Engine) runsOf(ctx context.Context, en *Environment) ([]run.Snapshot, error) {
	if en.Orphan {
		return nil, nil
	}
	all, err := e.store.List(ctx, run.Filter{Factory: en.Factory})
	if err != nil {
		return nil, err
	}
	var out []run.Snapshot
	for _, s := range all {
		sameEnv := s.Environment != nil && s.Environment.Name == en.Environment.Name
		sameTicket := s.Ticket != nil && en.Latest.Ticket != nil && s.Ticket.Ref == en.Latest.Ticket.Ref
		if sameEnv || sameTicket {
			out = append(out, s)
		}
	}
	return out, nil
}

// teardown removes what provisioning and deploying created, in reverse.
// Every deployer and DNS record is tried, so one failure doesn't leave the
// rest behind; the provisioner's Destroy only follows if they all succeed.
// Each part is idempotent, so trying again after a failure is safe.
func (e *Engine) teardown(ctx context.Context, p *Project, en *env.Environment) error {
	var errs []error
	for _, d := range slices.Backward(p.Deployers) {
		if ds, ok := d.Deployer.(deploy.Destroyer); ok {
			if err := ds.Destroy(ctx, en); err != nil {
				errs = append(errs, fmt.Errorf("deployer %s: %w", d.Name, err))
			}
		}
	}
	if p.DNS != nil {
		for _, ep := range p.Endpoints {
			if ep.DNS == nil {
				continue
			}
			u := en.Endpoints[ep.Name]
			if u == "" { // an orphan: no run recorded its endpoints
				u = ep.URL(en)
			}
			parsed, err := url.Parse(u)
			if err != nil || parsed.Hostname() == "" {
				errs = append(errs, fmt.Errorf("endpoint %s: URL %q has no host", ep.Name, u))
				continue
			}
			if err := p.DNS.Delete(ctx, parsed.Hostname()); err != nil {
				errs = append(errs, fmt.Errorf("endpoint %s: DNS: %w", ep.Name, err))
			}
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return p.Provisioner.Destroy(ctx, en)
}

// tearingDownErr makes provisioning wait while this engine tears down the
// same environment.
func (e *Engine) tearingDownErr(factory, name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tearingDown[factory+"/"+name] {
		return &core.WaitError{Reason: "environment " + name + " to finish tearing down"}
	}
	return nil
}

// Sweep destroys the environments each factory's Teardown says are no
// longer needed, and returns what it destroyed. An environment whose
// ticket has an unfinished run is left alone. Errors don't stop the sweep;
// they're returned together at the end.
func (e *Engine) Sweep(ctx context.Context) ([]Environment, error) {
	envs, err := e.Environments(ctx, false)
	if err != nil {
		return nil, err
	}
	var destroyed []Environment
	var errs []error
	for _, en := range envs {
		p := e.projects[en.Factory]
		if en.Orphan || p == nil {
			continue
		}
		reason, err := e.due(ctx, p, &en)
		if err != nil {
			errs = append(errs, fmt.Errorf("environment %s: %w", en.Environment.Name, err))
			continue
		}
		if reason == "" {
			continue
		}
		runs, err := e.runsOf(ctx, &en)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if slices.ContainsFunc(runs, func(s run.Snapshot) bool { return slices.Contains(active, s.Status) }) {
			continue
		}
		d, err := e.Destroy(ctx, en.Factory, en.Environment.Name, reason)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		destroyed = append(destroyed, *d)
	}
	return destroyed, errors.Join(errs...)
}

// due returns why en should be torn down under p's Teardown, or "".
func (e *Engine) due(ctx context.Context, p *Project, en *Environment) (string, error) {
	t := p.Settings.Teardown
	if t.After > 0 {
		if idle := e.opts.Now().Sub(en.Latest.UpdatedAt); idle >= t.After {
			return fmt.Sprintf("no run for %s", t.After), nil
		}
	}
	if t.WhenDone && en.Latest.Ticket != nil {
		dc, ok := p.Tracker.(tracker.DoneChecker)
		if !ok {
			return "", nil
		}
		done, err := dc.Done(ctx, en.Latest.Ticket.Ref.ID)
		if err != nil {
			return "", fmt.Errorf("is ticket %s done: %w", en.Latest.Ticket.Key, err)
		}
		if done {
			return "the ticket is done", nil
		}
	}
	return "", nil
}

// destroyedMessage is the text of environment.destroyed. It lists the
// environment's human actions, which may need undoing by hand.
func destroyedMessage(r *run.Run, reason string) core.Message {
	var b strings.Builder
	fmt.Fprintf(&b, "Environment `%s` was torn down: %s. A new run for this ticket creates a new one.",
		r.Environment.Name, reason)
	if undo := undoActions(r); undo != "" {
		b.WriteString("\n\n" + undo)
	}
	return core.Message{Title: subject(r) + ": environment removed", Body: b.String()}
}

// undoActions lists the environment's human actions as a reminder to undo
// them, or returns "" if it had none.
func undoActions(r *run.Run) string {
	if len(r.Actions) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("These were done by hand for it; undo what shouldn't outlive it:\n")
	for _, a := range r.Actions {
		fmt.Fprintf(&b, "- %s\n", a.Title)
	}
	return b.String()
}
