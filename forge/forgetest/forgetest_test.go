package forgetest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Tight-Line/heinzel/forge"
)

func TestFakeConforms(t *testing.T) {
	f, err := NewFake(filepath.Join(t.TempDir(), "app.git"), "main", map[string]string{"README.md": "hi\n"})
	if err != nil {
		t.Fatal(err)
	}
	Run(t, f)
}

func TestFakeRefusesUnpushedBranch(t *testing.T) {
	f, _ := NewFake(filepath.Join(t.TempDir(), "app.git"), "main", nil)
	_, err := f.OpenChangeRequest(context.Background(), forge.ChangeRequestInput{SourceBranch: "nope", TargetBranch: "main"})
	if err == nil {
		t.Fatal("expected an error for a branch that was never pushed")
	}
}
