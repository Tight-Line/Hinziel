// Package trigger defines how runs get started (Trello Power-Up, webhooks,
// pollers, …). The CLI is not a trigger; it calls the engine directly.
// Implementations live in subpackages.
package trigger

import (
	"context"
	"net/http"

	"github.com/Tight-Line/hinzele/run"
	"github.com/Tight-Line/hinzele/tracker"
)

// Trigger starts runs for the factory it belongs to.
type Trigger interface {
	// Start begins accepting run requests and must return promptly. A trigger
	// may register HTTP handlers on mux (served by `daemon` under
	// /triggers/<factory>/<trigger>/, with that prefix stripped) and/or start
	// background work, such as a poller, that stops when ctx is done.
	Start(ctx context.Context, d Dispatcher, mux *http.ServeMux) error
}

// Dispatcher is the engine as seen by one factory's triggers. Run requests it
// receives are always for that factory.
type Dispatcher interface {
	Submit(ctx context.Context, req run.Request) (run.ID, error)
	Get(ctx context.Context, id run.ID) (*run.Snapshot, error)
	// Latest returns the most recent run for a ticket, or nil. Used for status badges.
	Latest(ctx context.Context, ticket tracker.TicketRef) (*run.Snapshot, error)
}
