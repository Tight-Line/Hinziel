// Package github is a tracker.Tracker for GitHub issues (github.com or
// GitHub Enterprise Server).
//
// A ticket is an issue in one repository. Fetch accepts the issue's number,
// "#<number>" or the issue's URL. The ticket's Key is "#<number>", which
// GitHub links wherever Hinziel writes it, such as pull request titles;
// branch and environment names get the number. Comments and labels come
// with the ticket. Pull requests share the issues' numbers but are refused:
// they aren't tickets.
//
// It implements StageMover (puts the label named for the stage on the issue,
// and takes the other stages' labels off) and DoneChecker (the issue is
// closed or deleted, or has StageDone's label). There is no Linker: the pull
// request's description links the issue, and GitHub shows that on the issue.
//
// # Credentials
//
//   - GITHUB_TOKEN, allowed to read issues and comment on them, and to label
//     them if Stages are set: a fine-grained personal access token limited
//     to the repository, with Issues set to read and write, or a classic
//     token with the repo scope. With forge/github on the same repository,
//     one token can do both (add Contents and Pull requests). Where the
//     GitHub CLI is logged in, `export GITHUB_TOKEN=$(gh auth token)` uses
//     its token. Comments appear under the token's user.
//   - The labels Stages name must exist in the repository; nothing is
//     created. `check` lists the repository's labels when one doesn't match.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/apiclient"
	"github.com/Tight-Line/hinziel/tracker"
)

// Options configure a GitHub tracker.
type Options struct {
	Repo  string // owner and name, e.g. "acme/app"
	Token string
	// Stages names the label each lifecycle stage puts on an issue.
	// Unmapped stages are not reflected.
	Stages map[tracker.Stage]string
	// BaseURL of the API; "" = https://api.github.com. For GitHub Enterprise
	// Server it's https://<host>/api/v3.
	BaseURL string
}

// Tracker is the GitHub tracker.
type Tracker struct {
	opts Options
	api  *apiclient.Client
}

var (
	_ tracker.Tracker     = (*Tracker)(nil)
	_ tracker.StageMover  = (*Tracker)(nil)
	_ tracker.DoneChecker = (*Tracker)(nil)
	_ core.Checker        = (*Tracker)(nil)
)

// New returns a GitHub tracker.
func New(opts Options) *Tracker {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Tracker{opts: opts, api: &apiclient.Client{
		Name:    "github",
		BaseURL: base,
		Auth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+opts.Token)
			r.Header.Set("Accept", "application/vnd.github+json")
			r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		},
	}}
}

type issue struct {
	Number      int              `json:"number"`
	Title       string           `json:"title"`
	Body        string           `json:"body"`
	HTMLURL     string           `json:"html_url"`
	State       string           `json:"state"` // "open" or "closed"
	Labels      []label          `json:"labels"`
	PullRequest *json.RawMessage `json:"pull_request"` // set on pull requests only
}

type label struct {
	Name string `json:"name"`
}

