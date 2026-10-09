// Package remotetest helps test code that uses package remote without a
// server: LocalSSH is an ssh command that runs everything on this machine.
package remotetest

import (
	"os"
	"path/filepath"
	"testing"
)

// LocalSSH returns an ssh command (for remote.Host.SSH) that ignores every
// option and runs the remote command locally, in the test's environment.
// Remote paths are therefore local paths.
func LocalSSH(t testing.TB) []string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ssh")
	script := "#!/bin/sh\nfor a in \"$@\"; do last=$a; done\nexec sh -c \"$last\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return []string{path}
}
