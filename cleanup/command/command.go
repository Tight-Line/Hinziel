// Package command is a cleanup.Cleaner that runs the team's own command in a
// checkout, such as `./toolkit down` or `make stop`: whatever the repository
// already uses to stop what its development setup starts.
//
// The command comes from the run's checkout, which holds code the agent
// wrote, so it runs with the agent's environment (cleanup.Workspace.Env),
// never with the factory's credentials.
//
// # Credentials
//
// None. Whatever the command needs on the factory's host (Docker, a
// language runtime) must be installed there; list it in Tools so `check`
// reports it.
package command

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/Tight-Line/hinziel/cleanup"
	"github.com/Tight-Line/hinziel/core"
)

type Options struct {
	Repo    string   // the Repo.Name of the checkout to run in; "" = the directory holding every checkout
	Command string   // run with sh -c, e.g. "./toolkit down"
	Tools   []string // commands it needs on the host, for `check`; sh is always checked
}

// Cleaner runs Options.Command.
type Cleaner struct{ opts Options }

var (
	_ cleanup.Cleaner = (*Cleaner)(nil)
	_ core.Checker    = (*Cleaner)(nil)
)

func New(opts Options) *Cleaner { return &Cleaner{opts: opts} }

func (c *Cleaner) Check(context.Context) error {
	if strings.TrimSpace(c.opts.Command) == "" {
		return fmt.Errorf("Command is empty")
	}
	return core.NeedTools(append([]string{"sh"}, c.opts.Tools...)...)
}

func (c *Cleaner) Cleanup(ctx context.Context, w *cleanup.Workspace) error {
	dir := w.Dir
	if c.opts.Repo != "" {
		dir = ""
		for _, r := range w.Repos {
			if r.Name == c.opts.Repo {
				dir = r.Path
			}
		}
		if dir == "" {
			return fmt.Errorf("the run has no checkout of repo %q", c.opts.Repo)
		}
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", c.opts.Command)
	cmd.Dir = dir
	cmd.Env = append([]string{}, w.Env...) // never nil: nil would mean the factory's environment
	cmd.Stdout, cmd.Stderr = w.Log, w.Log
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", c.opts.Command, err)
	}
	return nil
}