type comment struct {
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

func (t *Tracker) repoPath() string { return "/repos/" + t.opts.Repo }

// Signature ends every comment Hinziel posts on an issue, so that
// trigger/githubcomments can tell them apart from people's, even when
// Hinziel posts with a person's token.
const Signature = "<!-- hinziel -->"

// issueURLRE matches https://<host>/<owner>/<repo>/issues/<number>.
var issueURLRE = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/issues/([0-9]+)`)

// number accepts "12", "#12" or an issue URL in the repository.
func (t *Tracker) number(id string) (string, error) {
	id = strings.TrimSpace(id)
	if m := issueURLRE.FindStringSubmatch(id); m != nil {
		if !strings.EqualFold(m[1], t.opts.Repo) {
			return "", fmt.Errorf("issue %s is not in %s", id, t.opts.Repo)
		}
		return m[2], nil
	}
	n := strings.TrimPrefix(id, "#")
	if v, err := strconv.Atoi(n); err != nil || v < 1 {
		return "", fmt.Errorf("issue %q: give its number, #<number> or its URL", id)
	}
	return n, nil
}

func (t *Tracker) getIssue(ctx context.Context, id string) (string, *issue, error) {
	n, err := t.number(id)
	if err != nil {
		return "", nil, err
	}
	var is issue
	if err := t.api.Do(ctx, http.MethodGet, t.repoPath()+"/issues/"+n, nil, nil, &is); err != nil {
		return n, nil, fmt.Errorf("issue #%s: %w", n, err)
	}
	if is.PullRequest != nil {
		return n, nil, fmt.Errorf("#%s is a pull request, not an issue", n)
	}
	return n, &is, nil
}

func (t *Tracker) Fetch(ctx context.Context, id string) (*tracker.Ticket, error) {
	n, is, err := t.getIssue(ctx, id)
	if err != nil {
		return nil, err
	}
	tk := &tracker.Ticket{
		Ref:         tracker.TicketRef{ID: n},
		Key:         "#" + n,
		Title:       is.Title,
		Description: is.Body,
		URL:         is.HTMLURL,
	}
	for _, l := range is.Labels {
		tk.Labels = append(tk.Labels, l.Name)
	}
	// Comments come oldest first, a page at a time.
	for page := 1; ; page++ {
		var cs []comment
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := t.api.Do(ctx, http.MethodGet, t.repoPath()+"/issues/"+n+"/comments", q, nil, &cs); err != nil {
			return nil, fmt.Errorf("comments on #%s: %w", n, err)
		}
		for _, c := range cs {
			tk.Comments = append(tk.Comments, tracker.Comment{Author: c.User.Login, Body: c.Body, At: c.CreatedAt})
		}
		if len(cs) < 100 {
			return tk, nil
		}
	}
}

// maxComment is GitHub's limit on the length of a comment, in characters.
const maxComment = 65536

func (t *Tracker) Comment(ctx context.Context, id string, msg core.Message) error {
	n, err := t.number(id)
	if err != nil {
		return err
	}
	text := msg.Markdown()
	if room := maxComment - utf8.RuneCountInString("\n\n"+Signature); utf8.RuneCountInString(text) > room {
		const cut = "\n\n…(truncated)"
		text = string([]rune(text)[:room-utf8.RuneCountInString(cut)]) + cut
	}
	text += "\n\n" + Signature
	return t.api.Do(ctx, http.MethodPost, t.repoPath()+"/issues/"+n+"/comments", nil, map[string]string{"body": text}, nil)
}

// MoveTo puts the stage's label on the issue and takes off the labels of
// every other stage, so a "Blocked" label disappears once the run continues.
func (t *Tracker) MoveTo(ctx context.Context, id string, stage tracker.Stage) error {
	want, ok := t.opts.Stages[stage]
	if !ok || want == "" {
		return nil
	}
	n, is, err := t.getIssue(ctx, id)
	if err != nil {
		return err
	}
	labels := t.repoPath() + "/issues/" + n + "/labels"
	for _, l := range is.Labels {
		if l.Name != want && t.isStageLabel(l.Name) {
			err := t.api.Do(ctx, http.MethodDelete, labels+"/"+url.PathEscape(l.Name), nil, nil, nil)
			if err != nil && !apiclient.IsStatus(err, http.StatusNotFound) {
				return fmt.Errorf("label %q on #%s: %w", l.Name, n, err)
			}
		}
	}
	if slices.ContainsFunc(is.Labels, func(l label) bool { return l.Name == want }) {
		return nil
	}
	if err := t.api.Do(ctx, http.MethodPost, labels, nil, map[string]any{"labels": []string{want}}, nil); err != nil {
		return fmt.Errorf("label %q on #%s: %w", want, n, err)
	}
	return nil
}

func (t *Tracker) isStageLabel(name string) bool {
	for _, l := range t.opts.Stages {
		if l == name {
			return true
		}
	}
	return false
}

// Done reports whether the issue is done: closed, deleted, or with
// StageDone's label.
func (t *Tracker) Done(ctx context.Context, id string) (bool, error) {
	_, is, err := t.getIssue(ctx, id)
	if apiclient.IsStatus(err, http.StatusNotFound, http.StatusGone) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if is.State == "closed" {
		return true, nil
	}
	done := t.opts.Stages[tracker.StageDone]
	return done != "" && slices.ContainsFunc(is.Labels, func(l label) bool { return l.Name == done }), nil
}

// Check verifies that the token can see the repository's issues and that
// every stage's label exists.
func (t *Tracker) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{"Repo": t.opts.Repo, "Token": t.opts.Token}); err != nil {
		return err
	}
	var r struct {
		HasIssues bool `json:"has_issues"`
	}
	if err := t.api.Do(ctx, http.MethodGet, t.repoPath(), nil, nil, &r); err != nil {
		return fmt.Errorf("repository %s: %w", t.opts.Repo, err)
	}
	if !r.HasIssues {
		return fmt.Errorf("repository %s has issues turned off (Settings → General → Features)", t.opts.Repo)
	}
	if len(t.opts.Stages) == 0 {
		return nil
	}
	var all []string
	for page := 1; ; page++ {
		var ls []label
		q := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}}
		if err := t.api.Do(ctx, http.MethodGet, t.repoPath()+"/labels", q, nil, &ls); err != nil {
			return fmt.Errorf("labels of %s: %w", t.opts.Repo, err)
		}
		for _, l := range ls {
			all = append(all, l.Name)
		}
		if len(ls) < 100 {
			break
		}
	}
	var missing []string
	for stage, l := range t.opts.Stages {
		if l != "" && !slices.Contains(all, l) {
			missing = append(missing, fmt.Sprintf("%q (stage %s)", l, stage))
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		return fmt.Errorf("no label %s in %s (it has: %s)", strings.Join(missing, ", "), t.opts.Repo, strings.Join(all, ", "))
	}
	return nil
}
