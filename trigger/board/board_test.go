package board

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/trigger"
)

type fakeTickets struct {
	tickets []tracker.Ticket
	err     error
}

func (f *fakeTickets) List(context.Context) ([]tracker.Ticket, error) { return f.tickets, f.err }

func ticket(id, title string) tracker.Ticket {
	return tracker.Ticket{Ref: tracker.TicketRef{ID: id}, Key: "#" + id, Title: title, URL: "https://github.test/acme/app/issues/" + id}
}

// fakeDispatcher has a latest run for the tickets in latest, and accepts
// one run per ticket.
type fakeDispatcher struct {
	mu      sync.Mutex
	latest  map[string]*run.Snapshot
	reqs    []run.Request
	resumed []string
}

func (d *fakeDispatcher) Submit(_ context.Context, req run.Request) (run.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if s := d.latest[req.Ticket.ID]; s != nil && s.Status == run.StatusRunning {
		return "", errors.New("ticket already has run " + string(s.ID) + " (running)")
	}
	d.reqs = append(d.reqs, req)
	return "r9", nil
}

func (d *fakeDispatcher) Get(context.Context, run.ID) (*run.Snapshot, error) { return nil, nil }

func (d *fakeDispatcher) Resume(_ context.Context, ticket tracker.TicketRef, by string) (run.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.latest[ticket.ID]
	if s == nil || s.Status != run.StatusFailed {
		return "", trigger.ErrNotFailed
	}
	d.resumed = append(d.resumed, ticket.ID+" by "+by)
	return s.ID, nil
}

func (d *fakeDispatcher) Latest(_ context.Context, ticket tracker.TicketRef) (*run.Snapshot, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.latest[ticket.ID], nil
}

func (d *fakeDispatcher) Answer(context.Context, tracker.TicketRef, string, string) (run.ID, error) {
	return "", nil
}

func (d *fakeDispatcher) Teardown(context.Context, tracker.TicketRef, string) (string, error) {
	return "", nil
}

var now = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

func newTest(t *testing.T) (*httptest.Server, *fakeTickets, *fakeDispatcher) {
	tickets := &fakeTickets{tickets: []tracker.Ticket{
		ticket("1", "Add CSV export"),
		ticket("2", "Fix the <blank> card"),
		ticket("3", "Renewal date"),
		ticket("4", "Churn on cancel"),
		ticket("5", "Settings"),
		ticket("6", "Dashboard"),
	}}
	d := &fakeDispatcher{latest: map[string]*run.Snapshot{
		"2": {ID: "r2", Status: run.StatusFailed, Error: "deploy failed", UpdatedAt: now.Add(-5 * time.Minute)},
		"3": {ID: "r3", Status: run.StatusRunning, CurrentStep: "run_agent", StartedAt: now.Add(-3 * time.Minute)},
		"4": {ID: "r4", Status: run.StatusSucceeded, UpdatedAt: now.Add(-2 * time.Hour),
			ChangeRequests: map[string]*forge.ChangeRequest{"app": {URL: "https://github.test/acme/app/pull/12"}},
			Environment:    &env.Environment{Endpoints: map[string]string{"org": "https://org.test/login"}}},
		"5": {ID: "r5", Status: run.StatusNeedsInput, StartedAt: now.Add(-time.Minute)},
		"6": {ID: "r6", Status: run.StatusCancelled, UpdatedAt: now.Add(-time.Hour)},
	}}
	b := New(Options{Tickets: tickets, Title: "sfplay"})
	b.now = func() time.Time { return now }
	mux := http.NewServeMux()
	if err := b.Start(context.Background(), d, mux); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, tickets, d
}

func get(t *testing.T, u string) string {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: %s\n%s", u, resp.Status, body)
	}
	return string(body)
}

// column returns the HTML of one of the page's columns.
func column(t *testing.T, page, class string) string {
	t.Helper()
	_, rest, ok := strings.Cut(page, `<section class="`+class+`">`)
	if !ok {
		t.Fatalf("no column %s in\n%s", class, page)
	}
	col, _, _ := strings.Cut(rest, "</section>")
	return col
}

func TestPage(t *testing.T) {
	srv, _, _ := newTest(t)
	page := get(t, srv.URL+"/")

	open := column(t, page, "open")
	for _, want := range []string{"#1", "Add CSV export", ">Implement<", "#2", "Fix the &lt;blank&gt; card",
		"last run failed: deploy failed · 5 min ago", `action="resume"`, ">Resume<", `name="ticket" value="1"`,
		"last run cancelled · 1 h ago", ">Implement again<"} {
		if !strings.Contains(open, want) {
			t.Errorf("open column lacks %q:\n%s", want, open)
		}
	}
	building := column(t, page, "building")
	for _, want := range []string{"#3", "run agent · started 3 min ago", "#5", "needs input: answer on the ticket"} {
		if !strings.Contains(building, want) {
			t.Errorf("building column lacks %q:\n%s", want, building)
		}
	}
	review := column(t, page, "review")
	for _, want := range []string{"#4", "built 2 h ago", `href="https://github.test/acme/app/pull/12"`, ">pull request<",
		`href="https://org.test/login"`, ">org<"} {
		if !strings.Contains(review, want) {
			t.Errorf("review column lacks %q:\n%s", want, review)
		}
	}
	for _, col := range []string{building, review} {
		if strings.Contains(col, "<button") {
			t.Errorf("only open tickets can be implemented:\n%s", col)
		}
	}
}

