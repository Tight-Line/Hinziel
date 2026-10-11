//go:build unix

package claudecode

import (
	"context"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWhatClaudeLeftRunningIsStopped(t *testing.T) {
	a, task, log := setup(t, "background", 1)
	if _, err := a.Implement(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(read(t, log+".bgpid")))
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			syscall.Kill(pid, syscall.SIGKILL)
			t.Fatal("the process claude started in the background is still running")
		}
	}
}
