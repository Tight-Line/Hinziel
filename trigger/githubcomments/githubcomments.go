// Package githubcomments is a trigger.Trigger that starts a run when someone
// asks for one in a comment on a GitHub issue: the comment @mentions the
// factory's GitHub user and contains the word "implement", e.g.
// "@hinziel implement". With the word "teardown" instead, it destroys the
// issue's environment. The user is whoever the token belongs to, unless
// Options.Mention names another, so each installation answers to its own
// name without configuration.
//
// It polls the repository's issue comments, so it needs no public URL and no
// webhook: it runs anywhere the daemon does, a laptop included. Only
// comments made while it runs count: those from before it started, or from
// while it was stopped, are not picked up. Comments on pull requests are
// ignored, as are comments ending in tracker/github's Signature, which
// Hinziel posts itself: those may come from the same token as a person's.
//
// It replies on the issue with the run it started, or why it couldn't start
// one (the issue already has an unfinished run, for example). A teardown
// replies only when it fails: success is announced by the factory's
// notifiers (environment.destroyed), as is a teardown for other reasons.
//
// # Credentials
//
// The same GITHUB_TOKEN as tracker/github: it reads the repository's issue
// comments and posts the replies, under the token's user.
package githubcomments

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/apiclient"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	ghtracker "github.com/Tight-Line/hinziel/tracker/github"
	"github.com/Tight-Line/hinziel/trigger"
)

type Options struct {
	Repo  string // owner and name, e.g. "acme/app"
	Token string
	// Mention is the GitHub user comments must @mention, without the "@";
	// "" = the token's user.
	Mention string
	// ImplementWord is what a comment must contain, besides the mention, to
	// start a run; matched as a whole word, in any case. "" = "implement".
	ImplementWord string
	// TeardownWord asks for the issue's environment to be destroyed
	// instead; "" = "teardown".
	TeardownWord string
	// Interval between polls; 0 = 30 seconds.
	Interval time.Duration
	// BaseURL of the API; "" = https://api.github.com. For GitHub Enterprise
	// Server it's https://<host>/api/v3.
	BaseURL string
}

// Trigger starts runs from comments on one repository's issues.
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
const pageSize = 100

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
		base = "https://api.github.com"
	}
	return &Trigger{
		opts:      opts,
		implement: wordRE(opts.ImplementWord),
		teardown:  wordRE(opts.TeardownWord),
		api: &apiclient.Client{Name: "github", BaseURL: base, Auth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+opts.Token)
			r.Header.Set("Accept", "application/vnd.github+json")
			r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		}},
	}
}