func TestPageWhenTheTrackerFails(t *testing.T) {
	srv, tickets, _ := newTest(t)
	tickets.tickets, tickets.err = nil, errors.New("github: 401 Bad credentials")
	if page := get(t, srv.URL+"/"); !strings.Contains(page, "Can't list the tickets: github: 401 Bad credentials") {
		t.Errorf("page doesn't say why it's empty:\n%s", page)
	}
}

func implement(t *testing.T, srv *httptest.Server, id string, header http.Header) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/implement", strings.NewReader(url.Values{"ticket": {id}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for k, v := range header {
		req.Header[k] = v
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestImplement(t *testing.T) {
	srv, _, d := newTest(t)
	cases := []struct {
		ticket, kind, text string
	}{
		{"1", "message", "Started run r9 for #1."},
		{"6", "message", "Started run r9 for #6."}, // a cancelled ticket can be tried again
		{"3", "error", "#3: ticket already has run r3 (running)"},
		{"99", "error", `ticket "99" isn't open`},
	}
	for _, c := range cases {
		resp := implement(t, srv, c.ticket, http.Header{"Sec-Fetch-Site": {"same-origin"}})
		if resp.StatusCode != http.StatusSeeOther {
			t.Fatalf("implement %s: %s", c.ticket, resp.Status)
		}
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if loc.Path != "./" || loc.Query().Get(c.kind) != c.text {
			t.Errorf("implement %s: back to %s, want %s %q", c.ticket, loc, c.kind, c.text)
		}
	}
	if len(d.reqs) != 2 || d.reqs[0].Ticket.ID != "1" || d.reqs[0].RequestedBy != "board" || d.reqs[1].Ticket.ID != "6" {
		t.Errorf("submitted %+v", d.reqs)
	}
	if page := get(t, srv.URL+"/?message=Started+run+r9+for+%231."); !strings.Contains(page, `<div class="flash">Started run r9 for #1.</div>`) {
		t.Errorf("the message isn't shown:\n%s", page)
	}
}

func TestResume(t *testing.T) {
	srv, _, d := newTest(t)
	for _, c := range []struct{ ticket, kind, text string }{
		{"2", "message", "Resumed run r2 for #2."},
		{"1", "error", "#1: " + trigger.ErrNotFailed.Error()},
	} {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/resume", strings.NewReader("ticket="+c.ticket))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusSeeOther || loc.Query().Get(c.kind) != c.text {
			t.Errorf("resume %s: %s, back to %s, want %s %q", c.ticket, resp.Status, loc, c.kind, c.text)
		}
	}
	if len(d.resumed) != 1 || d.resumed[0] != "2 by board" {
		t.Errorf("resumed %v", d.resumed)
	}
}

func TestImplementRefusesOtherSites(t *testing.T) {
	srv, _, d := newTest(t)
	for _, h := range []http.Header{
		{"Sec-Fetch-Site": {"cross-site"}},
		{"Origin": {"https://evil.test"}},
	} {
		if resp := implement(t, srv, "1", h); resp.StatusCode != http.StatusForbidden {
			t.Errorf("implement with %v: %s, want 403", h, resp.Status)
		}
	}
	if len(d.reqs) != 0 {
		t.Errorf("submitted %+v", d.reqs)
	}
}

func TestBasicAuth(t *testing.T) {
	d := &fakeDispatcher{}
	b := New(Options{Tickets: &fakeTickets{tickets: []tracker.Ticket{ticket("1", "Add CSV export")}},
		BasicAuth: base64.StdEncoding.EncodeToString([]byte("demo:s3cret"))})
	mux := http.NewServeMux()
	if err := b.Start(context.Background(), d, mux); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	for _, c := range []struct {
		method, path string
		user, pass   string
		want         int
	}{
		{"GET", "/", "", "", http.StatusUnauthorized},
		{"GET", "/", "demo", "wrong", http.StatusUnauthorized},
		{"GET", "/", "demo", "s3cret", http.StatusOK},
		{"POST", "/implement", "", "", http.StatusUnauthorized},
		{"POST", "/implement", "demo", "s3cret", http.StatusSeeOther},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader("ticket=1"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if c.user != "" {
			req.SetBasicAuth(c.user, c.pass)
		}
		client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("%s %s as %q: %s, want %d", c.method, c.path, c.user, resp.Status, c.want)
		}
		if c.want == http.StatusUnauthorized && !strings.HasPrefix(resp.Header.Get("WWW-Authenticate"), "Basic ") {
			t.Errorf("%s %s: no Basic challenge", c.method, c.path)
		}
	}
	if len(d.reqs) != 1 {
		t.Errorf("submitted %+v, want only the logged-in request", d.reqs)
	}
}

func TestBasicAuthMustBeEncoded(t *testing.T) {
	for _, bad := range []string{"demo:s3cret", "'ZGVtbzpzM2NyZXQ='", base64.StdEncoding.EncodeToString([]byte("nocolon"))} {
		err := New(Options{Tickets: &fakeTickets{}, BasicAuth: bad}).Start(context.Background(), &fakeDispatcher{}, http.NewServeMux())
		if err == nil {
			t.Errorf("BasicAuth %q must be refused", bad)
		}
	}
}

func TestStartNeedsTickets(t *testing.T) {
	if err := New(Options{}).Start(context.Background(), &fakeDispatcher{}, http.NewServeMux()); err == nil {
		t.Error("Start without Tickets must fail")
	}
}
