package local

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/forge/forgetest"
	"github.com/Tight-Line/hinziel/internal/gittest"
)

// newRepo creates a bare repository with a main branch and a pushed
// feature branch.
func newRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "app.git")
	if _, err := gittest.NewBare(repo, "main", map[string]string{"README.md": "hi\n"}); err != nil {
		t.Fatal(err)
	}
	if err := gittest.Git(repo, "branch", "hinziel/t-1", "main"); err != nil {
		t.Fatal(err)
	}
	return repo
}

var in = forge.ChangeRequestInput{
	SourceBranch: "hinziel/t-1",
	TargetBranch: "main",
	Title:        "T-1: Mark churned accounts",
	Description:  core.Message{Body: "Cancelled subscriptions now mark the account **Churned**."},
}

func TestConformance(t *testing.T) {
	forgetest.Run(t, New(Options{Repo: newRepo(t)}))
}

func TestMergeRequestsAreFilesThatOutliveTheForge(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	cr, err := New(Options{Repo: repo}).OpenChangeRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	dir := strings.TrimSuffix(repo, ".git") + ".merge-requests"
	if cr.ID != "1" || cr.URL != "file://"+filepath.Join(dir, "1", "description.md") {
		t.Errorf("change request %+v", cr)
	}

	// Another process (a resumed run) finds it.
	f := New(Options{Repo: repo})
	again, err := f.OpenChangeRequest(ctx, in)
	if err != nil || again.ID != "1" {
		t.Fatalf("got %+v, %v; want merge request 1 again", again, err)
	}
	if err := f.CommentChangeRequest(ctx, again, core.Message{Title: "Deployed", Body: "To the scratch org."}); err != nil {
		t.Fatal(err)
	}
	updated := in
	updated.Draft = true
	updated.Description = core.Message{Body: "Rewritten."}
	if err := f.UpdateChangeRequest(ctx, again, updated); err != nil {
		t.Fatal(err)
	}

	desc, _ := os.ReadFile(filepath.Join(dir, "1", "description.md"))
	if want := "# Draft: T-1: Mark churned accounts\n\n`hinziel/t-1` → `main`\n\nRewritten.\n"; string(desc) != want {
		t.Errorf("description.md:\n%s\nwant\n%s", desc, want)
	}
	comments, _ := os.ReadFile(filepath.Join(dir, "1", "comments.md"))
	if !strings.Contains(string(comments), "**Deployed**\n\nTo the scratch org.") {
		t.Errorf("comments.md:\n%s", comments)
	}
	var mr mergeRequest
	data, _ := os.ReadFile(filepath.Join(dir, "1", "merge-request.json"))
	if err := json.Unmarshal(data, &mr); err != nil || mr.State != "open" || !mr.Draft || mr.SourceBranch != "hinziel/t-1" {
		t.Errorf("merge-request.json: %+v, %v", mr, err)
	}
}

func TestAClosedMergeRequestIsntReused(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	mrs := filepath.Join(t.TempDir(), "reviews")
	f := New(Options{Repo: repo, MergeRequests: mrs})
	if _, err := f.OpenChangeRequest(ctx, in); err != nil {
		t.Fatal(err)
	}
	meta := filepath.Join(mrs, "1", "merge-request.json")
	data, _ := os.ReadFile(meta)
	os.WriteFile(meta, []byte(strings.Replace(string(data), `"state": "open"`, `"state": "merged"`, 1)), 0o644)

	cr, err := f.OpenChangeRequest(ctx, in)
	if err != nil || cr.ID != "2" {
		t.Errorf("got %+v, %v; want a new merge request 2", cr, err)
	}
}

func TestUnpushedBranchIsRefused(t *testing.T) {
	f := New(Options{Repo: newRepo(t)})
	other := in
	other.SourceBranch = "never-pushed"
	if _, err := f.OpenChangeRequest(context.Background(), other); err == nil {
		t.Error("expected an error for a branch that isn't in the repository")
	}
}

func TestCheck(t *testing.T) {
	ctx := context.Background()
	repo := newRepo(t)
	if err := New(Options{Repo: repo}).Check(ctx); err != nil {
		t.Fatal(err)
	}
	if err := New(Options{Repo: repo, DefaultBranch: "develop"}).Check(ctx); err == nil || !strings.Contains(err.Error(), "no branch develop") {
		t.Errorf("a missing default branch: %v", err)
	}
	notBare := t.TempDir()
	gittest.Git(notBare, "init", "--quiet")
	if err := New(Options{Repo: notBare}).Check(ctx); err == nil || !strings.Contains(err.Error(), "not a bare git repository") {
		t.Errorf("a non-bare repository: %v", err)
	}
	if err := New(Options{}).Check(ctx); err == nil || !strings.Contains(err.Error(), "Repo") {
		t.Errorf("no Repo: %v", err)
	}
}
