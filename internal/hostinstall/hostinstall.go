// Package hostinstall installs a factory on a server, or updates it: `host
// install`. The host needs only SSH and root (or passwordless sudo); the
// rest it gets here: Docker, Sysbox if a sandbox has Docker inside, a hinziel
// user, the factory's
// configuration and binary, and the factory's container running `daemon`.
// See the README, "Hosting a factory".
package hostinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/Tight-Line/hinziel/core/remote"
)

// SysboxVersion is the Sysbox release installed on hosts that have none.
const SysboxVersion = "0.7.1"

//go:embed prepare.sh
var prepareScript string

//go:embed factory.Dockerfile
var factoryDockerfile string

// Options say what to install where.
type Options struct {
	Host    *remote.Host
	Factory string // the factory's name; the directories and the container are named after it
	EnvFile string // the factory's variables, as KEY=VALUE lines
	// Required are variables the factory reads (hinziel.Env); each must have
	// a value in EnvFile.
	Required []string
	Binary   string // the factory binary for the host: linux, statically linked
	Runs     int    // daemon --runs; 0 = 1
	// Sandbox gives the factory's container the host's Docker socket, to
	// start the agents' sandboxes. Without it, the agents run in the
	// factory's container.
	Sandbox bool
	Sysbox  bool // a sandbox has Docker inside, which needs Sysbox
	Force   bool // restart Docker for Sysbox, and interrupt executing runs
	Out     io.Writer
}

// Paths on the host.
func configDir(factory string) string { return "/etc/hinziel/" + factory }
func dataDir(factory string) string   { return "/var/lib/hinziel/" + factory }
func binPath(factory string) string   { return dataDir(factory) + "/bin/" + factory }

// Container is the name of the factory's container.
func Container(factory string) string { return "hinziel-" + factory }

