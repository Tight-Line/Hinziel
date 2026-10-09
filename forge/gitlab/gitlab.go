// Package gitlab is a forge.Forge for GitLab (gitlab.com or self-managed).
//
// The token needs the api scope (to open merge requests) and write access to
// the repository (Heinzel and the agent push over https with it). A personal,
// project or group access token with the Developer role works.
package gitlab

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/internal/apiclient"
)

// Options configure a GitLab forge.
type Options struct {
	Project string // path with namespace, e.g. "acme/app"
	Token   string
	// Labels are added to every merge request, besides those the engine asks for.
	Labels []string
	// BaseURL of the GitLab instance; "" = https://gitlab.com.
	BaseURL string
}

// Forge is the GitLab forge.
type Forge struct {
	opts Options
	api  *apiclient.Client

	mu      sync.Mutex
	project *project
}

var (
	_ forge.Forge                = (*Forge)(nil)
	_ forge.ChangeRequestUpdater = (*Forge)(nil)
	_ core.Checker               = (*Forge)(nil)
)

// New returns a GitLab forge.
func New(opts Options) *Forge {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://gitlab.com"
	}
	return &Forge{opts: opts, api: &apiclient.Client{
		Name:    "gitlab",
		BaseURL: base + "/api/v4",
		Auth:    func(r *http.Request) { r.Header.Set("PRIVATE-TOKEN", opts.Token) },
	}}
}

type project struct {
	ID            int    `json:"id"`
	HTTPURL       string `json:"http_url_to_repo"`
	DefaultBranch string `json:"default_branch"`
}

type mergeRequest struct {
	IID          int    `json:"iid"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}

func (m *mergeRequest) changeRequest() *forge.ChangeRequest {
	return &forge.ChangeRequest{ID: strconv.Itoa(m.IID), URL: m.WebURL, SourceBranch: m.SourceBranch, TargetBranch: m.TargetBranch}
}

func (f *Forge) projectPath() string { return "/projects/" + url.PathEscape(f.opts.Project) }

func (f *Forge) getProject(ctx context.Context) (*project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.project != nil {
		return f.project, nil
	}
	var p project
	if err := f.api.Do(ctx, http.MethodGet, f.projectPath(), nil, nil, &p); err != nil {
		return nil, fmt.Errorf("project %s: %w", f.opts.Project, err)
	}
	f.project = &p
	return &p, nil
}

func (f *Forge) Remote(ctx context.Context) (*forge.GitRemote, error) {
	p, err := f.getProject(ctx)
	if err != nil {
		return nil, err
	}
	return &forge.GitRemote{URL: p.HTTPURL, Username: "oauth2", Password: f.opts.Token, DefaultBranch: p.DefaultBranch}, nil
}

func (f *Forge) OpenChangeRequest(ctx context.Context, in forge.ChangeRequestInput) (*forge.ChangeRequest, error) {
	if mr, err := f.openFor(ctx, in.SourceBranch); err != nil || mr != nil {
		return mr, err
	}
	body := map[string]any{
		"source_branch": in.SourceBranch,
		"target_branch": in.TargetBranch,
		"title":         title(in),
		"description":   in.Description.Markdown(),
	}
	if labels := append(append([]string{}, f.opts.Labels...), in.Labels...); len(labels) > 0 {
		body["labels"] = strings.Join(labels, ",")
	}
	if len(in.Reviewers) > 0 {
		ids, err := f.userIDs(ctx, in.Reviewers)
		if err != nil {
			return nil, err
		}
		body["reviewer_ids"] = ids
	}
	var mr mergeRequest
	err := f.api.Do(ctx, http.MethodPost, f.projectPath()+"/merge_requests", nil, body, &mr)
	if apiclient.IsStatus(err, http.StatusConflict) {
		// Opened concurrently, or by an earlier attempt whose response was lost.
		if existing, lookupErr := f.openFor(ctx, in.SourceBranch); lookupErr == nil && existing != nil {
			return existing, nil
		}
	}
	if err != nil {
		return nil, err
	}
	return mr.changeRequest(), nil
}

// openFor returns the open merge request from branch, or nil.
func (f *Forge) openFor(ctx context.Context, branch string) (*forge.ChangeRequest, error) {
	var mrs []mergeRequest
	q := url.Values{"source_branch": {branch}, "state": {"opened"}}
	if err := f.api.Do(ctx, http.MethodGet, f.projectPath()+"/merge_requests", q, nil, &mrs); err != nil {
		return nil, err
	}
	if len(mrs) == 0 {
		return nil, nil
	}
	return mrs[0].changeRequest(), nil
}

func (f *Forge) userIDs(ctx context.Context, usernames []string) ([]int, error) {
	var ids []int
	for _, name := range usernames {
		var users []struct {
			ID int `json:"id"`
		}
		if err := f.api.Do(ctx, http.MethodGet, "/users", url.Values{"username": {name}}, nil, &users); err != nil {
			return nil, err
		}
		if len(users) == 0 {
			return nil, fmt.Errorf("reviewer %q: no such GitLab user", name)
		}
		ids = append(ids, users[0].ID)
	}
	return ids, nil
}

func title(in forge.ChangeRequestInput) string {
	if in.Draft {
		return "Draft: " + in.Title
	}
	return in.Title
}

func (f *Forge) CommentChangeRequest(ctx context.Context, cr *forge.ChangeRequest, msg core.Message) error {
	path := f.projectPath() + "/merge_requests/" + url.PathEscape(cr.ID) + "/notes"
	return f.api.Do(ctx, http.MethodPost, path, nil, map[string]string{"body": msg.Markdown()}, nil)
}

func (f *Forge) UpdateChangeRequest(ctx context.Context, cr *forge.ChangeRequest, in forge.ChangeRequestInput) error {
	path := f.projectPath() + "/merge_requests/" + url.PathEscape(cr.ID)
	body := map[string]string{"title": title(in), "description": in.Description.Markdown()}
	return f.api.Do(ctx, http.MethodPut, path, nil, body, nil)
}

// Check verifies that the token can see the project.
func (f *Forge) Check(ctx context.Context) error {
	_, err := f.getProject(ctx)
	return err
}
