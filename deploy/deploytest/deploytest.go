// Package deploytest provides in-memory deployers for tests and a conformance
// suite for deploy.Deployer implementations.
package deploytest

import (
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/internal/testutil"
)

// Deployed records one Deploy call.
type Deployed struct {
	Env     string
	Repo    string
	Branch  string
	Commit  string
	Workdir string
	State   map[string]string // the environment's State the deployer was given
}

// Fake is an in-memory deploy.Deployer. It checks that Source.Workdir exists
// and that a commit was given, then records the call.
type Fake struct {
	URL   string            // returned as Deployment.URL
	State map[string]string // returned as Deployment.State

	mu       sync.Mutex
	deployed []Deployed

	DeployErrs []error // returned by the next Deploy calls, in order
}

var _ deploy.Deployer = (*Fake)(nil)

func (f *Fake) Deploy(_ context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := testutil.PopErr(&f.DeployErrs); err != nil {
		return nil, err
	}
	if src.Commit == "" {
		return nil, fmt.Errorf("no commit given")
	}
	if _, err := os.Stat(src.Workdir); err != nil {
		return nil, fmt.Errorf("workdir: %w", err)
	}
	f.deployed = append(f.deployed, Deployed{Env: e.Name, Repo: src.Repo, Branch: src.Branch, Commit: src.Commit, Workdir: src.Workdir, State: maps.Clone(e.State)})
	return &deploy.Deployment{URL: f.URL, Commit: src.Commit, At: time.Now(), State: maps.Clone(f.State)}, nil
}

// Deployed returns every successful Deploy call, in order.
func (f *Fake) Deployed() []Deployed {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.deployed)
}

// FakeDestroyer is a Fake that also implements deploy.Destroyer, recording
// the environments it was asked to destroy.
type FakeDestroyer struct {
	Fake

	dmu       sync.Mutex
	destroyed []string

	DestroyErrs []error // returned by the next Destroy calls, in order
}

var _ deploy.Destroyer = (*FakeDestroyer)(nil)

func (f *FakeDestroyer) Destroy(_ context.Context, e *env.Environment) error {
	f.dmu.Lock()
	defer f.dmu.Unlock()
	if err := testutil.PopErr(&f.DestroyErrs); err != nil {
		return err
	}
	f.destroyed = append(f.destroyed, e.Name)
	return nil
}

// Destroyed returns the names of the environments destroyed, in order.
func (f *FakeDestroyer) Destroyed() []string {
	f.dmu.Lock()
	defer f.dmu.Unlock()
	return slices.Clone(f.destroyed)
}

// FakeRetriever is a Fake that also implements deploy.Retriever. Files set
// with SetPending stand for changes made directly in the environment (clicks);
// the next Retrieve writes them into the working tree and forgets them.
type FakeRetriever struct {
	Fake

	rmu       sync.Mutex
	pending   map[string]string
	retrieves int
}

var _ deploy.Retriever = (*FakeRetriever)(nil)

// SetPending records a change made in the environment: path -> content.
func (f *FakeRetriever) SetPending(path, content string) {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	if f.pending == nil {
		f.pending = map[string]string{}
	}
	f.pending[path] = content
}

func (f *FakeRetriever) Retrieve(_ context.Context, _ *env.Environment, workdir string) error {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	f.retrieves++
	for path, content := range maps.All(f.pending) {
		p := filepath.Join(workdir, path)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	f.pending = nil
	return nil
}

// Retrieves counts Retrieve calls.
func (f *FakeRetriever) Retrieves() int {
	f.rmu.Lock()
	defer f.rmu.Unlock()
	return f.retrieves
}

// Run checks d against the deployer contract by deploying src to e twice. The
// caller provides a provisioned environment and a source checked out at a
// pushed commit.
func Run(t *testing.T, d deploy.Deployer, e *env.Environment, src *deploy.Source) {
	ctx := context.Background()
	dep, err := d.Deploy(ctx, e, src)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if dep.Commit != src.Commit {
		t.Fatalf("Deployment.Commit = %q, want %q", dep.Commit, src.Commit)
	}
	t.Run("RedeployIsSafe", func(t *testing.T) {
		if _, err := d.Deploy(ctx, e, src); err != nil {
			t.Fatal(err)
		}
	})
	if r, ok := d.(deploy.Retriever); ok {
		t.Run("Retrieve", func(t *testing.T) {
			if err := r.Retrieve(ctx, e, src.Workdir); err != nil {
				t.Fatal(err)
			}
		})
	}
}
