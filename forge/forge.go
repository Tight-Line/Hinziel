// Package forge defines the code host: clone/push credentials and
// merge/pull requests (GitLab, Forgejo, …). Implementations live in subpackages.
//
// Branch creation and pushing are done with the git CLI by the engine, not
// through the forge API, so only change-request operations are per-forge.
package forge

import (
	"context"

	"github.com/Tight-Line/hinziel/core"
)

// Forge hosts the code: clone/push credentials and merge/pull requests.
type Forge interface {
	Remote(ctx context.Context) (*GitRemote, error)
	// OpenChangeRequest returns the existing open CR for SourceBranch if one exists.
	OpenChangeRequest(ctx context.Context, in ChangeRequestInput) (*ChangeRequest, error)
	CommentChangeRequest(ctx context.Context, cr *ChangeRequest, msg core.Message) error
}

// ChangeRequestUpdater is optional: lets publish rewrite the description
// instead of adding a comment.
type ChangeRequestUpdater interface {
	UpdateChangeRequest(ctx context.Context, cr *ChangeRequest, in ChangeRequestInput) error
}

// GitRemote is everything the engine needs to clone and push.
type GitRemote struct {
	URL           string `json:"url"` // https or ssh
	Username      string `json:"-"`   // for https token auth
	Password      string `json:"-"`   // token; never persisted
	SSHKeyPath    string `json:"-"`
	DefaultBranch string `json:"default_branch"`
}

type ChangeRequestInput struct {
	SourceBranch string
	TargetBranch string
	Title        string
	Description  core.Message
	Draft        bool
	Labels       []string
	Reviewers    []string // forge usernames
}

type ChangeRequest struct {
	ID           string `json:"id"` // forge-native id (GitLab iid, Forgejo index), as string
	URL          string `json:"url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
}
