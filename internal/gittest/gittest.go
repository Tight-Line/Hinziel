// Package gittest creates throwaway git repositories for tests.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// NewBare creates a bare repository at dir whose branch has one commit with
// files (path -> content). It returns dir.
func NewBare(dir, branch string, files map[string]string) (string, error) {
	src, err := os.MkdirTemp("", "gittest-src-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(src)

	for name, content := range files {
		p := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return "", err
		}
	}
	steps := [][]string{
		{"init", "--quiet", "--initial-branch", branch, src},
		{"-C", src, "add", "--all"},
		{"-C", src, "-c", "user.name=test", "-c", "user.email=test@localhost", "commit", "--quiet", "--allow-empty", "-m", "initial"},
		{"clone", "--quiet", "--bare", src, dir},
	}
	for _, args := range steps {
		if err := Git("", args...); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// Git runs git in dir and returns an error that includes git's output.
func Git(dir string, args ...string) error {
	_, err := Output(dir, args...)
	return err
}

// Output runs git in dir and returns its trimmed stdout.
func Output(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return "", &gitError{args: args, err: err, stderr: string(exit.Stderr)}
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

type gitError struct {
	args   []string
	err    error
	stderr string
}

func (e *gitError) Error() string {
	return "git " + strings.Join(e.args, " ") + ": " + e.err.Error() + ": " + strings.TrimSpace(e.stderr)
}

func (e *gitError) Unwrap() error { return e.err }
