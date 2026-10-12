package clitest

import (
	"bytes"
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel"
	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trackertest"
	"github.com/Tight-Line/hinziel/trigger"
)

// syncBuffer is a bytes.Buffer the daemon's goroutines can share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// submitOnce asks for a run of D-1 twice, as two comments on one card would,
// and stops the daemon once the first run has finished.
type submitOnce struct {
	stop          context.CancelFunc
	first, second error
	final         *run.Snapshot
}

func (s *submitOnce) Start(ctx context.Context, d trigger.Dispatcher, _ *http.ServeMux) error {
	go func() {
		ticket := tracker.TicketRef{ID: "D-1"}
		var id run.ID
		id, s.first = d.Submit(ctx, run.Request{Ticket: ticket, RequestedBy: "test"})
		_, s.second = d.Submit(ctx, run.Request{Ticket: ticket, RequestedBy: "test"})
		for ctx.Err() == nil {
			if r, _ := d.Get(ctx, id); r != nil && r.Status == run.StatusSucceeded {
				s.final, _ = d.Latest(ctx, ticket)
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.stop()
	}()
	return nil
}

func TestDaemonExecutesRunsFromTriggers(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		os.WriteFile(filepath.Join(task.Repos[0].Path, "hello.txt"), []byte("hello\n"), 0o644)
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trig := &submitOnce{stop: cancel}
	f.Triggers = []hinziel.Trigger{{Name: "test", Trigger: trig}}

	var out syncBuffer
	if err := hinziel.Run(ctx, []string{"daemon"}, &out, f); err != nil {
		t.Fatalf("daemon: %v\n%s", err, out.String())
	}
	if trig.first != nil {
		t.Fatalf("first submit: %v", trig.first)
	}
	if trig.second == nil || !strings.Contains(trig.second.Error(), "already has run") {
		t.Errorf("a second run for the same ticket must be refused: %v", trig.second)
	}
	if trig.final == nil || trig.final.Status != run.StatusSucceeded || trig.final.Factory != "demo" {
		t.Fatalf("Latest: %+v\n%s", trig.final, out.String())
	}
	for _, want := range []string{"trigger demo/test started", "queued for ticket D-1 (test)", "succeeded"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestDaemonWithoutTriggersRefuses(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	_, err := runCLI(t, cliFactory(t, nil), "daemon")
	if err == nil || !strings.Contains(err.Error(), "no factory has triggers") {
		t.Fatalf("got %v", err)
	}
}

// tearDownAfterRun runs D-1, then asks for its environment's teardown, as a
// "teardown" comment would, and stops the daemon.
type tearDownAfterRun struct {
	stop     context.CancelFunc
	env      string
	err      error
	statuses []run.Status
}

func (s *tearDownAfterRun) Start(ctx context.Context, d trigger.Dispatcher, _ *http.ServeMux) error {
	go func() {
		defer s.stop()
		ticket := tracker.TicketRef{ID: "D-1"}
		id, err := d.Submit(ctx, run.Request{Ticket: ticket, RequestedBy: "test"})
		if err != nil {
			s.err = err
			return
		}
		for ctx.Err() == nil {
			if r, _ := d.Get(ctx, id); r != nil && r.Status == run.StatusSucceeded {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		s.env, s.err = d.Teardown(ctx, ticket, "trello:ante")
	}()
	return nil
}

func TestDaemonTearsDownOnRequest(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		os.WriteFile(filepath.Join(task.Repos[0].Path, "hello.txt"), []byte("hello\n"), 0o644)
		return nil, nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	trig := &tearDownAfterRun{stop: cancel}
	f.Triggers = []hinziel.Trigger{{Name: "test", Trigger: trig}}

	var out syncBuffer
	if err := hinziel.Run(ctx, []string{"daemon"}, &out, f); err != nil {
		t.Fatalf("daemon: %v\n%s", err, out.String())
	}
	if trig.err != nil || trig.env != "hz-demo-d-1" {
		t.Fatalf("teardown: %q, %v\n%s", trig.env, trig.err, out.String())
	}
	if envs, _ := f.Environment.(env.Lister).List(context.Background()); len(envs) != 0 {
		t.Errorf("environments left: %+v", envs)
	}
	if !strings.Contains(out.String(), "environment hz-demo-d-1 destroyed (asked for by trello:ante)") {
		t.Errorf("output:\n%s", out.String())
	}
}

func TestDaemonSweepsWithoutTriggers(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	f := cliFactory(t, nil)
	f.Teardown = hinziel.Teardown{After: time.Hour}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := hinziel.Run(ctx, []string{"daemon"}, &bytes.Buffer{}, f); err != nil {
		t.Fatalf("a factory with a Teardown gives the daemon something to do: %v", err)
	}
}

// submitTwo asks for runs of two tickets at once.
type submitTwo struct{ errs []error }

func (s *submitTwo) Start(ctx context.Context, d trigger.Dispatcher, _ *http.ServeMux) error {
	for _, id := range []string{"D-1", "D-2"} {
		_, err := d.Submit(ctx, run.Request{Ticket: tracker.TicketRef{ID: id}, RequestedBy: "test"})
		s.errs = append(s.errs, err)
	}
	return nil
}

// With --runs 2, the agents of two tickets work at the same time.
func TestDaemonRunsInParallel(t *testing.T) {
	t.Setenv("HINZIEL_STATE_DIR", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var mu sync.Mutex
	working, most, done := 0, 0, 0
	both := make(chan struct{})
	f := cliFactory(t, func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		mu.Lock()
		working++
		most = max(most, working)
		if working == 2 {
			close(both)
		}
		mu.Unlock()
		select { // wait for the other agent, or give up and let the test fail
		case <-both:
		case <-time.After(5 * time.Second):
		}
		os.WriteFile(filepath.Join(task.Repos[0].Path, "hello.txt"), []byte(task.Ticket.Key+"\n"), 0o644)
		mu.Lock()
		working--
		done++
		if done == 2 {
			defer cancel()
		}
		mu.Unlock()
		return nil, nil
	})
	f.Tracker = trackertest.NewFake(tracker.Ticket{Key: "D-1", Title: "One"}, tracker.Ticket{Key: "D-2", Title: "Two"})
	trig := &submitTwo{}
	f.Triggers = []hinziel.Trigger{{Name: "test", Trigger: trig}}

	var out syncBuffer
	if err := hinziel.Run(ctx, []string{"daemon", "--runs", "2"}, &out, f); err != nil {
		t.Fatalf("daemon: %v\n%s", err, out.String())
	}
	if trig.errs[0] != nil || trig.errs[1] != nil {
		t.Fatalf("submit: %v", trig.errs)
	}
	if most != 2 {
		t.Errorf("at most %d agents worked at once, want 2\n%s", most, out.String())
	}
	if !strings.Contains(out.String(), "up to 2 at once") {
		t.Errorf("output:\n%s", out.String())
	}
}
