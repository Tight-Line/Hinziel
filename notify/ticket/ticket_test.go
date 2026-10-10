package ticket

import (
	"context"
	"testing"

	"github.com/Tight-Line/hinzele/core"
	"github.com/Tight-Line/hinzele/notify"
	"github.com/Tight-Line/hinzele/notify/notifytest"
	"github.com/Tight-Line/hinzele/run"
	"github.com/Tight-Line/hinzele/tracker"
	"github.com/Tight-Line/hinzele/tracker/trackertest"
)

func TestConformance(t *testing.T) {
	tr := trackertest.NewFake(tracker.Ticket{Key: "HZ-1", Title: "x"})
	notifytest.Run(t, New(tr))
}

func TestNotify(t *testing.T) {
	tr := trackertest.NewFake(tracker.Ticket{Key: "T-1", Title: "Export"})
	n := New(tr)
	ctx := context.Background()
	snap := run.Snapshot{Ticket: &tracker.Ticket{Ref: tracker.TicketRef{ID: "T-1"}, Key: "T-1"}}
	msg := core.Message{Title: "action needed"}

	for _, typ := range []notify.EventType{notify.EventRunActionRequired, notify.EventRunNeedsInput, notify.EventRunFailed} {
		if err := n.Notify(ctx, &notify.Event{Type: typ, Run: snap, Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	if err := n.Notify(ctx, &notify.Event{Type: notify.EventRunStarted, Message: msg}); err != nil {
		t.Fatal("an event before the ticket is fetched must be skipped, not fail")
	}
	if got := len(tr.Comments("T-1")); got != 2 {
		t.Errorf("%d comments, want 2 (needs_input is posted by the engine)", got)
	}
}
