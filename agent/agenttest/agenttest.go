// Package agenttest provides a scriptable agent for tests. There is no
// conformance suite: what "implementing a ticket" means can't be checked
// generically, so agents are tested end to end with a factory instead.
package agenttest

import (
	"context"
	"sync"

	"github.com/Tight-Line/skrat/agent"
)

// Fake runs Func for every task and records the tasks it was given.
// A nil Func does nothing and returns an empty outcome.
type Fake struct {
	Func func(ctx context.Context, task *agent.Task) (*agent.Outcome, error)

	mu    sync.Mutex
	tasks []agent.Task
}

var _ agent.Agent = (*Fake)(nil)

func (f *Fake) Implement(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
	f.mu.Lock()
	f.tasks = append(f.tasks, *task)
	f.mu.Unlock()
	if f.Func == nil {
		return &agent.Outcome{}, nil
	}
	return f.Func(ctx, task)
}

// Tasks returns the tasks the agent was given, in order.
func (f *Fake) Tasks() []agent.Task {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]agent.Task, len(f.tasks))
	copy(out, f.tasks)
	return out
}
