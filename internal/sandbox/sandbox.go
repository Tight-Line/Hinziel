// Package sandbox runs a run's agent in a container of its own. The factory
// says what's inside (Contents), the host how it runs (the Docker runtime,
// $HINZIEL_SANDBOX_RUNTIME). See the README, "The agent's sandbox".
package sandbox

import (
	"context"
	"os/exec"
	"sync"

	"github.com/Tight-Line/hinziel/agent"
)

// Contents is what a factory puts in its sandbox, on top of Hinziel's base
// image (git, ssh, Node, Claude Code).
type Contents struct {
	Docker     bool   // a Docker daemon of its own, with Compose
	Dockerfile string // lines appended to the image
}

// Spec is one run's sandbox.
type Spec struct {
	Name     string // unique per run; starting a name that exists replaces it
	Contents Contents
	Mounts   []Mount
	Labels   map[string]string
}

// Mount shares a path of the factory's host, at the same path inside.
type Mount struct {
	Path     string
	ReadOnly bool
}

// Runner starts sandboxes.
type Runner interface {
	Start(ctx context.Context, spec Spec) (Instance, error)
	// Remove removes the sandbox called name, if there is one.
	Remove(ctx context.Context, name string) error
}

// Instance is a running sandbox.
type Instance interface {
	Wrap(cmd *exec.Cmd) error
	Stop(ctx context.Context) error // removes it with everything started in it
}

// Lazy is a sandbox that starts on its first Wrap, so an agent that never
// runs a process (a test's fake) never starts a container.
type Lazy struct {
	runner Runner
	spec   Spec

	mu   sync.Mutex
	inst Instance
}

var _ agent.Sandbox = (*Lazy)(nil)

// NewLazy returns spec's sandbox, not started yet.
func NewLazy(r Runner, spec Spec) *Lazy { return &Lazy{runner: r, spec: spec} }

func (l *Lazy) Wrap(ctx context.Context, cmd *exec.Cmd) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inst == nil {
		inst, err := l.runner.Start(ctx, l.spec)
		if err != nil {
			return err
		}
		l.inst = inst
	}
	return l.inst.Wrap(cmd)
}

// Stop removes the sandbox if it was started.
func (l *Lazy) Stop(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inst == nil {
		return nil
	}
	err := l.inst.Stop(ctx)
	l.inst = nil
	return err
}
