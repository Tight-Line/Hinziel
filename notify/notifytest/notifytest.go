// Package notifytest provides a recording notifier for tests and a
// conformance suite for notify.Notifier implementations.
package notifytest

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/internal/testutil"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// Fake records every event it is sent.
type Fake struct {
	mu     sync.Mutex
	events []notify.Event

	NotifyErrs []error // returned by the next Notify calls, in order
}

var _ notify.Notifier = (*Fake)(nil)

func (f *Fake) Notify(_ context.Context, ev *notify.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := testutil.PopErr(&f.NotifyErrs); err != nil {
		return err
	}
	f.events = append(f.events, *ev)
	return nil
}

// Events returns the events received, in order.
func (f *Fake) Events() []notify.Event {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.events)
}

// Types returns the types of the events received, in order.
func (f *Fake) Types() []notify.EventType {
	var out []notify.EventType
	for _, e := range f.Events() {
		out = append(out, e.Type)
	}
	return out
}

// Run checks n against the notifier contract by sending one event of every
// type. Point it at a test channel.
func Run(t *testing.T, n notify.Notifier) {
	snap := run.Snapshot{
		ID:        "20261009-120000-abcdef",
		Factory:   "hinziel-conformance",
		Ticket:    &tracker.Ticket{Key: "HZ-1", Title: "Conformance test", URL: "https://example.com/HZ-1"},
		Branch:    "hinziel/hz-1-conformance-test",
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	for _, typ := range []notify.EventType{
		notify.EventRunStarted, notify.EventRunSucceeded, notify.EventRunFailed,
		notify.EventRunNeedsInput, notify.EventRunAwaitingApproval, notify.EventRunActionRequired, notify.EventRunWaiting,
	} {
		t.Run(string(typ), func(t *testing.T) {
			ev := &notify.Event{
				Type:    typ,
				Run:     snap,
				Message: core.Message{Title: "Hinziel conformance: " + string(typ), Body: "Sent by **notifytest**."},
			}
			if typ == notify.EventRunActionRequired {
				ev.Action = &env.Action{ID: "conformance", Title: "Conformance action", Body: "Nothing to do.", Before: "publish"}
			}
			if err := n.Notify(context.Background(), ev); err != nil {
				t.Fatal(err)
			}
		})
	}
}
