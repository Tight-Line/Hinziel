// Package cleanuptest provides a recording cleaner for tests.
package cleanuptest

import (
	"context"
	"sync"

	"github.com/Tight-Line/hinziel/cleanup"
	"github.com/Tight-Line/hinziel/internal/testutil"
)

// Fake records every workspace it is asked to clean up.
type Fake struct {
	mu    sync.Mutex
	calls []cleanup.Workspace

	CleanupErrs []error // returned by the next Cleanup calls, in order
}

var _ cleanup.Cleaner = (*Fake)(nil)

func (f *Fake) Cleanup(_ context.Context, w *cleanup.Workspace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, *w)
	return testutil.PopErr(&f.CleanupErrs)
}

// Calls returns the workspaces received, in order.
func (f *Fake) Calls() []cleanup.Workspace {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]cleanup.Workspace(nil), f.calls...)
}
