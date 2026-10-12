package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/store"
	"github.com/Tight-Line/hinziel/internal/workspace"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// Step record statuses.
const (
	stepPending = "pending"
	stepRunning = "running"
	stepWaiting = "waiting"
	stepDone    = "done"
	stepSkipped = "skipped"
	stepFailed  = "failed"
)

// Options tune an Engine. Zero values give the defaults.
type Options struct {
	Out         io.Writer        // progress lines; nil = discard (the run log always gets them)
	Bin         string           // factory binary, passed to the agent as HINZIEL_BIN; "" = this executable
	MaxAttempts int              // attempts per step for core.RetryableError; 0 = 3
	RetryDelay  time.Duration    // first retry delay, doubled each attempt; 0 = 5s
	Now         func() time.Time // nil = time.Now
	// Config, if set, is called after every step and every deploy for the
	// agent. An error fails that step: it means the factory's configuration
	// turned out unusable while running, e.g. a variable read too late to be
	// checked at startup.
	Config func() error
}

// Engine executes runs for a set of projects.
type Engine struct {
	store    *store.FS
	projects map[string]*Project
	opts     Options

	mu          sync.Mutex
	tearingDown map[string]bool // factory/environment being destroyed by Destroy
}

// New returns an engine for projects, persisting runs in st.
func New(st *store.FS, projects []*Project, opts Options) *Engine {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	if opts.Bin == "" {
		opts.Bin, _ = os.Executable()
	}
	if opts.MaxAttempts == 0 {
		opts.MaxAttempts = 3
	}
	if opts.RetryDelay == 0 {
		opts.RetryDelay = 5 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	e := &Engine{store: st, projects: map[string]*Project{}, opts: opts, tearingDown: map[string]bool{}}
	for _, p := range projects {
		e.projects[p.Name] = p
	}
	return e
}

// Store gives read access to runs (for status and logs).
func (e *Engine) Store() *store.FS { return e.store }

// unfinished statuses block a new run for the same ticket.
var unfinished = []run.Status{run.StatusQueued, run.StatusRunning, run.StatusAwaitingApproval, run.StatusNeedsInput, run.StatusWaiting, run.StatusFailed}

// Submit records a new queued run. It refuses if the ticket already has an
// unfinished run, because both would fight over the same branch and environment.
func (e *Engine) Submit(ctx context.Context, req run.Request) (*run.Run, error) {
	if _, ok := e.projects[req.Factory]; !ok {
		return nil, fmt.Errorf("unknown factory %q", req.Factory)
	}
	if req.Ticket.ID == "" {
		return nil, errors.New("no ticket given")
	}
	existing, err := e.store.List(ctx, run.Filter{Factory: req.Factory, Ticket: &req.Ticket, Status: unfinished, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		return nil, fmt.Errorf("ticket %s already has run %s (%s); resume or cancel it first",
			req.Ticket.ID, existing[0].ID, existing[0].Status)
	}
	now := e.opts.Now()
	r := &run.Run{Request: req}
	r.ID = e.store.NewID(now)
	r.Factory = req.Factory
	r.Status = run.StatusQueued
	r.StartedAt, r.UpdatedAt = now, now
	for _, name := range Pipeline {
		r.Steps = append(r.Steps, run.StepRecord{Name: name, Status: stepPending})
	}
	return r, e.store.Save(ctx, r)
}

// Execute runs a queued or running run until it finishes or pauses (gate,
// needs input). It returns an error only if the run failed or couldn't start.
func (e *Engine) Execute(ctx context.Context, id run.ID) error {
	unlock, err := e.store.Lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	r, err := e.store.Load(ctx, id)
	if err != nil {
		return err
	}
	p, ok := e.projects[r.Factory]
	if !ok {
		return fmt.Errorf("run %s belongs to factory %q, which this binary doesn't have", id, r.Factory)
	}
	x, err := e.newExecution(p, r)
	if err != nil {
		return err
	}
	defer x.close()

	switch r.Status {
	case run.StatusQueued:
		r.Status = run.StatusRunning
		if err := x.save(ctx); err != nil {
			return err
		}
		x.logf("", "run %s started for ticket %s", r.ID, r.Request.Ticket.ID)
		x.emit(ctx, notify.EventRunStarted)
	case run.StatusRunning:
		x.logf("", "run %s continuing", r.ID)
	default:
		return fmt.Errorf("run %s is %s", id, r.Status)
	}
	return x.pipeline(ctx)
}

// Resume makes a failed, needs_input, waiting or crashed run runnable again
// and executes it. A needs_input run fetches its ticket again first, so the
// agent sees the answer to its question. A waiting run checks its step again
// right away, whatever its next check time.
func (e *Engine) Resume(ctx context.Context, id run.ID) error {
	if err := e.prepare(ctx, id, func(r *run.Run) error {
		switch r.Status {
		case run.StatusFailed, run.StatusRunning, run.StatusWaiting:
		case run.StatusNeedsInput:
			resetStep(r, StepFetchTicket)
		default:
			return fmt.Errorf("run %s is %s; only failed, needs_input, waiting or interrupted runs can be resumed", r.ID, r.Status)
		}
		for i := range r.Steps {
			if s := r.Steps[i].Status; s == stepFailed || s == stepRunning || s == stepWaiting {
				r.Steps[i].Status = stepPending
			}
		}
		if !r.EnvironmentDestroyed.IsZero() {
			reprovision(r)
		}
		r.Status = run.StatusRunning
		r.Error = ""
		return nil
	}); err != nil {
		return err
	}
	return e.Execute(ctx, id)
}

// Approve passes the gate a run is waiting at and executes the rest of it.
// On a run that isn't waiting for approval, it confirms the human actions
// instead, so the run won't stop for them; that works while it executes too.
func (e *Engine) Approve(ctx context.Context, id run.ID) error {
	confirmedOnly := false
	err := e.prepare(ctx, id, func(r *run.Run) error {
		if r.Status == run.StatusAwaitingApproval {
			r.Approved = append(r.Approved, r.CurrentStep)
			// Approving confirms every human action: whoever approves has seen them
			// all in the notifications and in `status`.
			for i := range r.Actions {
				r.Actions[i].Done = true
			}
			r.Status = run.StatusRunning
			return nil
		}
		// Not stopped for approval: confirm the actions, so the run doesn't
		// stop for them later, and leave the run as it is.
		if len(undoneActions(r)) == 0 || r.Status == run.StatusSucceeded || r.Status == run.StatusCancelled {
			return fmt.Errorf("run %s is %s, not awaiting approval", r.ID, r.Status)
		}
		for i := range r.Actions {
			r.Actions[i].Done = true
		}
		confirmedOnly = true
		return nil
	})
	if errors.As(err, new(executingError)) {
		return e.confirmWhileExecuting(ctx, id)
	}
	if err != nil {
		return err
	}
	if confirmedOnly {
		fmt.Fprintln(e.opts.Out, "confirmed the human actions; the run won't stop for them")
		return nil
	}
	return e.Execute(ctx, id)
}

// confirmWhileExecuting confirms the human actions of a run that another
// process is executing, through the store: that process applies the
// confirmation before its next step, so doing an action early never makes
// the run stop for it.
func (e *Engine) confirmWhileExecuting(ctx context.Context, id run.ID) error {
	r, err := e.store.Load(ctx, id)
	if err != nil {
		return err
	}
	ids := undoneActions(r)
	if len(ids) == 0 {
		return fmt.Errorf("run %s is executing in another process and has no human actions to confirm; "+
			"approve it once it waits for approval", id)
	}
	if err := e.store.ConfirmActions(id, ids); err != nil {
		return err
	}
	fmt.Fprintf(e.opts.Out, "confirmed %s; the run won't stop for them\n", strings.Join(ids, ", "))
	return nil
}

// undoneActions returns the IDs of the run's unconfirmed human actions.
func undoneActions(r *run.Run) []string {
	var ids []string
	for _, a := range r.Actions {
		if !a.Done {
			ids = append(ids, a.ID)
		}
	}
	return ids
}

// executingError means another process holds the run's lock.
type executingError struct{ id run.ID }

func (e executingError) Error() string {
	return fmt.Sprintf("run %s is executing in another process; stop it there first", e.id)
}

// Cancel marks an unfinished run as cancelled. A run executing in another
// process must be stopped there first. A run interrupted during implement
// may have left what the agent started running, so the factory's cleaners
// run too.
func (e *Engine) Cancel(ctx context.Context, id run.ID) error {
	err := e.prepare(ctx, id, func(r *run.Run) error {
		if r.Status == run.StatusSucceeded || r.Status == run.StatusCancelled {
			return fmt.Errorf("run %s is already %s", r.ID, r.Status)
		}
		r.Status = run.StatusCancelled
		return nil
	})
	if err != nil {
		return err
	}
	r, err := e.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if p := e.projects[r.Factory]; p != nil && len(p.Cleaners) > 0 {
		x, err := e.newExecution(p, r)
		if err != nil {
			return err
		}
		defer x.close()
		x.cleanUp(ctx)
	}
	return nil
}

// prepare changes a run's state while holding its lock.
func (e *Engine) prepare(ctx context.Context, id run.ID, change func(*run.Run) error) error {
	unlock, err := e.store.Lock(id)
	if errors.Is(err, store.ErrLocked) {
		return executingError{id}
	}
	if err != nil {
		return err
	}
	defer unlock()
	r, err := e.store.Load(ctx, id)
	if err != nil {
		return err
	}
	if err := change(r); err != nil {
		return err
	}
	r.UpdatedAt = e.opts.Now()
	return e.store.Save(ctx, r)
}

// reprovision makes a run whose environment was torn down provision a new
// one, then deploy to it what it had deployed: the base before implement,
// its own commits after.
func reprovision(r *run.Run) {
	resetStep(r, StepProvisionEnvironment)
	implemented := slices.ContainsFunc(r.Steps, func(s run.StepRecord) bool {
		return s.Name == StepImplement && s.Status == stepDone
	})
	if implemented {
		resetStep(r, StepDeploy)
	} else {
		resetStep(r, StepDeployBase)
	}
	r.Deployments = nil
	r.EnvironmentDestroyed = time.Time{}
}

func resetStep(r *run.Run, name string) {
	for i := range r.Steps {
		if r.Steps[i].Name == name {
			r.Steps[i].Status = stepPending
		}
	}
}

// execution is one process executing one run.
type execution struct {
	e   *Engine
	p   *Project
	r   *run.Run
	log *os.File

	repos map[string]*workspace.Repo

	// socketMu serialises requests from the agent's deploy/retrieve commands.
	socketMu sync.Mutex
}

func (e *Engine) newExecution(p *Project, r *run.Run) (*execution, error) {
	if err := os.MkdirAll(e.store.Dir(r.ID), 0o755); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(e.store.LogPath(r.ID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &execution{e: e, p: p, r: r, log: log, repos: map[string]*workspace.Repo{}}, nil
}

func (x *execution) close() { x.log.Close() }

func (x *execution) save(ctx context.Context) error {
	x.r.UpdatedAt = x.e.opts.Now()
	return x.e.store.Save(ctx, x.r)
}

// logf writes a progress line to the run log and to Options.Out.
func (x *execution) logf(step, format string, args ...any) {
	prefix := x.e.opts.Now().UTC().Format(time.RFC3339)
	if step != "" {
		prefix += " [" + step + "]"
	}
	line := prefix + " " + fmt.Sprintf(format, args...) + "\n"
	x.log.WriteString(line)
	io.WriteString(x.e.opts.Out, line)
}

func (x *execution) record(name string) *run.StepRecord {
	for i := range x.r.Steps {
		if x.r.Steps[i].Name == name {
			return &x.r.Steps[i]
		}
	}
	x.r.Steps = append(x.r.Steps, run.StepRecord{Name: name, Status: stepPending})
	return &x.r.Steps[len(x.r.Steps)-1]
}

func (x *execution) pipeline(ctx context.Context) error {
	for _, name := range Pipeline {
		rec := x.record(name)
		if rec.Status == stepDone || rec.Status == stepSkipped {
			continue
		}
		if reason := x.skipReason(name); reason != "" {
			rec.Status = stepSkipped
			x.logf(name, "skipped: %s", reason)
			if err := x.save(ctx); err != nil {
				return err
			}
			continue
		}
		if err := x.applyConfirmations(); err != nil {
			return err
		}
		if x.gated(name) || len(pendingActions(x.r, name)) > 0 {
			x.r.Status = run.StatusAwaitingApproval
			x.r.CurrentStep = name
			if err := x.save(ctx); err != nil {
				return err
			}
			x.logf(name, "waiting for approval")
			x.emit(ctx, notify.EventRunAwaitingApproval)
			return nil
		}

		rec.Status, rec.Error = stepRunning, ""
		rec.Started = x.e.opts.Now()
		x.r.CurrentStep = name
		if err := x.save(ctx); err != nil {
			return err
		}
		x.logf(name, "started")
		err := x.attempt(ctx, name)
		rec.Finished = x.e.opts.Now()
		if err != nil {
			return x.fail(ctx, rec, err)
		}
		rec.Status = stepDone
		x.r.WaitingFor, x.r.WaitUntil = "", time.Time{}
		if err := x.save(ctx); err != nil {
			return err
		}
		x.logf(name, "done")
	}

	x.r.Status = run.StatusSucceeded
	x.r.CurrentStep = ""
	if err := x.save(ctx); err != nil {
		return err
	}
	x.logf("", "run %s succeeded", x.r.ID)
	x.emit(ctx, notify.EventRunSucceeded)
	return nil
}

// attempt runs a step with its timeout, retrying core.RetryableError with
// exponential backoff.
func (x *execution) attempt(ctx context.Context, name string) error {
	delay := x.e.opts.RetryDelay
	for n := 1; ; n++ {
		stepCtx, cancel := context.WithTimeout(ctx, stepTimeouts[name])
		err := x.runStep(stepCtx, name)
		cancel()
		if err == nil {
			err = x.checkConfig()
		}
		var retryable *core.RetryableError
		if err == nil || !errors.As(err, &retryable) || n >= x.e.opts.MaxAttempts {
			return err
		}
		x.logf(name, "attempt %d failed, retrying in %s: %v", n, delay, err)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay *= 2
	}
}

func (x *execution) checkConfig() error {
	if x.e.opts.Config == nil {
		return nil
	}
	return x.e.opts.Config()
}

func (x *execution) fail(ctx context.Context, rec *run.StepRecord, err error) error {
	rec.Status = stepFailed
	rec.Error = err.Error()
	x.r.Error = err.Error()

	var wait *core.WaitError
	if errors.As(err, &wait) {
		return x.wait(ctx, rec, wait)
	}

	var needs *core.NeedsInputError
	if errors.As(err, &needs) {
		x.r.Status = run.StatusNeedsInput
		if serr := x.save(ctx); serr != nil {
			return serr
		}
		x.logf(rec.Name, "needs input: %s", needs.Question)
		x.ticketComment(ctx, questionMessage(x.r, needs.Question))
		x.moveTicket(ctx, tracker.StageBlocked)
		x.emit(ctx, notify.EventRunNeedsInput)
		return nil
	}

	x.r.Status = run.StatusFailed
	if serr := x.save(ctx); serr != nil {
		return serr
	}
	x.logf(rec.Name, "failed: %v", err)
	x.moveTicket(ctx, tracker.StageBlocked)
	x.emit(ctx, notify.EventRunFailed)
	return fmt.Errorf("run %s failed at %s: %w", x.r.ID, rec.Name, err)
}

// wait parks the run: its step runs again after the wait, without this
// process. A new reason is announced; checking again on the same one isn't.
func (x *execution) wait(ctx context.Context, rec *run.StepRecord, w *core.WaitError) error {
	after := w.After
	if after <= 0 {
		after = time.Minute
	}
	announce := w.Reason != x.r.WaitingFor
	rec.Status, rec.Error = stepWaiting, ""
	x.r.Error = ""
	x.r.Status = run.StatusWaiting
	x.r.WaitingFor = w.Reason
	x.r.WaitUntil = x.e.opts.Now().Add(after)
	if err := x.save(ctx); err != nil {
		return err
	}
	x.logf(rec.Name, "waiting for %s; next check at %s", w.Reason, x.r.WaitUntil.UTC().Format(time.RFC3339))
	if announce {
		x.emit(ctx, notify.EventRunWaiting)
	}
	return nil
}

func (x *execution) skipReason(name string) string {
	if slices.Contains(x.p.Settings.DisabledSteps, name) {
		return "disabled"
	}
	noEnv := x.p.Provisioner == nil
	switch name {
	case StepProvisionEnvironment:
		if noEnv {
			return "no environment"
		}
	case StepDeployBase, StepDeploy:
		if noEnv || len(x.p.Deployers) == 0 {
			return "no deployers"
		}
	case StepRetrieve:
		if noEnv || len(x.retrievers()) == 0 {
			return "no deployer retrieves changes"
		}
	}
	return ""
}

func (x *execution) gated(name string) bool {
	gates := x.p.Settings.Gates
	if x.r.Request.Gates != nil {
		gates = x.r.Request.Gates
	}
	return slices.Contains(gates, name) && !slices.Contains(x.r.Approved, name)
}

// emit sends an event to every notifier subscribed to it. Notifier failures
// are logged, never fatal.
func (x *execution) emit(ctx context.Context, typ notify.EventType) {
	x.emitEvent(ctx, &notify.Event{Type: typ, Run: x.r.Snapshot, Message: eventMessage(x.r, typ)})
}

func (x *execution) emitEvent(ctx context.Context, ev *notify.Event) {
	for _, n := range x.p.Notifiers {
		if len(n.On) > 0 && !slices.Contains(n.On, ev.Type) {
			continue
		}
		if err := n.Notifier.Notify(ctx, ev); err != nil {
			x.logf("", "notifier failed for %s: %v", ev.Type, err)
		}
	}
}

// ticketID is the id to use for tracker calls.
func (x *execution) ticketID() string {
	if x.r.Ticket != nil && x.r.Ticket.Ref.ID != "" {
		return x.r.Ticket.Ref.ID
	}
	return x.r.Request.Ticket.ID
}

// ticketComment posts on the ticket; failures are logged.
func (x *execution) ticketComment(ctx context.Context, msg core.Message) {
	if err := x.p.Tracker.Comment(ctx, x.ticketID(), msg); err != nil {
		x.logf("", "commenting on the ticket failed: %v", err)
	}
}

// moveTicket moves the ticket if the tracker supports it; failures are logged.
func (x *execution) moveTicket(ctx context.Context, stage tracker.Stage) {
	m, ok := x.p.Tracker.(tracker.StageMover)
	if !ok {
		return
	}
	if err := m.MoveTo(ctx, x.ticketID(), stage); err != nil {
		x.logf("", "moving the ticket to %s failed: %v", stage, err)
	}
}
