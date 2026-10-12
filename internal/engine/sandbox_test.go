package engine

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/internal/sandbox"
)

// fakeRunner runs commands on the host and records what it was asked to do.
type fakeRunner struct {
	mu      sync.Mutex
	started []sandbox.Spec
	wrapped [][]string
	stopped int
	removed []string
}

func (r *fakeRunner) Start(ctx context.Context, spec sandbox.Spec) (sandbox.Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, spec)
	return fakeInstance{r}, nil
}

func (r *fakeRunner) Remove(ctx context.Context, name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removed = append(r.removed, name)
	return nil
}

type fakeInstance struct{ r *fakeRunner }

func (i fakeInstance) Wrap(cmd *exec.Cmd) error {
	i.r.mu.Lock()
	defer i.r.mu.Unlock()
	i.r.wrapped = append(i.r.wrapped, slices.Clone(cmd.Args))
	return nil
}

func (i fakeInstance) Stop(ctx context.Context) error {
	i.r.mu.Lock()
	defer i.r.mu.Unlock()
	i.r.stopped++
	return nil
}

// The agent's processes run in a sandbox that holds the run's checkouts,
// its own directory and the factory binary, and nothing else; it's removed
// when implement ends, even when the agent fails, and again on cancel.
func TestAgentRunsInSandbox(t *testing.T) {
	f := newFixture(t)
	runner := &fakeRunner{}
	f.project.Sandbox = runner
	f.project.Settings.Sandbox = sandbox.Contents{Docker: true}
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		if task.Sandbox == nil {
			t.Fatal("the task has no sandbox")
		}
		cmd := exec.CommandContext(ctx, "true")
		if err := task.Sandbox.Wrap(ctx, cmd); err != nil {
			return nil, err
		}
		if err := cmd.Run(); err != nil {
			return nil, err
		}
		return nil, errors.New("agent gave up")
	}
	r, err := f.start()
	if err == nil {
		t.Fatal("the run should fail")
	}

	if len(runner.started) != 1 || runner.stopped != 1 {
		t.Fatalf("started %d, stopped %d; want 1 and 1", len(runner.started), runner.stopped)
	}
	spec := runner.started[0]
	name := "hinziel-shop-" + string(r.ID)
	if spec.Name != name || !spec.Contents.Docker || spec.Labels["hinziel.run"] != string(r.ID) {
		t.Errorf("spec %+v", spec)
	}
	dir := f.eng.Store().Dir(r.ID)
	want := []sandbox.Mount{
		{Path: filepath.Join(dir, "repos")},
		{Path: filepath.Join(dir, "agent")},
		{Path: "/usr/local/bin/shop-factory", ReadOnly: true},
	}
	for _, m := range want {
		if !slices.Contains(spec.Mounts, m) {
			t.Errorf("mount %+v missing from %+v", m, spec.Mounts)
		}
	}
	for _, m := range spec.Mounts {
		// The socket is mounted on its own when the agent's directory is too
		// deep for a socket path.
		if !m.ReadOnly && !slices.Contains(want, m) && filepath.Ext(m.Path) != ".sock" {
			t.Errorf("unexpected writable mount %+v", m)
		}
	}
	if len(runner.wrapped) != 1 || runner.wrapped[0][0] != "true" {
		t.Errorf("wrapped %q", runner.wrapped)
	}

	if err := f.eng.Cancel(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(runner.removed, name) {
		t.Errorf("cancel must remove what a dead process left behind; removed %q", runner.removed)
	}
}

// An agent that never runs a process never starts a sandbox.
func TestSandboxStartsOnFirstCommand(t *testing.T) {
	f := newFixture(t)
	runner := &fakeRunner{}
	f.project.Sandbox = runner
	f.agentChangesAPI()
	if _, err := f.start(); err != nil {
		t.Fatal(err)
	}
	if len(runner.started) != 0 || runner.stopped != 0 {
		t.Errorf("started %d, stopped %d; want none", len(runner.started), runner.stopped)
	}
}
