// Package github is a forge.Forge for GitHub (github.com or GitHub
// Enterprise Server). Hinziel and the agent push over https with the same
// token that opens pull requests.
//
// # Credentials
//
//   - GITHUB_TOKEN, allowed to push to the repository and open pull
//     requests on it: a fine-grained personal access token limited to the
//     repository, with Contents and Pull requests set to read and write
//     (avatar → Settings → Developer settings → Personal access tokens), or
//     a classic token with the repo scope. Where the GitHub CLI is logged in,
//     `export GITHUB_TOKEN=$(gh auth token)` uses its token.
//   - The run branches (hinziel/… by default) must not be protected, or
//     pushes are refused.
//
// Draft pull requests need a public repository or a paid plan. Where GitHub
// refuses them, the pull request is opened ready for review with "Draft: "
// in its title.
package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/internal/apiclient"
)

// Options configure a GitHub forge.
type Options struct {
	Repo  string // owner and name, e.g. "acme/app"
	Token string
	// Labels are added to every pull request, besides those the engine asks for.
	Labels []string
	// BaseURL of the API; "" = https://api.github.com. For GitHub Enterprise
	// Server it's https://<host>/api/v3.
	BaseURL string
}

// Forge is the GitHub forge.
type Forge struct {
	opts Options
	api  *apiclient.Client

	mu   sync.Mutex
	repo *repository
}

var (
	_ forge.Forge                = (*Forge)(nil)
	_ forge.ChangeRequestUpdater = (*Forge)(nil)
	_ core.Checker               = (*Forge)(nil)
)

// New returns a GitHub forge.
func New(opts Options) *Forge {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.github.com"
	}
	return &Forge{opts: opts, api: &apiclient.Client{
		Name:    "github",
		BaseURL: base,
		Auth: func(r *http.Request) {
			r.Header.Set("Authorization", "Bearer "+opts.Token)
			r.Header.Set("Accept", "application/vnd.github+json")
			r.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		},
	}}
}

type repository struct {
	CloneURL      string `json:"clone_url"`
	DefaultBranch string `json:"default_branch"`
	Permissions   struct {
		Push bool `json:"push"`
	} `json:"permissions"`
}

type pullRequest struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
}

func (p *pullRequest) changeRequest() *forge.ChangeRequest {
	return &forge.ChangeRequest{ID: strconv.Itoa(p.Number), URL: p.HTMLURL, SourceBranch: p.Head.Ref, TargetBranch: p.Base.Ref}
}

func (f *Forge) repoPath() string { return "/repos/" + f.opts.Repo }

func (f *Forge) getRepo(ctx context.Context) (*repository, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.repo != nil {
		return f.repo, nil
	}
	var r repository
	if err := f.api.Do(ctx, http.MethodGet, f.repoPath(), nil, nil, &r); err != nil {
		return nil, fmt.Errorf("repository %s: %w", f.opts.Repo, err)
	}
	f.repo = &r
	return &r, nil
}

func (f *Forge) Remote(ctx context.Context) (*forge.GitRemote, error) {
	r, err := f.getRepo(ctx)
	if err != nil {
		return nil, err
	}
	return &forge.GitRemote{URL: r.CloneURL, Username: "x-access-token", Password: f.opts.Token, DefaultBranch: r.DefaultBranch}, nil
}

// OpenChangeRequest opens a pull request, then adds its labels and
// reviewers. If one of those fails, the pull request is already open and a
// retry returns it as it is.
func (f *Forge) OpenChangeRequest(ctx context.Context, in forge.ChangeRequestInput) (*forge.ChangeRequest, error) {
	if pr, err := f.openFor(ctx, in.SourceBranch); err != nil || pr != nil {
		return pr, err
	}
	body := map[string]any{
		"head":  in.SourceBranch,
		"base":  in.TargetBranch,
		"title": in.Title,
		"body":  in.Description.Markdown(),
		"draft": in.Draft,
	}
	var pr pullRequest
	err := f.api.Do(ctx, http.MethodPost, f.repoPath()+"/pulls", nil, body, &pr)
	if in.Draft && unsupportedDraft(err) {
		body["draft"], body["title"] = false, "Draft: "+in.Title
		err = f.api.Do(ctx, http.MethodPost, f.repoPath()+"/pulls", nil, body, &pr)
	}
	if apiclient.IsStatus(err, http.StatusUnprocessableEntity) {
		// Opened concurrently, or by an earlier attempt whose response was lost.
		if existing, lookupErr := f.openFor(ctx, in.SourceBranch); lookupErr == nil && existing != nil {
			return existing, nil
		}
	}
	if err != nil {
		return nil, err
	}
	issue := f.repoPath() + "/issues/" + strconv.Itoa(pr.Number)
	if labels := append(append([]string{}, f.opts.Labels...), in.Labels...); len(labels) > 0 {
		if err := f.api.Do(ctx, http.MethodPost, issue+"/labels", nil, map[string]any{"labels": labels}, nil); err != nil {
			return nil, fmt.Errorf("labels for pull request %d: %w", pr.Number, err)
		}
	}
	if len(in.Reviewers) > 0 {
		path := f.repoPath() + "/pulls/" + strconv.Itoa(pr.Number) + "/requested_reviewers"
		if err := f.api.Do(ctx, http.MethodPost, path, nil, map[string]any{"reviewers": in.Reviewers}, nil); err != nil {
			return nil, fmt.Errorf("reviewers %s for pull request %d: %w", strings.Join(in.Reviewers, ", "), pr.Number, err)
		}
	}
	return pr.changeRequest(), nil
}

// unsupportedDraft reports whether GitHub refused a draft pull request
// because the repository's plan doesn't have them.
func unsupportedDraft(err error) bool {
	var se *apiclient.StatusError
	return errors.As(err, &se) && se.Status == http.StatusUnprocessableEntity &&
		strings.Contains(strings.ToLower(se.Body), "draft pull requests are not supported")
}

// openFor returns the open pull request from branch, or nil.
func (f *Forge) openFor(ctx context.Context, branch string) (*forge.ChangeRequest, error) {
	owner, _, _ := strings.Cut(f.opts.Repo, "/")
	var prs []pullRequest
	q := url.Values{"head": {owner + ":" + branch}, "state": {"open"}}
	if err := f.api.Do(ctx, http.MethodGet, f.repoPath()+"/pulls", q, nil, &prs); err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0].changeRequest(), nil
}

func (f *Forge) CommentChangeRequest(ctx context.Context, cr *forge.ChangeRequest, msg core.Message) error {
	path := f.repoPath() + "/issues/" + url.PathEscape(cr.ID) + "/comments"
	return f.api.Do(ctx, http.MethodPost, path, nil, map[string]string{"body": msg.Markdown()}, nil)
}

// UpdateChangeRequest rewrites the description. The title stays as it was
// opened, "Draft: " included.
func (f *Forge) UpdateChangeRequest(ctx context.Context, cr *forge.ChangeRequest, in forge.ChangeRequestInput) error {
	path := f.repoPath() + "/pulls/" + url.PathEscape(cr.ID)
	return f.api.Do(ctx, http.MethodPatch, path, nil, map[string]string{"body": in.Description.Markdown()}, nil)
}

// Check verifies that the token can see the repository and push to it.
func (f *Forge) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{"Repo": f.opts.Repo, "Token": f.opts.Token}); err != nil {
		return err
	}
	r, err := f.getRepo(ctx)
	if err != nil {
		return err
	}
	if !r.Permissions.Push {
		return fmt.Errorf("repository %s: the token can read it but not push to it", f.opts.Repo)
	}
	return nil
}
