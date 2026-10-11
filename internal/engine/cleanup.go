package engine

import (
	"context"
	"path/filepath"
	"time"

	"github.com/Tight-Line/hinziel/cleanup"
)

// cleanUp runs the factory's cleaners on the run's checkouts, to stop what
// the agent left running. It runs even when ctx is done (implement timed
// out, say), and a cleaner's error is logged, never returned: a leftover
// must not fail the run.
func (x *execution) cleanUp(ctx context.Context) {
	if len(x.p.Cleaners) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	w := &cleanup.Workspace{
		Dir: x.reposDir(),
		Env: agentEnv(x.p.Settings.AgentEnv),
		Log: x.log,
	}
	for _, cfg := range x.p.Repos {
		w.Repos = append(w.Repos, cleanup.Checkout{Name: cfg.Name, Path: filepath.Join(x.reposDir(), cfg.Name)})
	}
	for i, c := range x.p.Cleaners {
		if err := c.Cleanup(ctx, w); err != nil {
			x.logf(StepImplement, "cleanup %d: %v", i, err)
		}
	}
}
