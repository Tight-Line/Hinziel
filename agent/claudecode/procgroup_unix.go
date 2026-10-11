//go:build unix

package claudecode

import (
	"os/exec"
	"syscall"
)

// inOwnGroup starts claude in a process group of its own, so that what it
// leaves running (a dev server, a watcher, a test runner in the background)
// can be stopped with it.
func inOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}

// killGroup stops every process left in claude's group once it has exited.
func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
