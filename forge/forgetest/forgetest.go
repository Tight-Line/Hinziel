// Package forgetest provides a fake forge backed by a local bare repository,
// and a conformance suite for forge.Forge implementations.
package forgetest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/internal/gittest"
	"github.com/Tight-Line/hinziel/internal/testutil"
	"github.com/Tight-Line/hinziel/internal/workspace"
)

// Fake is a forge.Forge whose git remote is a local bare repository. Change
// requests live in memory. It refuses to open a change request for a branch
// that hasn't been pushed, like a real forge.
type Fake struct {
	Dir           string // the bare repository
	DefaultBranch string

	mu       sync.Mutex
	crs      map[string]*forge.ChangeRequest // by source branch
	inputs   map[string]forge.ChangeRequestInput
	comments map[string][]core.Message
	opens    int
}

var (
	_ forge.Forge                = (*Fake)(nil)
	_ forge.ChangeRequestUpdater = (*Fake)(nil)
)

// NewFake creates a bare repository in dir whose branch has one commit with
// files (path -> content).
func NewFake(dir, branch string, files map[string]string) (*Fake, error) {
	if _, err := gittest.NewBare(dir, branch, files); err != nil {
		return nil, err
	}
	return &Fake{
		Dir:           dir,
		DefaultBranch: branch,
		crs:           map[string]*forge.ChangeRequest{},
		inputs:        map[string]forge.ChangeRequestInput{},
		comments:      map[string][]core.Message{},
	}, nil
}

func (f *Fake) Remote(context.Context) (*forge.GitRemote, error) {
	return &forge.GitRemote{URL: f.Dir, DefaultBranch: f.DefaultBranch}, nil
}

func (f *Fake) OpenChangeRequest(_ context.Context, in forge.ChangeRequestInput) (*forge.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cr, ok := f.crs[in.SourceBranch]; ok {
		c := *cr
		return &c, nil
	}
	if !f.BranchExists(in.SourceBranch) {
		return nil, fmt.Errorf("source branch %q does not exist", in.SourceBranch)
	}
	f.opens++
	id := strconv.Itoa(f.opens)
	cr := &forge.ChangeRequest{
		ID:           id,
		URL:          "https://forge.test/" + filepath.Base(f.Dir) + "/-/merge_requests/" + id,
		SourceBranch: in.SourceBranch,
		TargetBranch: in.TargetBranch,
	}
	f.crs[in.SourceBranch] = cr
	f.inputs[id] = in
	c := *cr
	return &c, nil
}

func (f *Fake) CommentChangeRequest(_ context.Context, cr *forge.ChangeRequest, msg core.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments[cr.ID] = append(f.comments[cr.ID], msg)
	return nil
}

func (f *Fake) UpdateChangeRequest(_ context.Context, cr *forge.ChangeRequest, in forge.ChangeRequestInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inputs[cr.ID] = in
	return nil
}

// BranchExists reports whether the bare repository has branch.
func (f *Fake) BranchExists(branch string) bool {
	return gittest.Git(f.Dir, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
}

// ChangeRequests returns the open change requests, by source branch.
func (f *Fake) ChangeRequests() map[string]forge.ChangeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]forge.ChangeRequest{}
	for k, v := range f.crs {
		out[k] = *v
	}
	return out
}

// Input returns the latest input (open or update) for a change request.
func (f *Fake) Input(id string) forge.ChangeRequestInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inputs[id]
}

// Comments returns the comments on a change request.
func (f *Fake) Comments(id string) []core.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.comments[id])
}

// Run checks f against the forge contract. It pushes a throwaway branch,
// opens a change request for it and comments on it, so point it at a sandbox
// repository. The branch is deleted afterwards; the change request stays open.
func Run(t *testing.T, f forge.Forge) {
	ctx := context.Background()

	remote, err := f.Remote(ctx)
	if err != nil {
		t.Fatalf("Remote: %v", err)
	}
	if remote.URL == "" || remote.DefaultBranch == "" {
		t.Fatalf("Remote needs URL and DefaultBranch: %+v", remote)
	}

	repo, err := workspace.Clone(ctx, remote, filepath.Join(t.TempDir(), "repo"))
	if err != nil {
		t.Fatalf("cloning with Remote's credentials: %v", err)
	}
	branch := "hinziel-conformance-" + testutil.Suffix()
	if err := repo.CheckoutBranch(ctx, branch, "origin/"+remote.DefaultBranch); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo.Dir, branch+".txt"), []byte("conformance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitAll(ctx, "Hinziel conformance test"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Push(ctx, branch); err != nil {
		t.Fatalf("pushing with Remote's credentials: %v", err)
	}
	t.Cleanup(func() { repo.DeleteRemoteBranch(context.Background(), branch) })

	in := forge.ChangeRequestInput{
		SourceBranch: branch,
		TargetBranch: remote.DefaultBranch,
		Title:        "Hinziel conformance test",
		Description:  core.Message{Body: "Opened by **forgetest**. Safe to close."},
		Draft:        true,
	}
	cr, err := f.OpenChangeRequest(ctx, in)
	if err != nil {
		t.Fatalf("OpenChangeRequest: %v", err)
	}
	if cr.ID == "" || cr.URL == "" || cr.SourceBranch != branch || cr.TargetBranch != remote.DefaultBranch {
		t.Fatalf("change request incomplete: %+v", cr)
	}

	t.Run("OpenChangeRequestIsIdempotent", func(t *testing.T) {
		again, err := f.OpenChangeRequest(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != cr.ID {
			t.Fatalf("second open returned %q, want existing %q", again.ID, cr.ID)
		}
	})
	t.Run("CommentChangeRequest", func(t *testing.T) {
		if err := f.CommentChangeRequest(ctx, cr, core.Message{Body: "Comment from **forgetest**."}); err != nil {
			t.Fatal(err)
		}
	})
	if u, ok := f.(forge.ChangeRequestUpdater); ok {
		t.Run("UpdateChangeRequest", func(t *testing.T) {
			in2 := in
			in2.Description = core.Message{Body: "Updated by **forgetest**."}
			if err := u.UpdateChangeRequest(ctx, cr, in2); err != nil {
				t.Fatal(err)
			}
		})
	}
}
