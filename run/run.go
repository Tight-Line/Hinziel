// Package run defines a pipeline run: its request, status, persisted state and
// the Store that keeps it. It is data only; the engine lives in internal/engine.
package run

import (
	"context"
	"time"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/tracker"
)

type ID string

type Request struct {
	Factory     string            `json:"factory"` // set by the Dispatcher a trigger is given
	Ticket      tracker.TicketRef `json:"ticket"`
	RequestedBy string            `json:"requested_by"`    // free-form: "cli:lpapez", "trello:<memberId>"
	Gates       []string          `json:"gates,omitempty"` // step names; override the factory's gates for this run
}

type Status string

const (
	StatusQueued           Status = "queued"
	StatusRunning          Status = "running"
	StatusAwaitingApproval Status = "awaiting_approval"
	StatusWaiting          Status = "waiting" // a step waits on someone or something else (core.WaitError)
	StatusNeedsInput       Status = "needs_input"
	StatusSucceeded        Status = "succeeded"
	StatusFailed           Status = "failed"
	StatusCancelled        Status = "cancelled"
)

// Snapshot is the read-only view of a run that triggers and notifiers get.
type Snapshot struct {
	ID          ID              `json:"id"`
	Factory     string          `json:"factory"`
	Status      Status          `json:"status"`
	CurrentStep string          `json:"current_step"`
	Ticket      *tracker.Ticket `json:"ticket,omitempty"`
	Branch      string          `json:"branch,omitempty"` // same name in every repo
	// ChangeRequests has one entry per repo the run changed, keyed by repo name.
	// Repos without changes get no change request.
	ChangeRequests map[string]*forge.ChangeRequest `json:"change_requests,omitempty"`
	Environment    *env.Environment                `json:"environment,omitempty"`
	// Deployments holds the latest deployment per deployer, keyed by deployer name.
	Deployments map[string]*deploy.Deployment `json:"deployments,omitempty"`
	// WaitingFor and WaitUntil say what a waiting run waits for, and when its
	// step is next checked.
	WaitingFor string    `json:"waiting_for,omitempty"`
	WaitUntil  time.Time `json:"wait_until,omitempty"`
	// Actions are the human actions of the run's environment and their progress.
	Actions   []Action  `json:"actions,omitempty"`
	Error     string    `json:"error,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Run is the mutable state passed between steps and persisted after each one.
type Run struct {
	Snapshot
	Request Request           `json:"request"`
	Commits map[string]string `json:"commits,omitempty"` // repo name -> pushed head
	Outcome *agent.Outcome    `json:"outcome,omitempty"`
	// Approved lists the gates (step names) this run has been approved past.
	Approved []string     `json:"approved,omitempty"`
	Steps    []StepRecord `json:"steps"`
}

// Action is a human action of the run's environment (env.Action), with its progress.
type Action struct {
	env.Action
	Announced bool `json:"announced,omitempty"` // run.action_required was emitted
	Done      bool `json:"done,omitempty"`      // someone confirmed it (approve)
}

type StepRecord struct {
	Name     string    `json:"name"`
	Status   string    `json:"status"` // pending|running|waiting|done|skipped|failed
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
	Error    string    `json:"error,omitempty"`
}

// Store persists runs. v1: one JSON file per run under $XDG_STATE_HOME/heinzel.
type Store interface {
	Save(ctx context.Context, r *Run) error
	Load(ctx context.Context, id ID) (*Run, error)
	List(ctx context.Context, f Filter) ([]Snapshot, error)
}

type Filter struct {
	Factory string
	Ticket  *tracker.TicketRef
	Status  []Status
	Limit   int
}
