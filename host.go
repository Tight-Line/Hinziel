package hinziel

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Tight-Line/hinziel/core/remote"
	"github.com/Tight-Line/hinziel/internal/envcheck"
	"github.com/Tight-Line/hinziel/internal/hostinstall"
)

// host runs `host install`, which installs or updates the factory on a
// server over SSH. It runs on the machine you install from, so it needs the
// env file, not the variables themselves.
func host(ctx context.Context, out io.Writer, factories []Factory, args []string) error {
	const usage = "usage: host install [--env-file F] [--binary B] [--runs N] [--ssh-key K] [--force] <[user@]host[:port]>"
	if len(args) == 0 || args[0] != "install" {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("host install", flag.ContinueOnError)
	fs.SetOutput(out)
	envFile := fs.String("env-file", "", "the factory's variables as KEY=VALUE lines; files they name (keys, certificates) are copied too")
	binary := fs.String("binary", "", "the factory binary for the host (linux, CGO_ENABLED=0); default: this one")
	runs := fs.Int("runs", 1, "how many runs the daemon executes at once")
	key := fs.String("ssh-key", "", "the SSH key to log in with; default: ssh's")
	force := fs.Bool("force", false, "restart Docker to install Sysbox, and interrupt executing runs")
	pos, err := parse(fs, args[1:])
	if err != nil {
		return err
	}
	if len(pos) != 1 || *envFile == "" {
		return errors.New(usage)
	}
	h, err := parseTarget(pos[0])
	if err != nil {
		return err
	}
	h.KeyPath = *key
	if *binary == "" {
		if *binary, err = os.Executable(); err != nil {
			return err
		}
	}
	name := factories[0].Name
	if len(factories) > 1 {
		name = filepath.Base(*binary)
	}
	return hostinstall.Install(ctx, hostinstall.Options{
		Host:     h,
		Factory:  name,
		EnvFile:  *envFile,
		Required: envcheck.Read(),
		Binary:   *binary,
		Runs:     *runs,
		Force:    *force,
		Out:      out,
	})
}

// parseTarget reads [user@]host[:port]; the user defaults to root.
func parseTarget(s string) (*remote.Host, error) {
	h := &remote.Host{User: "root"}
	if user, rest, ok := strings.Cut(s, "@"); ok {
		h.User, s = user, rest
	}
	if addr, port, ok := strings.Cut(s, ":"); ok {
		p, err := strconv.Atoi(port)
		if err != nil {
			return nil, fmt.Errorf("bad port in %q", s)
		}
		s, h.Port = addr, p
	}
	if s == "" || h.User == "" {
		return nil, fmt.Errorf("bad host %q, want [user@]host[:port]", s)
	}
	h.Address = s
	return h, nil
}
