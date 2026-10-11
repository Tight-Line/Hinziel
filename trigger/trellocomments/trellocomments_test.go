package trellocomments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
)

// fakeTrello serves one board's comments and records replies.
type fakeTrello struct {
	mu       sync.Mutex
	actions  []action // oldest first
	replies  map[string][]string
	nextID   int
	authOK   bool
	requests int
}

func (f *fakeTrello) comment(by, card, text string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	a := action{ID: fmt.Sprintf("a%03d", f.nextID), IDMemberCreator: "id-" + by, MemberCreator: member{ID: "id-" + by, Username: by}}
	a.Data.Text = text
	a.Data.Card.ID, a.Data.Card.ShortLink = "card-"+card, card
	f.actions = append(f.actions, a)
}

func (f *fakeTrello) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	if !strings.Contains(r.Header.Get("Authorization"), `oauth_token="tok"`) {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/1/members/me":
		json.NewEncoder(w).Encode(member{ID: "id-hinziel", Username: "hinziel"})
	case r.URL.Path == "/1/boards/B/actions":
		// Like Trello: newest first, after `since` and before `before`
		// (action IDs), the newest `limit` of them.
		q := r.URL.Query()
		var out []action
		for i := len(f.actions) - 1; i >= 0 && f.actions[i].ID != q.Get("since"); i-- {
			if q.Get("before") == "" || f.actions[i].ID < q.Get("before") {
				out = append(out, f.actions[i])
			}
		}
		if n, _ := strconv.Atoi(r.URL.Query().Get("limit")); n > 0 && len(out) > n {
			out = out[:n]
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && strings.HasPrefix(r.URL.Path, "/1/cards/"):
		var body struct{ Text string }
		json.NewDecoder(r.Body).Decode(&body)
		card := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/1/cards/"), "/actions/comments")
		f.replies[card] = append(f.replies[card], body.Text)
		w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTrello) repliesTo(card string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies["card-"+card])
}

// fakeDispatcher accepts one run per ticket.
type fakeDispatcher struct {
	mu   sync.Mutex
	reqs []run.Request
}

func (d *fakeDispatcher) Submit(_ context.Context, req run.Request) (run.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.reqs {
		if r.Ticket == req.Ticket {
			return "", errors.New("ticket already has run r1 (running)")
		}
	}
	d.reqs = append(d.reqs, req)
	return run.ID("r" + strconv.Itoa(len(d.reqs))), nil
}

func (d *fakeDispatcher) Get(context.Context, run.ID) (*run.Snapshot, error) { return nil, nil }
func (d *fakeDispatcher) Latest(context.Context, tracker.TicketRef) (*run.Snapshot, error) {
	return nil, nil
}

func (d *fakeDispatcher) requests() []run.Request {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.reqs)
}

func newTest(t *testing.T) (*Trigger, *fakeTrello) {
	f := &fakeTrello{replies: map[string][]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return New(Options{APIKey: "key", Token: "tok", Board: "B", Interval: 5 * time.Millisecond, BaseURL: srv.URL}), f
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCommentsStartRuns(t *testing.T) {
	tr, f := newTest(t)
	f.comment("ante", "old", "@hinziel implement") // before Start: ignored
	d := &fakeDispatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tr.Start(ctx, d, http.NewServeMux()); err != nil {
		t.Fatal(err)
	}

	f.comment("ante", "c1", "@hinziel implement")
	f.comment("ante", "c2", "we should implement this later")         // no mention
	f.comment("hinziel", "c3", "@hinziel implement")                  // its own comment
	f.comment("ante", "c4", "@Hinziel please IMPLEMENT this, thanks") // any case
	f.comment("ante", "c5", "@hinzielbot implement")                  // another member
	f.comment("ante", "c6", "@hinziel implementation notes")          // not the word
	f.comment("luka", "c1", "@hinziel implement")                     // c1 already has a run

	eventually(t, "the second reply on c1", func() bool { return len(f.repliesTo("c1")) == 2 })
	eventually(t, "a reply on c4", func() bool { return len(f.repliesTo("c4")) == 1 })

	var tickets []string
	for _, r := range d.requests() {
		tickets = append(tickets, r.Ticket.ID+" by "+r.RequestedBy)
	}
	if want := []string{"c1 by trello:ante", "c4 by trello:ante"}; !slices.Equal(tickets, want) {
		t.Errorf("runs requested: %v, want %v", tickets, want)
	}
	if r := f.repliesTo("c1"); r[0] != "On it, @ante: started run `r1`." || !strings.Contains(r[1], "@luka, I couldn't start a run: ticket already has run") {
		t.Errorf("replies on c1: %q", r)
	}
	for _, card := range []string{"old", "c2", "c3", "c5", "c6"} {
		if r := f.repliesTo(card); len(r) > 0 {
			t.Errorf("card %s must be left alone, got %q", card, r)
		}
	}
}

// A burst of comments longer than a page is read in full, in order.
func TestPollReadsEveryPage(t *testing.T) {
	tr, f := newTest(t)
	d := &fakeDispatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := tr.Start(ctx, d, http.NewServeMux()); err != nil {
		t.Fatal(err)
	}
	n := pageSize + 7
	for i := range n {
		f.comment("ante", "p"+strconv.Itoa(i), "@hinziel implement")
	}
	eventually(t, "a run per comment", func() bool { return len(d.requests()) == n })
	for i, r := range d.requests() {
		if r.Ticket.ID != "p"+strconv.Itoa(i) {
			t.Fatalf("run %d is for %s, want p%d: comments must be handled oldest first", i, r.Ticket.ID, i)
		}
	}
}

func TestCheck(t *testing.T) {
	tr, _ := newTest(t)
	if err := tr.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.opts.Board = ""
	if err := tr.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "not set: Board") {
		t.Errorf("an empty board must fail the check: %v", err)
	}
}
