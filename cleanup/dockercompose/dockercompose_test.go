package dockercompose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/cleanup"
)

// fakeDocker writes a docker command that lists ps as its containers and
// records every other invocation, one per line, in calls.
func fakeDocker(t *testing.T, ps string) (cmd []string, calls string) {
	t.Helper()
	dir := t.TempDir()
	calls = filepath.Join(dir, "calls")
	list := filepath.Join(dir, "ps")
	write(t, list, ps, 0o644)
	bin := filepath.Join(dir, "docker")
	write(t, bin, fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = ps ]; then cat %q; else echo \"$*\" >>%q; fi\n", list, calls), 0o755)
	return []string{bin}, calls
}

func write(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func readCalls(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func workspace(dir string, log *strings.Builder) *cleanup.Workspace {
	return &cleanup.Workspace{Dir: dir, Env: []string{"PATH=" + os.Getenv("PATH")}, Log: log}
}

func TestOnlyTheRunsContainersAreTouched(t *testing.T) {
	repos := "/state/runs/r1/repos"
	cmd, calls := fakeDocker(t, strings.Join([]string{
		"c1\tapi\t" + repos + "/api",          // the run's own project
		"c2\tapi\t" + repos + "/api",          //
		"c3\tapp\t" + repos + "/app",          // the run's, but the name is shared...
		"c4\tapp\t/state/runs/r0/repos/app",   // ...with an earlier run's
		"c5\tshop\t/home/dev/shop",            // the developer's own stack
		"c6\treposx\t/state/runs/r1/reposx/a", // a sibling directory, not inside
	}, "\n")+"\n")

	var log strings.Builder
	if err := New(Options{Command: cmd}).Cleanup(context.Background(), workspace(repos, &log)); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"compose --project-name api down --volumes --remove-orphans",
		"rm --force --volumes c3",
	}
	if got := readCalls(t, calls); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("docker calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if !strings.Contains(log.String(), "stopped Compose project api") || !strings.Contains(log.String(), "others share its name") {
		t.Errorf("log:\n%s", log.String())
	}
}

func TestNothingRunningIsNotAnError(t *testing.T) {
	cmd, calls := fakeDocker(t, "")
	var log strings.Builder
	if err := New(Options{Command: cmd}).Cleanup(context.Background(), workspace(t.TempDir(), &log)); err != nil {
		t.Fatal(err)
	}
	if got := readCalls(t, calls); got != nil || log.Len() > 0 {
		t.Errorf("calls %q, log %q", got, log.String())
	}
}

func TestDockerFailing(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "docker")
	write(t, bin, "#!/bin/sh\necho 'Cannot connect to the Docker daemon' >&2\nexit 1\n", 0o755)
	err := New(Options{Command: []string{bin}}).Cleanup(context.Background(), workspace(t.TempDir(), &strings.Builder{}))
	if err == nil || !strings.Contains(err.Error(), "Cannot connect") {
		t.Errorf("err %v", err)
	}
}

func TestCheckNamesAMissingDocker(t *testing.T) {
	err := New(Options{Command: []string{"hinziel-test-no-docker"}}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "hinziel-test-no-docker") {
		t.Errorf("err %v", err)
	}
}
