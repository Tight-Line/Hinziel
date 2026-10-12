package hostinstall

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestEnvFile(t *testing.T) {
	dir := t.TempDir()
	key := filepath.Join(dir, "id_ed25519")
	os.WriteFile(key, []byte("private"), 0o600)
	os.WriteFile(key+".pub", []byte("public"), 0o644)
	cert := filepath.Join(dir, "sub", "id_ed25519") // same name, other directory
	os.MkdirAll(filepath.Dir(cert), 0o755)
	os.WriteFile(cert, []byte("cert"), 0o600)
	env := filepath.Join(dir, "factory.env")
	os.WriteFile(env, []byte(`# a comment

TOKEN=abc=def
export QUOTED="two words"
SINGLE='x'
SSH_KEY=`+key+`
CERT=`+cert+`
MISSING_FILE=/nonexistent/file
EMPTY=
`), 0o600)

	vars, err := readEnvFile(env)
	if err != nil {
		t.Fatal(err)
	}
	vars, files := relocate(vars, "/etc/hinziel/shop/files")
	got, err := format(vars)
	if err != nil {
		t.Fatal(err)
	}
	want := `TOKEN=abc=def
QUOTED=two words
SINGLE=x
SSH_KEY=/etc/hinziel/shop/files/id_ed25519
CERT=/etc/hinziel/shop/files/2-id_ed25519
MISSING_FILE=/nonexistent/file
EMPTY=
`
	if got != want {
		t.Errorf("env file\n got:\n%s\nwant:\n%s", got, want)
	}
	wantFiles := []file{
		{key, "/etc/hinziel/shop/files/id_ed25519"},
		{key + ".pub", "/etc/hinziel/shop/files/id_ed25519.pub"},
		{cert, "/etc/hinziel/shop/files/2-id_ed25519"},
	}
	if !slices.Equal(files, wantFiles) {
		t.Errorf("files\n got %v\nwant %v", files, wantFiles)
	}

	os.WriteFile(env, []byte("not an assignment\n"), 0o600)
	if _, err := readEnvFile(env); err == nil || !strings.Contains(err.Error(), ":1: not a KEY=VALUE line") {
		t.Errorf("got %v", err)
	}
}

func TestBinaryArch(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("builds Linux binaries with the local toolchain")
	}
	dir := t.TempDir()
	main := filepath.Join(dir, "main.go")
	os.WriteFile(main, []byte("package main\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644)
	build := func(out string, env ...string) {
		t.Helper()
		cmd := exec.Command("go", "build", "-o", out, ".")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), env...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	static := filepath.Join(dir, "static")
	build(static, "CGO_ENABLED=0", "GOOS=linux", "GOARCH=arm64")
	if arch, err := binaryArch(static); err != nil || arch != "arm64" {
		t.Errorf("static arm64: %q, %v", arch, err)
	}
	if _, err := binaryArch(main); err == nil || !strings.Contains(err.Error(), "isn't a Linux executable") {
		t.Errorf("a source file: %v", err)
	}
	// /bin/sh is dynamically linked on every distribution we run on.
	if _, err := binaryArch("/bin/sh"); err == nil || !strings.Contains(err.Error(), "CGO_ENABLED=0") {
		t.Errorf("a dynamic binary: %v", err)
	}
}
