// Package trellocomments is a trigger.Trigger that starts a run when someone
// asks for one in a comment on a Trello card: the comment @mentions the
// factory's own Trello member and contains the word "implement", e.g.
// "@hinziel implement". With the word "teardown" instead, it destroys the
// card's environment. The member is whoever the token belongs to, so each
// installation's agent answers to its own name without configuration.
//
// It polls the board's comments, so it needs no public URL, no webhook and
// no Power-Up: it runs anywhere the daemon does, a laptop included. Only
// comments made while it runs count: those from before it started, or from
// while it was stopped, are not picked up.
//
// It replies on the card with the run it started, or why it couldn't start
// one (the card already has an unfinished run, for example). A teardown
// replies only when it fails: success is announced by the factory's
// notifiers (environment.destroyed), as is a teardown for other reasons.
//
// # Credentials
//
// The same TRELLO_API_KEY and TRELLO_TOKEN as tracker/trello: comments must
// mention the member the token belongs to, and the replies appear under its
// name. Board is the board's ID or the shortLink from its URL.
package trellocomments

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/apiclient"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/trigger"
)

type Options struct {
	APIKey string
	Token  string
	Board  string // ID or shortLink
	// ImplementWord is what a comment must contain, besides the mention, to
	// start a run; matched as a whole word, in any case. "" = "implement".
	ImplementWord string
	// TeardownWord asks for the card's environment to be destroyed instead;
	// "" = "teardown".
	TeardownWord string
	// Interval between polls; 0 = 30 seconds.
	Interval time.Duration
	// BaseURL of the API; "" = https://api.trello.com.
	BaseURL string
}

// Trigger starts runs from comments on one board.
type Trigger struct {
	opts      Options
	api       *apiclient.Client
	implement *regexp.Regexp
	teardown  *regexp.Regexp
}

var (
	_ trigger.Trigger = (*Trigger)(nil)
	_ core.Checker    = (*Trigger)(nil)
)

// pageSize is the most comments one request asks for.
const pageSize = 50

// New returns a comment trigger.
func New(opts Options) *Trigger {
	if opts.ImplementWord == "" {
		opts.ImplementWord = "implement"
	}
	if opts.TeardownWord == "" {
		opts.TeardownWord = "teardown"
	}
	if opts.Interval == 0 {
		opts.Interval = 30 * time.Second
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.trello.com"
	}
	t := &Trigger{opts: opts, implement: wordRE(opts.ImplementWord), teardown: wordRE(opts.TeardownWord)}
	t.api = &apiclient.Client{Name: "trello", BaseURL: base + "/1", Auth: func(r *http.Request) {
		r.Header.Set("Authorization", fmt.Sprintf(`OAuth oauth_consumer_key="%s", oauth_token="%s"`, opts.APIKey, opts.Token))
	}}
	return t
}

