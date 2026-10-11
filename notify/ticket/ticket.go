// Package ticket is a notify.Notifier that comments on the run's ticket,
// through the factory's own tracker. It lets "tell the tester on the card"
// use the same event routing as "tell the dev channel".
//
// # Credentials
//
// None: it posts with the tracker it's given.
package ticket

import (
	"context"

	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/tracker"
)

// Notifier comments on tickets.
type Notifier struct{ tracker tracker.Tracker }

var _ notify.Notifier = (*Notifier)(nil)

// New returns a notifier that posts through tr, usually the factory's Tracker.
func New(tr tracker.Tracker) *Notifier { return &Notifier{tracker: tr} }

// Notify posts the event's message on the run's ticket. Events before the
// ticket was fetched are skipped, and so is run.needs_input: the engine
// posts the question on the ticket itself.
func (n *Notifier) Notify(ctx context.Context, ev *notify.Event) error {
	if ev.Type == notify.EventRunNeedsInput || ev.Run.Ticket == nil || ev.Run.Ticket.Ref.ID == "" {
		return nil
	}
	return n.tracker.Comment(ctx, ev.Run.Ticket.Ref.ID, ev.Message)
}
