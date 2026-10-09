package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeedTools(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o755)
	t.Setenv("PATH", dir)
	if err := NeedTools("git"); err != nil {
		t.Fatal(err)
	}
	err := NeedTools("git", "ssh", "npx")
	if err == nil || !strings.HasSuffix(err.Error(), ": ssh, npx") {
		t.Fatalf("must list every missing tool: %v", err)
	}
}
