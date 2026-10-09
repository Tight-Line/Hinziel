package deploytest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Tight-Line/skrat/deploy"
	"github.com/Tight-Line/skrat/env"
)

func TestFakesConform(t *testing.T) {
	e := &env.Environment{Name: "sk-test"}
	src := &deploy.Source{Repo: "app", Workdir: t.TempDir(), Branch: "skrat/t-1", Commit: "abc123"}
	t.Run("Fake", func(t *testing.T) { Run(t, &Fake{}, e, src) })
	t.Run("FakeRetriever", func(t *testing.T) { Run(t, &FakeRetriever{}, e, src) })
}

func TestFakeRetrieverWritesPendingOnce(t *testing.T) {
	f := &FakeRetriever{}
	f.SetPending("flows/Approval.flow-meta.xml", "<Flow/>")
	dir := t.TempDir()
	f.Retrieve(t.Context(), &env.Environment{}, dir)
	if _, err := os.Stat(filepath.Join(dir, "flows/Approval.flow-meta.xml")); err != nil {
		t.Fatal(err)
	}
	os.RemoveAll(filepath.Join(dir, "flows"))
	f.Retrieve(t.Context(), &env.Environment{}, dir)
	if _, err := os.Stat(filepath.Join(dir, "flows")); !os.IsNotExist(err) {
		t.Fatal("pending change was retrieved twice")
	}
}
