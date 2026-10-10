// Package notify defines how people are told about runs (chat, email, ticket
// comments, …). Implementations live in subpackages.
package notify

import (
	"context"

	"github.com/Tight-Line/hinzele/core"
	"github.com/Tight-Line/hinzele/env"
	"github.com/Tight-Line/hinzele/run"
)

// Notifier tells people things. Routing (which events, which audience) is
// part of the factory: one notifier per audience/channel, each with its events.
type Notifier interface {
	Notify(ctx context.Context, ev *Event) error
}

type EventType string

const (
	EventRunStarted          EventType = "run.started"
	EventRunSucceeded        EventType = "run.succeeded"
	EventRunFailed           EventType = "run.failed"
	EventRunNeedsInput       EventType = "run.needs_input"
	EventRunAwaitingApproval EventType = "run.awaiting_approval"
	EventRunActionRequired   EventType = "run.action_required" // one per human action; Event.Action says which
	EventRunWaiting          EventType = "run.waiting"         // a step started waiting on something new
)

// Common event sets for routing notifiers.
var (
	Finished = []EventType{EventRunSucceeded, EventRunFailed}
	Problems = []EventType{EventRunFailed, EventRunNeedsInput, EventRunAwaitingApproval, EventRunActionRequired}
)

type Event struct {
	Type    EventType    `json:"type"`
	Run     run.Snapshot `json:"run"`              // structured data for plugins that want it
	Message core.Message `json:"message"`          // engine-rendered default text
	Action  *env.Action  `json:"action,omitempty"` // for run.action_required
}
