// Package board is a trigger that serves a web page of the factory's
// tickets, in three columns: the open tickets, each with an Implement button
// that starts a run; the tickets being worked on; and the tickets whose run
// finished and wait for review. A ticket's column comes from its latest run:
// none, failed or cancelled is open; queued, running, waiting, needing input
// or awaiting approval is being worked on; succeeded is in review. Closed
// tickets leave the board, as the tracker no longer lists them.
//
// The page is served by `daemon --listen` at /triggers/<factory>/<trigger>/
// and refreshes itself. It has no login: anyone who can reach the address
// can start runs, so keep it on a private network, or behind a proxy that
// asks for one. Implement only accepts requests from the page's own origin
// (http.CrossOriginProtection), so another site can't start runs through a
// visitor's browser.
//
// # Credentials
//
// None of its own: the tickets come from Options.Tickets, usually the
// factory's tracker (it must implement tracker.Lister, as tracker/github
// does).
package board

import (
	"cmp"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/trigger"
)

// Options configure a board.
type Options struct {
	// Tickets lists the open tickets: the factory's tracker, usually.
	Tickets tracker.Lister
	// Title heads the page; "" = "Hinziel".
	Title string
	// Refresh is how often the page reloads itself; 0 = 10 seconds.
	Refresh time.Duration
}

// Board is the trigger.
type Board struct {
	opts Options
	d    trigger.Dispatcher
	now  func() time.Time
}

var _ trigger.Trigger = (*Board)(nil)

// New returns a board.
func New(opts Options) *Board {
	if opts.Title == "" {
		opts.Title = "Hinziel"
	}
	if opts.Refresh == 0 {
		opts.Refresh = 10 * time.Second
	}
	return &Board{opts: opts, now: time.Now}
}

func (b *Board) Start(_ context.Context, d trigger.Dispatcher, mux *http.ServeMux) error {
	if b.opts.Tickets == nil {
		return errors.New("board: Options.Tickets is required")
	}
	b.d = d
	mux.HandleFunc("GET /{$}", b.page)
	mux.Handle("POST /implement", http.NewCrossOriginProtection().Handler(http.HandlerFunc(b.implement)))
	return nil
}

// card is one ticket on the board.
type card struct {
	Ticket tracker.Ticket
	Run    *run.Snapshot
	Note   string // what the run is doing, or why the last one stopped
	Since  string // how long ago the run started or finished
	Links  []link // pull requests and the environment's URLs
}

type link struct{ Name, URL string }

type view struct {
	Title          string
	Refresh        int
	Message, Error string
	Open, Building []card
	Review         []card
	ListError      string
}

func (b *Board) page(w http.ResponseWriter, r *http.Request) {
	v := view{
		Title:   b.opts.Title,
		Refresh: int(b.opts.Refresh / time.Second),
		Message: r.URL.Query().Get("message"),
		Error:   r.URL.Query().Get("error"),
	}
	tickets, err := b.opts.Tickets.List(r.Context())
	if err != nil {
		v.ListError = err.Error()
	}
	for _, tk := range tickets {
		c := card{Ticket: tk}
		latest, err := b.d.Latest(r.Context(), tk.Ref)
		if err != nil {
			c.Note = "can't read its runs: " + err.Error()
			v.Open = append(v.Open, c)
			continue
		}
		c.Run = latest
		switch {
		case latest == nil:
			v.Open = append(v.Open, c)
		case latest.Status == run.StatusFailed || latest.Status == run.StatusCancelled:
			c.Note = "last run " + string(latest.Status)
			if latest.Error != "" {
				c.Note += ": " + latest.Error
			}
			c.Since = b.ago(latest.UpdatedAt)
			v.Open = append(v.Open, c)
		case latest.Status == run.StatusSucceeded:
			c.Since = b.ago(latest.UpdatedAt)
			c.Links = links(latest)
			v.Review = append(v.Review, c)
		default:
			c.Note = doing(latest)
			c.Since = b.ago(latest.StartedAt)
			c.Links = links(latest)
			v.Building = append(v.Building, c)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := pageTemplate.Execute(w, v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// implement starts a run for one of the open tickets.
func (b *Board) implement(w http.ResponseWriter, r *http.Request) {
	id := r.FormValue("ticket")
	tickets, err := b.opts.Tickets.List(r.Context())
	if err != nil {
		b.back(w, r, "error", err.Error())
		return
	}
	i := slices.IndexFunc(tickets, func(tk tracker.Ticket) bool { return tk.Ref.ID == id })
	if i < 0 {
		b.back(w, r, "error", fmt.Sprintf("ticket %q isn't open", id))
		return
	}
	tk := tickets[i]
	rid, err := b.d.Submit(r.Context(), run.Request{Ticket: tk.Ref, RequestedBy: "board"})
	if err != nil {
		b.back(w, r, "error", fmt.Sprintf("%s: %v", tk.Key, err))
		return
	}
	b.back(w, r, "message", fmt.Sprintf("Started run %s for %s.", rid, tk.Key))
}

// back sends the browser back to the board, with a message to show. The
// location stays relative: http.Redirect would resolve it against the path
// the daemon stripped its prefix from, and send the browser to the root.
func (b *Board) back(w http.ResponseWriter, _ *http.Request, kind, text string) {
	w.Header().Set("Location", "./?"+url.Values{kind: {text}}.Encode())
	w.WriteHeader(http.StatusSeeOther)
}

// doing says what an unfinished run is doing or waiting for.
func doing(s *run.Snapshot) string {
	switch s.Status {
	case run.StatusQueued:
		return "queued"
	case run.StatusNeedsInput:
		return "needs input: answer on the ticket"
	case run.StatusAwaitingApproval:
		return "awaiting approval before " + s.CurrentStep
	case run.StatusWaiting:
		if s.WaitingFor != "" {
			return "waiting for " + s.WaitingFor
		}
		return "waiting in " + s.CurrentStep
	}
	if s.CurrentStep != "" {
		return strings.ReplaceAll(s.CurrentStep, "_", " ")
	}
	return string(s.Status)
}

// links are the run's change requests and its environment's URLs, by name.
func links(s *run.Snapshot) []link {
	var out []link
	for repo, cr := range s.ChangeRequests {
		if cr != nil && cr.URL != "" {
			name := "pull request"
			if len(s.ChangeRequests) > 1 {
				name += " (" + repo + ")"
			}
			out = append(out, link{name, cr.URL})
		}
	}
	if s.Environment != nil && s.EnvironmentDestroyed.IsZero() {
		for name, u := range s.Environment.Endpoints {
			out = append(out, link{name, u})
		}
	}
	slices.SortFunc(out, func(a, b link) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// ago says how long ago t was, roughly.
func (b *Board) ago(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := b.now().Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d/time.Minute))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d/time.Hour))
	}
	return fmt.Sprintf("%d days ago", int(d/(24*time.Hour)))
}

//go:embed board.html
var pageHTML string

var pageTemplate = template.Must(template.New("board").Parse(pageHTML))