// wordRE matches w as a whole word, in any case. Trello usernames are
// letters, digits and underscores, so \b ends a mention correctly too.
func wordRE(w string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|\W)` + regexp.QuoteMeta(w) + `\b`)
}

type member struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type action struct {
	ID              string `json:"id"`
	IDMemberCreator string `json:"idMemberCreator"`
	MemberCreator   member `json:"memberCreator"`
	Data            struct {
		Text string `json:"text"`
		Card struct {
			ID        string `json:"id"`
			ShortLink string `json:"shortLink"`
		} `json:"card"`
	} `json:"data"`
}

// Check verifies the credentials and that the board's comments can be read.
func (t *Trigger) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{"APIKey": t.opts.APIKey, "Token": t.opts.Token, "Board": t.opts.Board}); err != nil {
		return err
	}
	if _, err := t.me(ctx); err != nil {
		return err
	}
	_, err := t.comments(ctx, "", "", 1)
	return err
}

// Start looks up the member comments must mention and the newest comment
// so far, then polls in the background until ctx is done.
func (t *Trigger) Start(ctx context.Context, d trigger.Dispatcher, _ *http.ServeMux) error {
	me, err := t.me(ctx)
	if err != nil {
		return err
	}
	// Comments from before the start don't count; with none at all, the
	// start time is the cursor (Trello's `since` takes either).
	cursor := time.Now().UTC().Format(time.RFC3339)
	latest, err := t.comments(ctx, "", "", 1)
	if err != nil {
		return err
	}
	if len(latest) > 0 {
		cursor = latest[0].ID
	}
	mention := wordRE("@" + me.Username)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.opts.Interval):
			}
			next, err := t.poll(ctx, d, me, mention, cursor)
			if err != nil && ctx.Err() == nil {
				log.Printf("trellocomments: board %s: %v (trying again in %s)", t.opts.Board, err, t.opts.Interval)
			}
			cursor = next
		}
	}()
	return nil
}

// poll handles every comment after cursor, oldest first, and returns the
// new cursor: the last comment handled, even when a later one failed.
func (t *Trigger) poll(ctx context.Context, d trigger.Dispatcher, me *member, mention *regexp.Regexp, cursor string) (string, error) {
	// Trello returns the newest comments after `since`, newest first: page
	// back with `before` until a page isn't full.
	var all []action
	before := ""
	for {
		page, err := t.comments(ctx, cursor, before, pageSize)
		if err != nil {
			return cursor, err
		}
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
		before = page[len(page)-1].ID
	}
	slices.Reverse(all)
	for _, a := range all {
		cursor = a.ID
		if a.IDMemberCreator == me.ID || !mention.MatchString(a.Data.Text) {
			continue
		}
		implement, teardown := t.implement.MatchString(a.Data.Text), t.teardown.MatchString(a.Data.Text)
		var err error
		switch {
		case implement && teardown:
			err = t.reply(ctx, a, fmt.Sprintf("@%s, should I %s or %s? Ask for one at a time.",
				a.MemberCreator.Username, t.opts.ImplementWord, t.opts.TeardownWord))
		case implement:
			err = t.start(ctx, d, a)
		case teardown:
			err = t.tearDown(ctx, d, a)
		}
		if err != nil {
			return cursor, err
		}
	}
	return cursor, nil
}

// start submits a run for the comment's card and replies on the card.
func (t *Trigger) start(ctx context.Context, d trigger.Dispatcher, a action) error {
	id, err := d.Submit(ctx, run.Request{
		Ticket:      tracker.TicketRef{ID: a.Data.Card.ShortLink},
		RequestedBy: "trello:" + a.MemberCreator.Username,
	})
	reply := fmt.Sprintf("On it, @%s: started run `%s`.", a.MemberCreator.Username, id)
	if err != nil {
		reply = fmt.Sprintf("@%s, I couldn't start a run: %v", a.MemberCreator.Username, err)
	}
	return t.reply(ctx, a, reply)
}

// tearDown destroys the comment's card's environment, and replies only if
// that fails.
func (t *Trigger) tearDown(ctx context.Context, d trigger.Dispatcher, a action) error {
	_, err := d.Teardown(ctx, tracker.TicketRef{ID: a.Data.Card.ShortLink}, "trello:"+a.MemberCreator.Username)
	if err == nil {
		return nil
	}
	return t.reply(ctx, a, fmt.Sprintf("@%s, I couldn't tear down the environment: %v", a.MemberCreator.Username, err))
}

func (t *Trigger) reply(ctx context.Context, a action, text string) error {
	return t.api.Do(ctx, http.MethodPost, "/cards/"+url.PathEscape(a.Data.Card.ID)+"/actions/comments",
		nil, map[string]string{"text": text}, nil)
}

func (t *Trigger) me(ctx context.Context) (*member, error) {
	var m member
	if err := t.api.Do(ctx, http.MethodGet, "/members/me", url.Values{"fields": {"username"}}, nil, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// comments returns up to limit of the board's comments after since and
// before before (action IDs, or a date for since; "" = no bound), newest
// first.
func (t *Trigger) comments(ctx context.Context, since, before string, limit int) ([]action, error) {
	q := url.Values{
		"filter":               {"commentCard"},
		"limit":                {strconv.Itoa(limit)},
		"fields":               {"data,idMemberCreator"},
		"memberCreator_fields": {"username"},
	}
	if since != "" {
		q.Set("since", since)
	}
	if before != "" {
		q.Set("before", before)
	}
	var out []action
	err := t.api.Do(ctx, http.MethodGet, "/boards/"+url.PathEscape(t.opts.Board)+"/actions", q, nil, &out)
	return out, err
}
