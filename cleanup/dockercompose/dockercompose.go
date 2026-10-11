// Package dockercompose is a cleanup.Cleaner for Docker Compose: it stops
// the Compose projects the agent started from the run's checkouts, such as
// the repository's dev stack, brought up to run its tests. Left running,
// they hold memory and host ports, and Docker publishes those ports past
// the host's firewall rules (ufw included).
//
// Nothing else on the host is touched: a container belongs to the run only
// if Compose recorded a working directory inside the run's checkouts. A
// project only the run uses is taken down with its volumes, which hold
// nothing but the run's scratch data. A project whose name containers
// outside the run share (two checkouts both called "app", say) loses only
// the run's own containers, and keeps its networks and volumes.
//
// # Credentials
//
// None. The docker command must be installed on the factory's host, and the
// factory's user able to use it (in the docker group). It runs with the
// agent's environment, so DOCKER_HOST and DOCKER_CONTEXT point it at the
// same Docker the agent used.
package dockercompose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Tight-Line/hinziel/cleanup"
	"github.com/Tight-Line/hinziel/core"
)

type Options struct {
	// Command is the docker executable; nil = {"docker"}.
	Command []string
}

// Cleaner stops the run's Compose projects.
type Cleaner struct{ opts Options }

var (
	_ cleanup.Cleaner = (*Cleaner)(nil)
	_ core.Checker    = (*Cleaner)(nil)
)

func New(opts Options) *Cleaner { return &Cleaner{opts: opts} }

func (c *Cleaner) command() []string {
	if len(c.opts.Command) > 0 {
		return c.opts.Command
	}
	return []string{"docker"}
}

// Check reports a missing docker command.
func (c *Cleaner) Check(context.Context) error { return core.NeedTools(c.command()[0]) }

func (c *Cleaner) Cleanup(ctx context.Context, w *cleanup.Workspace) error {
	log := w.Log
	if log == nil {
		log = io.Discard
	}
	out, err := c.docker(ctx, w, "ps", "--all", "--filter", "label=com.docker.compose.project",
		"--format", "{{.ID}}\t{{.Label \"com.docker.compose.project\"}}\t{{.Label \"com.docker.compose.project.working_dir\"}}")
	if err != nil {
		return fmt.Errorf("listing containers: %w", err)
	}
	mine := map[string][]string{} // project → the run's containers
	shared := map[string]bool{}   // project → also has containers outside the run
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		id, rest, _ := strings.Cut(line, "\t")
		project, workdir, _ := strings.Cut(rest, "\t")
		if id == "" || project == "" {
			continue
		}
		if within(w.Dir, workdir) {
			mine[project] = append(mine[project], id)
		} else {
			shared[project] = true
		}
	}

	projects := make([]string, 0, len(mine))
	for p := range mine {
		projects = append(projects, p)
	}
	slices.Sort(projects)
	var errs []error
	for _, p := range projects {
		args := []string{"compose", "--project-name", p, "down", "--volumes", "--remove-orphans"}
		what := "stopped Compose project " + p + " and its volumes"
		if shared[p] {
			args = append([]string{"rm", "--force", "--volumes"}, mine[p]...)
			what = fmt.Sprintf("removed this run's %d containers of Compose project %s; others share its name, so its networks and volumes stay", len(mine[p]), p)
		}
		if _, err := c.docker(ctx, w, args...); err != nil {
			errs = append(errs, fmt.Errorf("Compose project %s: %w", p, err))
			continue
		}
		fmt.Fprintf(log, "cleanup: %s\n", what)
	}
	return errors.Join(errs...)
}

func (c *Cleaner) docker(ctx context.Context, w *cleanup.Workspace, args ...string) (string, error) {
	cmdline := c.command()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, cmdline[0], append(cmdline[1:], args...)...)
	cmd.Env = w.Env
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