// Install installs or updates the factory on the host.
func Install(ctx context.Context, o Options) error {
	if o.Out == nil {
		o.Out = io.Discard
	}
	if o.Runs == 0 {
		o.Runs = 1
	}
	say := func(format string, args ...any) { fmt.Fprintf(o.Out, "==> "+format+"\n", args...) }
	h := &sudoHost{o.Host}

	arch, err := binaryArch(o.Binary)
	if err != nil {
		return err
	}
	vars, err := readEnvFile(o.EnvFile)
	if err != nil {
		return err
	}
	var missing []string
	for _, name := range o.Required {
		if !slices.ContainsFunc(vars, func(v envVar) bool { return v.Name == name && v.Value != "" }) {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s has no value for %s, which the factory reads", o.EnvFile, strings.Join(missing, ", "))
	}
	vars, files := relocate(vars, configDir(o.Factory)+"/files")
	vars = slices.DeleteFunc(vars, func(v envVar) bool { return v.Name == "HINZIEL_STATE_DIR" || v.Name == "HOME" })
	envFile, err := format(vars)
	if err != nil {
		return err
	}

	tools := "Docker"
	if o.Sysbox {
		tools += ", Sysbox"
	}
	say("preparing %s: %s, the hinziel user, /etc/hinziel/%s and /var/lib/hinziel/%s", o.Host.Address, tools, o.Factory, o.Factory)
	script := fmt.Sprintf("FACTORY=%s SYSBOX=%d SYSBOX_VERSION=%s FORCE=%d\n%s", remote.Quote(o.Factory), b2i(o.Sysbox), SysboxVersion, b2i(o.Force), prepareScript)
	ids, err := h.output(ctx, script)
	if err != nil {
		return err
	}
	f := strings.Fields(ids)
	if len(f) != 4 {
		return fmt.Errorf("preparing the host: unexpected output %q", ids)
	}
	uid, gid, dockerGID, machine := f[0], f[1], f[2], f[3]
	if want := map[string]string{"x86_64": "amd64", "aarch64": "arm64"}[machine]; want != arch {
		return fmt.Errorf("the binary is for %s, the host is %s (build it with GOOS=linux GOARCH=%s CGO_ENABLED=0)", arch, machine, want)
	}

	if !o.Force {
		running, err := h.output(ctx, fmt.Sprintf(`grep -l '"status": "running"' %s/state/runs/*/run.json 2>/dev/null || true`, dataDir(o.Factory)))
		if err != nil {
			return err
		}
		if s := strings.TrimSpace(running); s != "" && containerRunning(ctx, h, o.Factory) {
			return fmt.Errorf("runs are executing on the host, and updating would interrupt them:\n%s\nwait for them, or run again with --force (they can be resumed)", s)
		}
	}

	say("copying the configuration (%d variables, %d files) and the binary", len(vars), len(files))
	if err := h.writeFile(ctx, configDir(o.Factory)+"/env", []byte(envFile), "600", "root:root"); err != nil {
		return err
	}
	for _, fl := range files {
		data, err := os.ReadFile(fl.Local)
		if err != nil {
			return err
		}
		if err := h.writeFile(ctx, fl.Remote, data, "600", "hinziel:hinziel"); err != nil {
			return err
		}
	}
	bin, err := os.ReadFile(o.Binary)
	if err != nil {
		return err
	}
	if err := h.writeFile(ctx, binPath(o.Factory), bin, "755", "hinziel:hinziel"); err != nil {
		return err
	}

	sum := sha256.Sum256([]byte(factoryDockerfile))
	image := "hinziel-factory:" + hex.EncodeToString(sum[:])[:16]
	say("building the factory's image %s on the host (once per Hinziel release that changes it)", image)
	build := fmt.Sprintf("docker image inspect %[1]s >/dev/null 2>&1 || docker build --quiet --tag %[1]s --label hinziel.factory-image=1 - >/dev/null", image)
	if err := h.run(ctx, build, strings.NewReader(factoryDockerfile), nil); err != nil {
		return fmt.Errorf("building the factory's image: %w", err)
	}

	say("starting the factory's container %s: daemon --runs %d", Container(o.Factory), o.Runs)
	cfg, data := configDir(o.Factory), dataDir(o.Factory)
	run := []string{
		"docker run --detach --init --restart unless-stopped",
		"--name", Container(o.Factory), "--label", "hinziel.factory=" + remote.Quote(o.Factory),
		"--user", uid + ":" + gid,
		"--env-file", cfg + "/env",
		"--env", "HOME=" + data + "/home", "--env", "HINZIEL_STATE_DIR=" + data + "/state",
	}
	if o.Sandbox {
		// Only with sandboxes: an agent in this container would be root on
		// the host with the socket.
		run = append(run, "--group-add", dockerGID, "--volume", "/var/run/docker.sock:/var/run/docker.sock")
	}
	run = append(run,
		// The same paths inside as on the host: the sandboxes are
		// started by the host's Docker, which mounts host paths.
		"--mount", "type=bind,source="+cfg+",target="+cfg+",readonly",
		"--mount", "type=bind,source="+data+",target="+data,
		image, binPath(o.Factory), "daemon", "--runs", strconv.Itoa(o.Runs),
	)
	start := strings.Join([]string{
		"set -e",
		"docker rm --force " + Container(o.Factory) + " >/dev/null 2>&1 || true",
		strings.Join(run, " ") + " >/dev/null",
	}, "\n")
	if err := h.run(ctx, start, nil, nil); err != nil {
		return err
	}

	if o.Sandbox {
		say("checking the factory on the host (the first check builds the agent's sandbox image, which takes a few minutes)")
	} else {
		say("checking the factory on the host")
	}
	var out bytes.Buffer
	err = h.run(ctx, "docker exec "+Container(o.Factory)+" "+binPath(o.Factory)+" check", nil, io.MultiWriter(o.Out, &out))
	if err != nil {
		return fmt.Errorf("the factory is running, but its check failed; see above, fix the env file and run host install again")
	}
	say("done: the factory runs as container %s; logs: ssh %s docker logs -f %s", Container(o.Factory), o.Host.Address, Container(o.Factory))
	return nil
}

func containerRunning(ctx context.Context, h *sudoHost, factory string) bool {
	out, err := h.output(ctx, "docker inspect --format '{{.State.Running}}' "+Container(factory)+" 2>/dev/null || true")
	return err == nil && strings.TrimSpace(out) == "true"
}

// binaryArch checks that path is a Linux binary that runs without the
// host's libraries, and returns its GOARCH.
func binaryArch(path string) (string, error) {
	f, err := elf.Open(path)
	if err != nil {
		return "", fmt.Errorf("the factory binary %s isn't a Linux executable (build it with GOOS=linux CGO_ENABLED=0 and pass it with --binary): %w", path, err)
	}
	defer f.Close()
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return "", fmt.Errorf("the factory binary %s is dynamically linked; build it with CGO_ENABLED=0 and pass it with --binary", path)
		}
	}
	switch f.Machine {
	case elf.EM_X86_64:
		return "amd64", nil
	case elf.EM_AARCH64:
		return "arm64", nil
	}
	return "", fmt.Errorf("the factory binary %s is for %s, which Hinziel doesn't install", path, f.Machine)
}

// sudoHost runs scripts as root: directly when logged in as root, else
// through passwordless sudo.
type sudoHost struct{ *remote.Host }

func (h *sudoHost) wrap(script string) string {
	if h.User == "root" {
		return script
	}
	return "sudo -n sh -c " + remote.Quote(script)
}

func (h *sudoHost) run(ctx context.Context, script string, stdin io.Reader, out io.Writer) error {
	return h.Run(ctx, h.wrap(script), stdin, out)
}

func (h *sudoHost) output(ctx context.Context, script string) (string, error) {
	out, err := h.Output(ctx, h.wrap(script))
	if err != nil && strings.Contains(err.Error(), "a password is required") {
		return "", errors.New("the SSH user needs passwordless sudo, or log in as root")
	}
	return out, err
}

// writeFile replaces path atomically, owned and moded as given.
func (h *sudoHost) writeFile(ctx context.Context, path string, data []byte, mode, owner string) error {
	q := remote.Quote(path)
	script := fmt.Sprintf("set -e; umask 077; cat > %[1]s.new; chown %[2]s %[1]s.new; chmod %[3]s %[1]s.new; mv %[1]s.new %[1]s", q, owner, mode)
	return h.run(ctx, script, bytes.NewReader(data), nil)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
