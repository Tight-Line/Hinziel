// Package trackertest provides an in-memory tracker for tests and a
// conformance suite for tracker.Tracker implementations.
package trackertest

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/testutil"
	"github.com/Tight-Line/hinziel/tracker"
)

// Fake is an in-memory tracker.Tracker with every optional capability.
type Fake struct {
	mu       sync.Mutex
	tickets  map[string]*tracker.Ticket
	comments map[string][]core.Message
	stages   map[string]tracker.Stage
	links    map[string][]core.Link
	files    map[string]string // attachment ID -> content
	fetches  int

	FetchErrs []error // returned by the next Fetch calls, in order
}

var (
	_ tracker.Tracker    = (*Fake)(nil)
	_ tracker.StageMover = (*Fake)(nil)
	_ tracker.Linker     = (*Fake)(nil)

	_ tracker.AttachmentFetcher = (*Fake)(nil)
	_ tracker.DoneChecker       = (*Fake)(nil)
)

// NewFake returns a tracker holding tickets, keyed by Ref.ID (set from Key if empty).
func NewFake(tickets ...tracker.Ticket) *Fake {
	f := &Fake{
		tickets:  map[string]*tracker.Ticket{},
		comments: map[string][]core.Message{},
		stages:   map[string]tracker.Stage{},
		links:    map[string][]core.Link{},
		files:    map[string]string{},
	}
	for _, t := range tickets {
		if t.Ref.ID == "" {
			t.Ref.ID = t.Key
		}
		f.tickets[t.Ref.ID] = &t
	}
	return f
}

func (f *Fake) Fetch(_ context.Context, id string) (*tracker.Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fetches++
	if err := testutil.PopErr(&f.FetchErrs); err != nil {
		return nil, err
	}
	t, ok := f.tickets[id]
	if !ok {
		return nil, fmt.Errorf("ticket %q not found", id)
	}
	c := *t
	c.Comments = slices.Clone(t.Comments)
	return &c, nil
}

func (f *Fake) Comment(_ context.Context, id string, msg core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tickets[id]; !ok {
		return fmt.Errorf("ticket %q not found", id)
	}
	f.comments[id] = append(f.comments[id], msg)
	return nil
}

func (f *Fake) MoveTo(_ context.Context, id string, stage tracker.Stage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stages[id] = stage
	return nil
}

// Done reports whether the ticket was moved to tracker.StageDone, or doesn't exist.
func (f *Fake) Done(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, exists := f.tickets[id]
	return !exists || f.stages[id] == tracker.StageDone, nil
}

func (f *Fake) AddLink(_ context.Context, id string, link core.Link) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, l := range f.links[id] {
		if l.URL == link.URL {
			return nil
		}
	}
	f.links[id] = append(f.links[id], link)
	return nil
}

// AddFile attaches a file with content to a ticket, as if uploaded to the tracker.
func (f *Fake) AddFile(id, attachmentID, name, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tickets[id]
	t.Attachments = append(t.Attachments, tracker.Attachment{
		ID: attachmentID, Name: name, URL: "https://tracker.test/files/" + attachmentID, IsFile: true,
	})
	f.files[attachmentID] = content
}

// AddAttachment attaches a as it is, with no content behind it: a link, or
// (with IsFile) a file the tracker fails to serve.
func (f *Fake) AddAttachment(id string, a tracker.Attachment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tickets[id]
	t.Attachments = append(t.Attachments, a)
}

func (f *Fake) FetchAttachment(_ context.Context, _ string, a tracker.Attachment) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	content, ok := f.files[a.ID]
	if !ok {
		return nil, fmt.Errorf("attachment %q not found", a.ID)
	}
	return io.NopCloser(strings.NewReader(content)), nil
}

// AddComment adds a comment to a ticket, as a human answering on the tracker would.
func (f *Fake) AddComment(id, author, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.tickets[id]
	t.Comments = append(t.Comments, tracker.Comment{Author: author, Body: body, At: time.Now()})
}

// Comments returns the messages posted to a ticket through Comment.
func (f *Fake) Comments(id string) []core.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[id])
}

// Stage returns the last stage a ticket was moved to.
func (f *Fake) Stage(id string) tracker.Stage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stages[id]
}

// Links returns the links added to a ticket.
func (f *Fake) Links(id string) []core.Link {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.links[id])
}

// Fetches counts Fetch calls.
func (f *Fake) Fetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

// Run checks tr against the tracker contract, using ticketID, which must
// exist. It posts a comment and, if supported, adds a link and moves the
// ticket, so point it at a sandbox ticket.
func Run(t *testing.T, tr tracker.Tracker, ticketID string) {
	ctx := context.Background()

	t.Run("Fetch", func(t *testing.T) {
		tk, err := tr.Fetch(ctx, ticketID)
		if err != nil {
			t.Fatalf("Fetch(%q): %v", ticketID, err)
		}
		if tk.Ref.ID == "" || tk.Key == "" || tk.Title == "" {
			t.Fatalf("ticket needs Ref.ID, Key and Title: %+v", tk)
		}
		again, err := tr.Fetch(ctx, tk.Ref.ID)
		if err != nil || again.Key != tk.Key {
			t.Fatalf("Fetch(Ref.ID) must return the same ticket: %+v, %v", again, err)
		}
	})
	t.Run("FetchMissing", func(t *testing.T) {
		if _, err := tr.Fetch(ctx, "hinziel-conformance-missing"); err == nil {
			t.Fatal("Fetch of a missing ticket must fail")
		}
	})
	t.Run("Comment", func(t *testing.T) {
		msg := core.Message{Title: "Hinziel conformance test", Body: "This comment was posted by **trackertest**."}
		if err := tr.Comment(ctx, ticketID, msg); err != nil {
			t.Fatal(err)
		}
	})
	if l, ok := tr.(tracker.Linker); ok {
		t.Run("AddLinkIsIdempotent", func(t *testing.T) {
			link := core.Link{Title: "Hinziel conformance", URL: "https://example.com/hinziel-conformance"}
			for range 2 {
				if err := l.AddLink(ctx, ticketID, link); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	if m, ok := tr.(tracker.StageMover); ok {
		t.Run("MoveTo", func(t *testing.T) {
			if err := m.MoveTo(ctx, ticketID, tracker.StageInProgress); err != nil {
				t.Fatal(err)
			}
			if err := m.MoveTo(ctx, ticketID, tracker.Stage("hinziel-unmapped-stage")); err != nil {
				t.Fatalf("an unmapped stage must be a no-op: %v", err)
			}
		})
	}
}
