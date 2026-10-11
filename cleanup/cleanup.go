// Package cleanup defines how a factory stops what the agent leaves running
// on the factory's host (a dev stack, a database, a server), so it doesn't
// outlive the run. Implementations live in subpackages.
//
// The engine calls every cleaner of the factory when implement ends,
// whether it succeeded or not, and when a run is cancelled, which also
// covers a run interrupted during implement. A cleaner's error is logged,
// never fails the run: a leftover must not undo the work.
//
// What the agent starts as processes of its own is the agent component's
// to stop (agent/claudecode stops its whole process group). Cleaners are for
// what outlives those processes, such as containers a Docker daemon runs.
package cleanup

import (
	"context"
	"io"
)

// Cleaner stops what the agent started for one run. Calling it again, or
// when nothing is running, must be safe.
type Cleaner interface {
	Cleanup(ctx context.Context, w *Workspace) error
}

// Workspace is the run's checkouts on the factory's host.
type Workspace struct {
	Dir   string     // holds every checkout, side by side
	Repos []Checkout // in the factory's order
	// Env is the environment the agent ran with. A cleaner that runs a
	// command from a checkout runs it with Env, never with the factory's
	// own environment: the checkout holds code the agent wrote, and the
	// factory's credentials must not reach it.
	Env []string
	Log io.Writer // the run log
}

type Checkout struct {
	Name string // Repo.Name
	Path string
}
