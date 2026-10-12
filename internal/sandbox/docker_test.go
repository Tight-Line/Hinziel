package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// Wrap passes variables by name, so their values stay out of the command
// line, and leaves out those that describe the factory's host.
func TestWrap(t *testing.T) {
	inst := &dockerInstance{d: &Docker{Command: []string{"/usr/bin/docker"}}, name: "hinziel-shop-1"}
	cmd := exec.Command("claude", "-p", "--model", "opus")
	cmd.Dir = "/state/runs/1/repos/api"
	cmd.Env = []string{"PATH=/usr/bin", "HOME=/home/hinziel", "XDG_RUNTIME_DIR=/run/user/1000", "LC_ALL=de_DE.UTF-8",
		"CLAUDE_CODE_OAUTH_TOKEN=secret", "HINZIEL_SOCKET=/state/runs/1/agent/hinziel.sock", "TZ=UTC"}
	if err := inst.Wrap(cmd); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/docker", "exec", "--interactive", "--user", "agent", "--workdir", "/state/runs/1/repos/api",
		"--env", "CLAUDE_CODE_OAUTH_TOKEN", "--env", "HINZIEL_SOCKET", "--env", "TZ",
		"hinziel-shop-1", "claude", "-p", "--model", "opus"}
	if !slices.Equal(cmd.Args, want) {
		t.Errorf("args\n got %q\nwant %q", cmd.Args, want)
	}
	if cmd.Path != "/usr/bin/docker" || cmd.Err != nil {
		t.Errorf("path %q, err %v", cmd.Path, cmd.Err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "secret") {
		t.Error("a value is on the command line")
	}
}

func TestDockerfile(t *testing.T) {
	base := Dockerfile(Contents{})
	if strings.Contains(base, "docker-ce") || !strings.Contains(base, "claude-code") || !strings.Contains(base, "ENTRYPOINT") {
		t.Errorf("base image:\n%s", base)
	}
	full := Dockerfile(Contents{Docker: true, Dockerfile: "RUN apt-get install -y php-cli"})
	docker, factory, entry := strings.Index(full, "docker-ce"), strings.Index(full, "php-cli"), strings.Index(full, "ENTRYPOINT")
	if docker < 0 || factory < docker || entry < factory {
		t.Errorf("want base, Docker, the factory's lines, entrypoint:\n%s", full)
	}
}

func TestDockerNeedsSysbox(t *testing.T) {
	d := &Docker{Runtime: "runc"}
	err := d.checkRuntime(context.Background(), Contents{Docker: true})
	if err == nil || !strings.Contains(err.Error(), "sysbox-runc") || !strings.Contains(err.Error(), "--privileged") {
		t.Errorf("got %v", err)
	}
}

// TestIntegration starts a real sandbox with plain runc: the agent runs as
// the factory's uid, sees its mounts at the same paths, gets the variables
// it's given and none of the host's.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_DOCKER") == "" {
		t.Skip("set HINZIEL_IT_DOCKER=1 (builds the sandbox image in the host's Docker, a few minutes the first time)")
	}
	ctx := context.Background()
	d := &Docker{Runtime: os.Getenv("HINZIEL_IT_RUNTIME")}
	if d.Runtime == "" {
		d.Runtime = "runc"
	}
	contents := Contents{Docker: d.Runtime == DefaultRuntime, Dockerfile: "RUN echo factory > /etc/factory-line"}
	work := t.TempDir()
	os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644)
	secret := t.TempDir()
	os.WriteFile(filepath.Join(secret, "env"), []byte("TOKEN=x\n"), 0o600)

	name := fmt.Sprintf("hinziel-it-%d", os.Getpid())
	inst, err := d.Start(ctx, Spec{Name: name, Contents: contents, Mounts: []Mount{{Path: work}}})
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Stop(ctx)

	script := `id -u; pwd; cat README.md; echo "$GREETING"; echo "home=$HOME"; cat /etc/factory-line; touch made-inside; test -e ` + secret + ` && echo LEAK || echo isolated`
	if contents.Docker {
		// A port published inside answers inside, and not on the host.
		script += `; docker run --detach --publish 18080:80 busybox httpd -f -p 80 >/dev/null 2>&1` +
			` && sleep 1 && echo "inside=$(curl -s -o /dev/null -w '%{http_code}' localhost:18080)"`
	}
	cmd := exec.Command("sh", "-c", script)
	cmd.Dir = work
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent-host-home", "GREETING=hi"}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := inst.Wrap(cmd); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	got := strings.Fields(out.String())
	want := []string{fmt.Sprint(os.Getuid()), work, "hello", "hi", "home=/home/agent", "factory", "isolated"}
	if contents.Docker {
		want = append(want, "inside=404")
		if c, err := net.DialTimeout("tcp", "127.0.0.1:18080", time.Second); err == nil {
			c.Close()
			t.Error("a port published in the sandbox's Docker answers on the host")
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if st, err := os.Stat(filepath.Join(work, "made-inside")); err != nil {
		t.Errorf("a file made inside isn't in the mount: %v", err)
	} else if uid := fileUID(st); uid != os.Getuid() {
		t.Errorf("a file made inside belongs to uid %d, not the factory's %d", uid, os.Getuid())
	}

	if err := inst.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if out, _ := exec.Command("docker", "ps", "-aq", "--filter", "name="+name).Output(); len(bytes.TrimSpace(out)) > 0 {
		t.Error("the sandbox is still there after Stop")
	}
}
