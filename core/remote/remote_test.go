package remote_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tight-Line/skrat/core/remote"
	"github.com/Tight-Line/skrat/core/remote/remotetest"
	"github.com/Tight-Line/skrat/env"
)

func TestRunOutputAndQuoting(t *testing.T) {
	h := &remote.Host{Address: "203.0.113.10", SSH: remotetest.LocalSSH(t)}
	ctx := context.Background()
	out, err := h.Output(ctx, "printf '%s' "+remote.Quote("it's $HOME"))
	if err != nil || out != "it's $HOME" {
		t.Fatalf("output %q, %v", out, err)
	}
	err = h.Run(ctx, "echo first; echo boom >&2; exit 3", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("a failure must carry the output: %v", err)
	}
}

func TestWriteFileAndUpload(t *testing.T) {
	h := &remote.Host{Address: "203.0.113.10", SSH: remotetest.LocalSSH(t)}
	ctx := context.Background()
	dest := t.TempDir()

	if err := h.WriteFile(ctx, filepath.Join(dest, "conf", "a.env"), []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(dest, "conf", "a.env")); string(data) != "A=1\n" {
		t.Errorf("written %q", data)
	}
	if info, _ := os.Stat(filepath.Join(dest, "conf", "a.env")); info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode())
	}

	src := t.TempDir()
	write := func(p, content string, mode os.FileMode) {
		os.MkdirAll(filepath.Dir(filepath.Join(src, p)), 0o755)
		if err := os.WriteFile(filepath.Join(src, p), []byte(content), mode); err != nil {
			t.Fatal(err)
		}
	}
	write("app/main.php", "<?php\n", 0o644)
	write("run.sh", "#!/bin/sh\n", 0o755)
	write(".git/HEAD", "ref\n", 0o644)
	os.Symlink("app/main.php", filepath.Join(src, "link.php"))

	app := filepath.Join(dest, "app")
	write("stale-marker", "", 0o644)
	if err := h.Upload(ctx, src, app, ".git"); err != nil {
		t.Fatal(err)
	}
	os.Remove(filepath.Join(src, "stale-marker"))
	if err := h.Upload(ctx, src, app, ".git"); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(filepath.Join(app, "app/main.php")); string(data) != "<?php\n" {
		t.Errorf("file content %q", data)
	}
	if info, _ := os.Stat(filepath.Join(app, "run.sh")); info.Mode().Perm()&0o100 == 0 {
		t.Error("executable bit lost")
	}
	if target, _ := os.Readlink(filepath.Join(app, "link.php")); target != "app/main.php" {
		t.Errorf("symlink %q", target)
	}
	if _, err := os.Stat(filepath.Join(app, ".git")); !os.IsNotExist(err) {
		t.Error(".git must be excluded")
	}
	if _, err := os.Stat(filepath.Join(app, "stale-marker")); !os.IsNotExist(err) {
		t.Error("a second upload must replace the tree, not merge into it")
	}
}

func TestFromEnv(t *testing.T) {
	if _, err := remote.FromEnv(env.Host{Name: "main", Address: "x"}); err == nil {
		t.Error("a host without SSH access must be refused")
	}
	h, err := remote.FromEnv(env.Host{Address: "x", SSH: &env.SSHAccess{User: "root", Port: 22, KeyPath: "/k"}})
	if err != nil || h.User != "root" || h.KeyPath != "/k" {
		t.Fatalf("%+v %v", h, err)
	}
}
