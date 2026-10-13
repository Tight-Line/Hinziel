// Package envtest provides an in-memory provisioner for tests and a
// conformance suite for env.Provisioner implementations.
package envtest

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/internal/testutil"
)

// Fake is an in-memory env.Provisioner and env.Lister. Every environment gets
// one host, "main", at a documentation address, and a new ID each time it's
// created, as a real provider's would.
type Fake struct {
	mu       sync.Mutex
	envs     map[string]*env.Environment
	ensures  int
	destroys int
	created  int

	EnsureErrs []error // returned by the next Ensure calls, in order
}

var (
	_ env.Provisioner = (*Fake)(nil)
	_ env.Lister      = (*Fake)(nil)
)

func NewFake() *Fake { return &Fake{envs: map[string]*env.Environment{}} }

func (f *Fake) Ensure(_ context.Context, spec env.Spec) (*env.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ensures++
	if err := testutil.PopErr(&f.EnsureErrs); err != nil {
		return nil, err
	}
	e, ok := f.envs[spec.Name]
	if !ok {
		f.created++
		e = &env.Environment{
			Name:     spec.Name,
			Provider: "fake",
			ID:       fmt.Sprintf("fake-%s-%d", spec.Name, f.created),
			Hosts:    []env.Host{{Name: "main", Address: "203.0.113.10"}},
		}
		f.envs[spec.Name] = e
	}
	return clone(e), nil
}

func (f *Fake) Destroy(_ context.Context, e *env.Environment) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.destroys++
	delete(f.envs, e.Name)
	return nil
}

func (f *Fake) List(context.Context) ([]env.Environment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []env.Environment
	for _, e := range f.envs {
		out = append(out, *clone(e))
	}
	return out, nil
}

// Ensures counts Ensure calls, including failed ones.
func (f *Fake) Ensures() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ensures
}

func clone(e *env.Environment) *env.Environment {
	c := *e
	c.Hosts = slices.Clone(e.Hosts)
	c.State = maps.Clone(e.State)
	return &c
}

// Run checks p against the provisioner contract. It creates and destroys a
// real environment named "skct-<random>".
func Run(t *testing.T, p env.Provisioner) {
	ctx := context.Background()
	spec := env.Spec{Name: "skct-" + testutil.Suffix(), Labels: map[string]string{"hinziel": "conformance"}}
	t.Cleanup(func() { p.Destroy(context.Background(), &env.Environment{Name: spec.Name}) })

	e, err := p.Ensure(ctx, spec)
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if e.Name != spec.Name || e.ID == "" {
		t.Fatalf("environment needs Name = spec.Name and an ID: %+v", e)
	}

	t.Run("EnsureIsIdempotent", func(t *testing.T) {
		again, err := p.Ensure(ctx, spec)
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != e.ID {
			t.Fatalf("second Ensure created %q, want existing %q", again.ID, e.ID)
		}
	})
	t.Run("EnvironmentIsJSON", func(t *testing.T) {
		if _, err := json.Marshal(e); err != nil {
			t.Fatal(err)
		}
	})
	lister, canList := p.(env.Lister)
	if canList {
		t.Run("ListContainsEnvironment", func(t *testing.T) {
			if !listed(t, lister, spec.Name) {
				t.Fatalf("List does not contain %q", spec.Name)
			}
		})
	}
	t.Run("DestroyIsIdempotent", func(t *testing.T) {
		if err := p.Destroy(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := p.Destroy(ctx, e); err != nil {
			t.Fatalf("destroying a missing environment must not fail: %v", err)
		}
		if canList && listed(t, lister, spec.Name) {
			t.Fatalf("List still contains %q after Destroy", spec.Name)
		}
	})
}

func listed(t *testing.T, l env.Lister, name string) bool {
	t.Helper()
	envs, err := l.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return slices.ContainsFunc(envs, func(e env.Environment) bool { return e.Name == name })
}
