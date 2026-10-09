package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/internal/gittest"
)

func setup(t *testing.T) (*Repo, string) {
	t.Helper()
	tmp := t.TempDir()
	bare, err := gittest.NewBare(filepath.Join(tmp, "remote.git"), "main", map[string]string{"README.md": "hello\n"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Clone(context.Background(), &forge.GitRemote{URL: bare}, filepath.Join(tmp, "work", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	return r, bare
}

func write(t *testing.T, r *Repo, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(r.Dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBranchCommitPushFlow(t *testing.T) {
	ctx := context.Background()
	r, bare := setup(t)

	if err := r.CheckoutBranch(ctx, "heinzel/t-1", "origin/main"); err != nil {
		t.Fatal(err)
	}
	if err := r.Push(ctx, "heinzel/t-1"); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.RemoteBranchExists(ctx, "heinzel/t-1"); err != nil || !ok {
		t.Fatalf("remote branch: %v %v", ok, err)
	}
	if differs, err := r.DiffersFrom(ctx, "origin/main"); err != nil || differs {
		t.Fatalf("fresh branch differs: %v %v", differs, err)
	}

	write(t, r, "feature.txt", "new\n")
	if dirty, _ := r.Dirty(ctx); !dirty {
		t.Fatal("expected dirty tree")
	}
	if ok, err := r.CommitAll(ctx, "add feature"); err != nil || !ok {
		t.Fatalf("commit: %v %v", ok, err)
	}
	if ok, _ := r.CommitAll(ctx, "nothing"); ok {
		t.Fatal("second commit should report nothing to commit")
	}
	if pushed, _ := r.IsPushed(ctx); pushed {
		t.Fatal("new commit should not be pushed yet")
	}
	if err := r.Push(ctx, "heinzel/t-1"); err != nil {
		t.Fatal(err)
	}
	if pushed, err := r.IsPushed(ctx); err != nil || !pushed {
		t.Fatalf("pushed: %v %v", pushed, err)
	}
	if differs, _ := r.DiffersFrom(ctx, "origin/main"); !differs {
		t.Fatal("branch with a new file should differ from main")
	}

	head, _ := r.Head(ctx)
	remoteHead, err := gittest.Output(bare, "rev-parse", "heinzel/t-1")
	if err != nil || remoteHead != head {
		t.Fatalf("remote head %q, local %q (%v)", remoteHead, head, err)
	}

	if err := r.DeleteRemoteBranch(ctx, "heinzel/t-1"); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteRemoteBranch(ctx, "heinzel/t-1"); err != nil {
		t.Fatalf("deleting a missing branch: %v", err)
	}
	if ok, _ := r.RemoteBranchExists(ctx, "heinzel/t-1"); ok {
		t.Fatal("branch still exists")
	}
}

func TestCloneIsIdempotent(t *testing.T) {
	r, bare := setup(t)
	write(t, r, "local.txt", "keep me\n")
	again, err := Clone(context.Background(), &forge.GitRemote{URL: bare}, r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(again.Dir, "local.txt")); err != nil {
		t.Fatal("re-cloning an existing checkout must not wipe it")
	}
}

func TestWorktreeIsCleanCheckoutOfCommit(t *testing.T) {
	ctx := context.Background()
	r, _ := setup(t)
	first, _ := r.Head(ctx)
	write(t, r, "second.txt", "2\n")
	r.CommitAll(ctx, "second")
	second, _ := r.Head(ctx)

	wt := filepath.Join(t.TempDir(), "deploy")
	if err := r.Worktree(ctx, wt, second); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(wt, "build-output.tmp"), []byte("x"), 0o644)
	if err := r.Worktree(ctx, wt, first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, "second.txt")); !os.IsNotExist(err) {
		t.Error("worktree still has a file from the later commit")
	}
	if _, err := os.Stat(filepath.Join(wt, "build-output.tmp")); !os.IsNotExist(err) {
		t.Error("worktree kept an untracked file")
	}
}

func TestAuthURLEmbedsToken(t *testing.T) {
	got, err := authURL(&forge.GitRemote{URL: "https://gitlab.com/acme/app.git", Password: "s3cret"})
	if err != nil || got != "https://oauth2:s3cret@gitlab.com/acme/app.git" {
		t.Fatalf("got %q %v", got, err)
	}
	got, _ = authURL(&forge.GitRemote{URL: "git@gitlab.com:acme/app.git", Password: "s3cret"})
	if got != "git@gitlab.com:acme/app.git" {
		t.Fatalf("ssh url changed: %q", got)
	}
}
