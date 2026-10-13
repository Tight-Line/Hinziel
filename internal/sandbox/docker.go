package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Runtimes sandboxes run with, unless $HINZIEL_SANDBOX_RUNTIME says
// otherwise: Docker's own, or Sysbox for a sandbox with Docker inside, which
// it lets run without --privileged.
const (
	DefaultRuntime = "runc"
	SysboxRuntime  = "sysbox-runc"
)

// User is the user the agent runs as inside, with the factory's uid and gid,
// so it owns the checkouts it's given.
const User = "agent"

//go:embed base.Dockerfile
var baseDockerfile string

//go:embed docker.Dockerfile
var dockerDockerfile string

//go:embed entrypoint.sh
var entrypoint string

// Docker runs sandboxes as containers of the host's Docker.
type Docker struct {
	Runtime string   // "" = $HINZIEL_SANDBOX_RUNTIME, else by the contents
	Command []string // nil = {"docker"}
	// UID and GID are the agent's inside; 0 = this process's.
	UID, GID int
}

var _ Runner = (*Docker)(nil)

func (d *Docker) runtime(c Contents) string {
	if d.Runtime != "" {
		return d.Runtime
	}
	if r := os.Getenv("HINZIEL_SANDBOX_RUNTIME"); r != "" {
		return r
	}
	if c.Docker {
		return SysboxRuntime
	}
	return DefaultRuntime
}

func (d *Docker) ids() (int, int, error) {
	uid, gid := d.UID, d.GID
	if uid == 0 {
		uid = os.Getuid()
	}
	if gid == 0 {
		gid = os.Getgid()
	}
	if uid == 0 {
		return 0, 0, errors.New("the factory runs as root; run it as a user of its own, whose uid the agent gets in its sandbox")
	}
	return uid, gid, nil
}

func (d *Docker) docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := []string{"docker"}
	if len(d.Command) > 0 {
		cmd = d.Command
	}
	return exec.CommandContext(ctx, cmd[0], append(slices.Clone(cmd[1:]), args...)...)
}

func (d *Docker) output(ctx context.Context, args ...string) (string, error) {
	var stderr bytes.Buffer
	cmd := d.docker(ctx, args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Dockerfile is the sandbox image's Dockerfile for c.
func Dockerfile(c Contents) string {
	parts := []string{baseDockerfile}
	if c.Docker {
		parts = append(parts, dockerDockerfile)
	}
	if s := strings.TrimSpace(c.Dockerfile); s != "" {
		parts = append(parts, "# From the factory\nUSER root\n"+s+"\n")
	}
	parts = append(parts, "USER root\nCOPY entrypoint.sh /usr/local/bin/hinziel-sandbox\nENTRYPOINT [\"/usr/local/bin/hinziel-sandbox\"]\n")
	return strings.Join(parts, "\n")
}

// Image builds c's image unless it exists and returns its tag. The tag is a
// hash of everything that goes into it, so a change rebuilds it and nothing
// else does.
func (d *Docker) Image(ctx context.Context, c Contents, log func(string)) (string, error) {
	uid, gid, err := d.ids()
	if err != nil {
		return "", err
	}
	dockerfile := Dockerfile(c)
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d:%d", dockerfile, entrypoint, uid, gid)))
	tag := "hinziel-sandbox:" + hex.EncodeToString(sum[:])[:16]
	if _, err := d.output(ctx, "image", "inspect", "--format", "{{.Id}}", tag); err == nil {
		return tag, nil
	}
	dir, err := os.MkdirTemp("", "hinziel-sandbox-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "entrypoint.sh"), []byte(entrypoint), 0o755); err != nil {
		return "", err
	}
	if log != nil {
		log("building the sandbox image " + tag + " (once per change to its contents)")
	}
	var out bytes.Buffer
	cmd := d.docker(ctx, "build", "--quiet", "--tag", tag, "--label", "hinziel.sandbox=1",
		"--build-arg", fmt.Sprint("UID=", uid), "--build-arg", fmt.Sprint("GID=", gid), dir)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("building the sandbox image: %w\n%s", err, tail(out.String(), 3000))
	}
	return tag, nil
}

// Check verifies that Docker answers, that the runtime is there and can
// hold what c asks for, and builds c's image.
func (d *Docker) Check(ctx context.Context, c Contents) error {
	if err := d.checkRuntime(ctx, c); err != nil {
		return err
	}
	_, err := d.Image(ctx, c, nil)
	return err
}

func (d *Docker) checkRuntime(ctx context.Context, c Contents) error {
	rt := d.runtime(c)
	if c.Docker && rt != SysboxRuntime {
		return fmt.Errorf("the factory's sandbox has Docker inside, which needs the %s runtime (Sysbox: https://github.com/nestybox/sysbox), not %s; Hinziel never runs a sandbox --privileged", SysboxRuntime, rt)
	}
	runtimes, err := d.output(ctx, "info", "--format", "{{range $name, $_ := .Runtimes}}{{$name}} {{end}}")
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Fields(runtimes), rt) {
		hint := ""
		if rt == SysboxRuntime {
			hint = " (install Sysbox: https://github.com/nestybox/sysbox)"
		}
		return fmt.Errorf("Docker has no %s runtime%s; it has: %s", rt, hint, runtimes)
	}
	return nil
}

