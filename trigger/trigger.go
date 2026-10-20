// Package trigger defines how runs get started (webhooks,
// pollers such as Trello comments, …). The CLI is not a trigger; it calls the engine directly.
// Implementations live in subpackages.
package trigger

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// Trigger starts runs for the factory it belongs to.
type Trigger interface {
	// Start begins accepting run requests and must return promptly. A trigger
	// may register HTTP handlers on mux (served by `daemon` under
	// /triggers/<factory>/<trigger>/, with that prefix stripped) and/or start
	// background work, such as a poller, that stops when ctx is done.
	Start(ctx context.Context, d Dispatcher, mux *http.ServeMux) error
}

// ErrNoQuestion is Dispatcher.Answer's error when the ticket has no run
// waiting for an answer.
var ErrNoQuestion = errors.New("no run of this ticket is waiting for an answer")

// Dispatcher is the engine as seen by one factory's triggers. Run requests it
// receives are always for that factory.
type Dispatcher interface {
	Submit(ctx context.Context, req run.Request) (run.ID, error)
	Get(ctx context.Context, id run.ID) (*run.Snapshot, error)
	// Latest returns the most recent run for a ticket, or nil.
	Latest(ctx context.Context, ticket tracker.TicketRef) (*run.Snapshot, error)
	// Answer gives text as the answer to the question the ticket's latest
	// run waits on (status needs_input), by whoever by is, and continues the
	// run. With no such run it returns trigger.ErrNoQuestion.
	Answer(ctx context.Context, ticket tracker.TicketRef, text, by string) (run.ID, error)
	// Teardown destroys the ticket's environment and returns its name. It
	// refuses while the ticket has an unfinished run (failed runs aside).
	// Its success is announced like a run's progress, by an
	// environment.destroyed notification.
	Teardown(ctx context.Context, ticket tracker.TicketRef, requestedBy string) (string, error)
}
