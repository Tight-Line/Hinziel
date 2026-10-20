// Package board is a trigger that serves a web page of the factory's
// tickets, in three columns: the open tickets, each with an Implement button
// that starts a run; the tickets being worked on; and the tickets whose run
// finished and wait for review. A ticket's column comes from its latest run:
// none, failed or cancelled is open; queued, running, waiting, needing input
// or awaiting approval is being worked on; succeeded is in review. A failed
// run gets a Resume button instead, as the engine keeps it to be resumed
// and won't start another for its ticket. Closed tickets leave the board, as
// the tracker no longer lists them.
//
// With Options.Open (the factory's environment, when it implements
// env.Opener, as deploy/scratchorg does), cards whose environment exists get
// a Log in button: it opens the environment in a new tab, logged in. The link
// carries a session, so it's made when the button is clicked and never put
// in the page.
//
// The page is served by `daemon --listen` at /triggers/<factory>/<trigger>/
// and refreshes itself. With Options.BasicAuth, the browser asks for a user
// name and password; without it, anyone who can reach the address can start
// runs, so keep it on a private network then. Basic auth sends the password
// in every request, so serve the page over HTTPS (behind a proxy) anywhere
// but a private network or an SSH tunnel. Implement only accepts requests
// from the page's own origin (http.CrossOriginProtection), so another site
// can't start runs through a visitor's browser.
//
// # Credentials
//
//   - BasicAuth, optional: "user:password" in base64, as in an Authorization
//     header (`printf %s 'user:password' | base64`). In an env file for
//     `docker run --env-file`, write it without quotes: Docker keeps them.
//   - The tickets come from Options.Tickets, usually the factory's tracker
//     (it must implement tracker.Lister, as tracker/github does).
package board

import (
	"cmp"
	"context"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/env"
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
	// BasicAuth is "user:password" in base64; "" = no login.
	BasicAuth string
	// Open logs people in to a run's environment; nil = no Log in button.
	Open env.Opener
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
	if b.opts.BasicAuth != "" {
		raw, err := base64.StdEncoding.DecodeString(b.opts.BasicAuth)
		if err != nil || !strings.Contains(string(raw), ":") {
			return errors.New(`board: BasicAuth must be "user:password" in base64 (printf %s 'user:password' | base64), without quotes around it`)
		}
	}
	b.d = d
	mux.Handle("GET /{$}", b.login(http.HandlerFunc(b.page)))
	csrf := http.NewCrossOriginProtection()
	mux.Handle("POST /implement", b.login(csrf.Handler(http.HandlerFunc(b.implement))))
	mux.Handle("POST /resume", b.login(csrf.Handler(http.HandlerFunc(b.resume))))
	mux.Handle("POST /open", b.login(csrf.Handler(http.HandlerFunc(b.open))))
	return nil
}

// login asks for Options.BasicAuth's user name and password, if it's set.
func (b *Board) login(next http.Handler) http.Handler {
	if b.opts.BasicAuth == "" {
		return next
	}
	want := []byte("Basic " + b.opts.BasicAuth)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="`+b.opts.Title+`", charset="UTF-8"`)
			http.Error(w, "Log in to see the board.", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// card is one ticket on the board.
type card struct {
	Ticket tracker.Ticket
	Run    *run.Snapshot
	Failed bool   // the latest run failed, and can be resumed
	Open   bool   // the run's environment exists and can be opened logged in
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
		c.Open = b.opts.Open != nil && live(latest)
		switch {
		case latest == nil:
			v.Open = append(v.Open, c)
		case latest.Status == run.StatusFailed || latest.Status == run.StatusCancelled:
			c.Note = "last run " + string(latest.Status)
			if latest.Error != "" {
				c.Note += ": " + latest.Error
			}
			c.Since = b.ago(latest.UpdatedAt)
			c.Failed = latest.Status == run.StatusFailed
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
	b.act(w, r, "Started", func(ctx context.Context, tk tracker.Ticket) (run.ID, error) {
		return b.d.Submit(ctx, run.Request{Ticket: tk.Ref, RequestedBy: "board"})
	})
}

// resume continues an open ticket's failed run.
func (b *Board) resume(w http.ResponseWriter, r *http.Request) {
	b.act(w, r, "Resumed", func(ctx context.Context, tk tracker.Ticket) (run.ID, error) {
		return b.d.Resume(ctx, tk.Ref, "board")
	})
}

// act does something to the open ticket the form names, and goes back to the
// board saying how it went.
func (b *Board) act(w http.ResponseWriter, r *http.Request, done string, do func(context.Context, tracker.Ticket) (run.ID, error)) {
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
	rid, err := do(r.Context(), tk)
	if err != nil {
		b.back(w, r, "error", fmt.Sprintf("%s: %v", tk.Key, err))
		return
	}
	b.back(w, r, "message", fmt.Sprintf("%s run %s for %s.", done, rid, tk.Key))
}

// open sends the browser to a link that logs it in to the environment of an
// open ticket's latest run.
func (b *Board) open(w http.ResponseWriter, r *http.Request) {
	if b.opts.Open == nil {
		http.NotFound(w, r)
		return
	}
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
	latest, err := b.d.Latest(r.Context(), tk.Ref)
	if err != nil {
		b.back(w, r, "error", fmt.Sprintf("%s: %v", tk.Key, err))
		return
	}
	if !live(latest) {
		b.back(w, r, "error", tk.Key+" has no environment to log in to")
		return
	}
	u, err := b.opts.Open.OpenURL(r.Context(), latest.Environment)
	if err != nil {
		b.back(w, r, "error", fmt.Sprintf("%s: logging in to %s: %v", tk.Key, latest.Environment.Name, err))
		return
	}
	// The link carries a session: keep it out of caches and other sites' logs.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Location", u)
	w.WriteHeader(http.StatusSeeOther)
}

// live reports whether the run has an environment that hasn't been torn down.
func live(s *run.Snapshot) bool {
	return s != nil && s.Environment != nil && s.EnvironmentDestroyed.IsZero()
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