// Start replaces any sandbox called spec.Name with a new one and waits until
// it's ready, its Docker daemon included.
func (d *Docker) Start(ctx context.Context, spec Spec) (Instance, error) {
	if err := d.checkRuntime(ctx, spec.Contents); err != nil {
		return nil, err
	}
	tag, err := d.Image(ctx, spec.Contents, nil)
	if err != nil {
		return nil, err
	}
	if err := d.Remove(ctx, spec.Name); err != nil {
		return nil, err
	}
	args := []string{"run", "--detach", "--init", "--name", spec.Name, "--hostname", spec.Name,
		"--runtime", d.runtime(spec.Contents), "--label", "hinziel.sandbox=1"}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	for _, m := range spec.Mounts {
		v := "type=bind,source=" + m.Path + ",target=" + m.Path
		if m.ReadOnly {
			v += ",readonly"
		}
		args = append(args, "--mount", v)
	}
	if _, err := d.output(ctx, append(args, tag)...); err != nil {
		return nil, fmt.Errorf("starting the sandbox: %w", err)
	}
	inst := &dockerInstance{d: d, name: spec.Name}
	if spec.Contents.Docker {
		if err := inst.waitForDocker(ctx); err != nil {
			inst.Stop(context.WithoutCancel(ctx))
			return nil, err
		}
	}
	return inst, nil
}

// Remove removes the sandbox called name with everything in it.
func (d *Docker) Remove(ctx context.Context, name string) error {
	var stderr bytes.Buffer
	cmd := d.docker(ctx, "rm", "--force", "--volumes", name)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil && !strings.Contains(stderr.String(), "No such container") {
		return fmt.Errorf("removing sandbox %s: %w: %s", name, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

type dockerInstance struct {
	d    *Docker
	name string
}

func (i *dockerInstance) waitForDocker(ctx context.Context) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		_, err := i.d.output(ctx, "exec", "--user", User, i.name, "docker", "info", "--format", "{{.ServerVersion}}")
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			logs, _ := i.d.output(context.WithoutCancel(ctx), "exec", i.name, "tail", "-n", "20", "/var/log/dockerd.log")
			return fmt.Errorf("the sandbox's Docker daemon didn't start: %w\n%s", err, logs)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// hostOnly are variables that describe the factory's host, not the sandbox:
// the image has its own.
func hostOnly(name string) bool {
	switch name {
	case "PATH", "HOME", "USER", "LOGNAME", "SHELL", "TMPDIR", "LANG", "LANGUAGE",
		"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "CLAUDE_CONFIG_DIR":
		return true
	}
	return strings.HasPrefix(name, "XDG_") || strings.HasPrefix(name, "LC_")
}

// Wrap makes cmd a `docker exec` into the sandbox. The variables travel by
// name (-e NAME), so their values stay out of the command line; the docker
// client gets them from cmd.Env.
func (i *dockerInstance) Wrap(cmd *exec.Cmd) error {
	if len(cmd.Args) == 0 {
		return errors.New("sandbox: empty command")
	}
	args := []string{"exec", "--interactive", "--user", User}
	if cmd.Dir != "" {
		args = append(args, "--workdir", cmd.Dir)
	}
	var names []string
	for _, kv := range cmd.Env {
		name, _, _ := strings.Cut(kv, "=")
		if name != "" && !hostOnly(name) && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for _, name := range names {
		args = append(args, "--env", name)
	}
	args = append(args, i.name)
	args = append(args, cmd.Args...)

	docker := i.d.docker(context.Background(), args...)
	cmd.Path, cmd.Args, cmd.Err = docker.Path, docker.Args, docker.Err
	cmd.Dir = "" // inside, --workdir; outside, it may not exist
	return nil
}

func (i *dockerInstance) Stop(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	return i.d.Remove(ctx, i.name)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
