// Package workspace runs git through the git CLI: clone, branch, commit, push,
// and the checks the engine needs ("is everything committed and pushed?").
package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Tight-Line/skrat/forge"
)

// Identity used for commits made by the engine (and by default the agent).
const (
	CommitName  = "Skrat"
	CommitEmail = "skrat@localhost"
)

// Repo is a checkout on disk.
type Repo struct {
	Dir    string
	env    []string
	secret string // redacted from error messages
}

// Clone clones remote into dir, or opens dir and fetches if it is already a
// checkout, so calling it again (on resume) is safe.
func Clone(ctx context.Context, remote *forge.GitRemote, dir string) (*Repo, error) {
	cloneURL, err := authURL(remote)
	if err != nil {
		return nil, err
	}
	r := &Repo{Dir: dir, secret: remote.Password}
	if remote.SSHKeyPath != "" {
		r.env = append(r.env, "GIT_SSH_COMMAND=ssh -i "+remote.SSHKeyPath+" -o IdentitiesOnly=yes")
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return r, r.Fetch(ctx)
	}
	// A previous clone may have failed halfway; start over.
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if _, err := r.git(ctx, filepath.Dir(dir), "clone", "--quiet", cloneURL, dir); err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{{"user.name", CommitName}, {"user.email", CommitEmail}} {
		if _, err := r.run(ctx, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// authURL embeds https credentials in the URL. The checkout keeps them in its
// .git/config so the agent can push too (it has full access by design).
func authURL(remote *forge.GitRemote) (string, error) {
	if remote.Password == "" || !strings.HasPrefix(remote.URL, "http") {
		return remote.URL, nil
	}
	u, err := url.Parse(remote.URL)
	if err != nil {
		return "", fmt.Errorf("remote url: %w", err)
	}
	user := remote.Username
	if user == "" {
		user = "oauth2"
	}
	u.User = url.UserPassword(user, remote.Password)
	return u.String(), nil
}

// Fetch updates all remote-tracking branches, pruning deleted ones.
func (r *Repo) Fetch(ctx context.Context) error {
	_, err := r.run(ctx, "fetch", "--quiet", "--prune", "origin")
	return err
}

// RemoteBranchExists reports whether origin has branch, as of the last fetch.
func (r *Repo) RemoteBranchExists(ctx context.Context, branch string) (bool, error) {
	_, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false, nil
	}
	return err == nil, err
}

// CheckoutBranch creates (or resets) branch at start and checks it out,
// without pushing it. Its upstream is set to the branch of the same name on
// origin, which may not exist yet, so a plain `git push` creates it there and
// can never land on start's branch.
func (r *Repo) CheckoutBranch(ctx context.Context, branch, start string) error {
	if _, err := r.run(ctx, "checkout", "--quiet", "--no-track", "-B", branch, start); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"branch." + branch + ".remote", "origin"},
		{"branch." + branch + ".merge", "refs/heads/" + branch},
	} {
		if _, err := r.run(ctx, "config", kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}

// Push pushes branch to origin and sets it as upstream.
func (r *Repo) Push(ctx context.Context, branch string) error {
	_, err := r.run(ctx, "push", "--quiet", "-u", "origin", branch)
	return err
}

// DeleteRemoteBranch deletes branch on origin; a missing branch is not an error.
func (r *Repo) DeleteRemoteBranch(ctx context.Context, branch string) error {
	ok, err := r.RemoteBranchExists(ctx, branch)
	if err != nil || !ok {
		return err
	}
	_, err = r.run(ctx, "push", "--quiet", "origin", "--delete", branch)
	return err
}

// Dirty reports whether the working tree has uncommitted changes, including
// untracked files.
func (r *Repo) Dirty(ctx context.Context) (bool, error) {
	out, err := r.run(ctx, "status", "--porcelain")
	return out != "", err
}

// CommitAll commits every change in the working tree. It reports false if
// there was nothing to commit.
func (r *Repo) CommitAll(ctx context.Context, message string) (bool, error) {
	dirty, err := r.Dirty(ctx)
	if err != nil || !dirty {
		return false, err
	}
	if _, err := r.run(ctx, "add", "--all"); err != nil {
		return false, err
	}
	_, err = r.run(ctx, "commit", "--quiet", "--message", message)
	return err == nil, err
}

// Head returns the commit HEAD points at.
func (r *Repo) Head(ctx context.Context) (string, error) {
	return r.run(ctx, "rev-parse", "HEAD")
}

// IsPushed reports whether HEAD exists on origin, i.e. is contained in at
// least one remote branch after a fresh fetch.
func (r *Repo) IsPushed(ctx context.Context) (bool, error) {
	if err := r.Fetch(ctx); err != nil {
		return false, err
	}
	out, err := r.run(ctx, "branch", "--remotes", "--contains", "HEAD")
	return out != "", err
}

// DiffersFrom reports whether HEAD's tree differs from ref's tree. Commits
// that cancel each other out count as no difference.
func (r *Repo) DiffersFrom(ctx context.Context, ref string) (bool, error) {
	_, err := r.run(ctx, "diff", "--quiet", ref, "HEAD")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// Worktree makes dir a clean, detached checkout of commit, creating it as a
// linked worktree on first use. Ignored files (dependency caches, build
// output) are kept between calls so repeated deploys stay fast.
func (r *Repo) Worktree(ctx context.Context, dir, commit string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return err
		}
		_, err := r.run(ctx, "worktree", "add", "--quiet", "--detach", dir, commit)
		return err
	}
	if _, err := r.git(ctx, dir, "checkout", "--quiet", "--force", "--detach", commit); err != nil {
		return err
	}
	_, err := r.git(ctx, dir, "clean", "--quiet", "-fd")
	return err
}

func (r *Repo) run(ctx context.Context, args ...string) (string, error) {
	return r.git(ctx, r.Dir, args...)
}

func (r *Repo) git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Env = append(cmd.Env, r.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if r.secret != "" {
			msg = strings.ReplaceAll(msg, r.secret, "***")
		}
		if msg == "" {
			return "", fmt.Errorf("git %s: %w", args[0], err)
		}
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}