// wordRE matches w as a whole word, in any case. GitHub logins have dashes,
// so a dash continues a word: "@hinziel" doesn't match "@hinziel-bot".
func wordRE(w string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)(^|[^\w-])` + regexp.QuoteMeta(w) + `($|[^\w-])`)
}

type comment struct {
	ID        int64     `json:"id"`
	Body      string    `json:"body"`
	HTMLURL   string    `json:"html_url"`  // …/issues/<n>#issuecomment-…, or …/pull/<n>#… on a pull request
	IssueURL  string    `json:"issue_url"` // the API's …/issues/<n>
	CreatedAt time.Time `json:"created_at"`
	User      struct {
		Login string `json:"login"`
	} `json:"user"`
}

// issue returns the number of the issue the comment is on, or "" for a
// comment on a pull request.
func (c *comment) issue() string {
	if strings.Contains(c.HTMLURL, "/pull/") {
		return ""
	}
	return c.IssueURL[strings.LastIndex(c.IssueURL, "/")+1:]
}

func (t *Trigger) repoPath() string { return "/repos/" + t.opts.Repo }

// Check verifies the token and that the repository's comments can be read.
func (t *Trigger) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{"Repo": t.opts.Repo, "Token": t.opts.Token}); err != nil {
		return err
	}
	if _, err := t.mention(ctx); err != nil {
		return err
	}
	_, err := t.newest(ctx)
	return err
}

// Start looks up the user comments must mention and the newest comment so
// far, then polls in the background until ctx is done.
func (t *Trigger) Start(ctx context.Context, d trigger.Dispatcher, _ *http.ServeMux) error {
	who, err := t.mention(ctx)
	if err != nil {
		return err
	}
	// Comments up to the newest one now don't count. `since` filters on
	// when a comment was last updated, so it only narrows the listing; the
	// comment ID decides what's new, and edits don't count again.
	last, err := t.newest(ctx)
	if err != nil {
		return err
	}
	since := time.Now().Add(-time.Minute)
	if last != nil && last.CreatedAt.Before(since) {
		since = last.CreatedAt
	}
	var lastID int64
	if last != nil {
		lastID = last.ID
	}
	mention := wordRE("@" + who)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(t.opts.Interval):
			}
			var err error
			since, lastID, err = t.poll(ctx, d, mention, since, lastID)
			if err != nil && ctx.Err() == nil {
				log.Printf("githubcomments: %s: %v (trying again in %s)", t.opts.Repo, err, t.opts.Interval)
			}
		}
	}()
	return nil
}

// poll handles every comment newer than lastID, oldest first, and returns
// the new cursor: the last comment handled, even when a later one failed.
func (t *Trigger) poll(ctx context.Context, d trigger.Dispatcher, mention *regexp.Regexp, since time.Time, lastID int64) (time.Time, int64, error) {
	var all []comment
	for page := 1; ; page++ {
		var cs []comment
		q := url.Values{
			"sort": {"created"}, "direction": {"asc"}, "since": {since.UTC().Format(time.RFC3339)},
			"per_page": {strconv.Itoa(pageSize)}, "page": {strconv.Itoa(page)},
		}
		if err := t.api.Do(ctx, http.MethodGet, t.repoPath()+"/issues/comments", q, nil, &cs); err != nil {
			return since, lastID, err
		}
		all = append(all, cs...)
		if len(cs) < pageSize {
			break
		}
	}
	for _, c := range all {
		if c.ID <= lastID {
			continue
		}
		since, lastID = c.CreatedAt, c.ID
		issue := c.issue()
		if issue == "" || strings.HasSuffix(strings.TrimSpace(c.Body), ghtracker.Signature) || !mention.MatchString(c.Body) {
			continue
		}
		implement, teardown := t.implement.MatchString(c.Body), t.teardown.MatchString(c.Body)
		var err error
		switch {
		case implement && teardown:
			err = t.reply(ctx, issue, fmt.Sprintf("@%s, should I %s or %s? Ask for one at a time.",
				c.User.Login, t.opts.ImplementWord, t.opts.TeardownWord))
		case implement:
			err = t.start(ctx, d, issue, c.User.Login)
		case teardown:
			err = t.tearDown(ctx, d, issue, c.User.Login)
		}
		if err != nil {
			return since, lastID, err
		}
	}
	return since, lastID, nil
}

// start submits a run for the issue and replies on it.
func (t *Trigger) start(ctx context.Context, d trigger.Dispatcher, issue, by string) error {
	id, err := d.Submit(ctx, run.Request{Ticket: tracker.TicketRef{ID: issue}, RequestedBy: "github:" + by})
	reply := fmt.Sprintf("On it, @%s: started run `%s`.", by, id)
	if err != nil {
		reply = fmt.Sprintf("@%s, I couldn't start a run: %v", by, err)
	}
	return t.reply(ctx, issue, reply)
}

// tearDown destroys the issue's environment, and replies only if that fails.
func (t *Trigger) tearDown(ctx context.Context, d trigger.Dispatcher, issue, by string) error {
	_, err := d.Teardown(ctx, tracker.TicketRef{ID: issue}, "github:"+by)
	if err == nil {
		return nil
	}
	return t.reply(ctx, issue, fmt.Sprintf("@%s, I couldn't tear down the environment: %v", by, err))
}

func (t *Trigger) reply(ctx context.Context, issue, text string) error {
	return t.api.Do(ctx, http.MethodPost, t.repoPath()+"/issues/"+issue+"/comments",
		nil, map[string]string{"body": text + "\n\n" + ghtracker.Signature}, nil)
}

// mention returns Options.Mention, or the token's user.
func (t *Trigger) mention(ctx context.Context) (string, error) {
	var me struct {
		Login string `json:"login"`
	}
	if err := t.api.Do(ctx, http.MethodGet, "/user", nil, nil, &me); err != nil {
		return "", err
	}
	if t.opts.Mention != "" {
		return strings.TrimPrefix(t.opts.Mention, "@"), nil
	}
	return me.Login, nil
}

// newest returns the repository's newest issue comment, or nil.
func (t *Trigger) newest(ctx context.Context) (*comment, error) {
	var cs []comment
	q := url.Values{"sort": {"created"}, "direction": {"desc"}, "per_page": {"1"}}
	if err := t.api.Do(ctx, http.MethodGet, t.repoPath()+"/issues/comments", q, nil, &cs); err != nil {
		return nil, fmt.Errorf("comments of %s: %w", t.opts.Repo, err)
	}
	if len(cs) == 0 {
		return nil, nil
	}
	return &cs[0], nil
}
