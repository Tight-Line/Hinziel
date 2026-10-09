package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Tight-Line/heinzel/run"
	"github.com/Tight-Line/heinzel/tracker"
)

func newRun(s *FS, at time.Time, factory, ticket string, status run.Status) *run.Run {
	r := &run.Run{Request: run.Request{Factory: factory, Ticket: tracker.TicketRef{ID: ticket}}}
	r.ID = s.NewID(at)
	r.Factory = factory
	r.Status = status
	return r
}

func TestSaveLoadRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _ := New(t.TempDir())
	r := newRun(s, time.Now(), "blanktracker", "T-1", run.StatusRunning)
	r.Commits = map[string]string{"app": "abc123"}
	if err := s.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != run.StatusRunning || got.Commits["app"] != "abc123" || got.Request.Ticket.ID != "T-1" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if _, err := s.Load(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestListFiltersNewestFirst(t *testing.T) {
	ctx := context.Background()
	s, _ := New(t.TempDir())
	base := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	for i, c := range []struct {
		factory, ticket string
		status          run.Status
	}{
		{"a", "T-1", run.StatusSucceeded},
		{"a", "T-1", run.StatusFailed},
		{"a", "T-2", run.StatusRunning},
		{"b", "T-1", run.StatusRunning},
	} {
		s.Save(ctx, newRun(s, base.Add(time.Duration(i)*time.Minute), c.factory, c.ticket, c.status))
	}

	all, _ := s.List(ctx, run.Filter{})
	if len(all) != 4 || all[0].Factory != "b" {
		t.Fatalf("want 4 runs newest first, got %+v", all)
	}
	got, _ := s.List(ctx, run.Filter{Factory: "a", Ticket: &tracker.TicketRef{ID: "T-1"}})
	if len(got) != 2 || got[0].Status != run.StatusFailed {
		t.Fatalf("got %+v", got)
	}
	got, _ = s.List(ctx, run.Filter{Status: []run.Status{run.StatusRunning}, Limit: 1})
	if len(got) != 1 || got[0].Factory != "b" {
		t.Fatalf("got %+v", got)
	}
}

func TestLockIsExclusive(t *testing.T) {
	s, _ := New(t.TempDir())
	unlock, err := s.Lock("r1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Lock("r1"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	again, err := s.Lock("r1")
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	again()
}
