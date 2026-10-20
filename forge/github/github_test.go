package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/forge/forgetest"
	"github.com/Tight-Line/hinziel/internal/gittest"
)

// fakeGitHub serves the slice of the GitHub API this package uses, for one
// repository that is a local bare repository.
type fakeGitHub struct {
	repo    string
	noDraft bool // refuse drafts, as on a free plan's private repository
	push    bool

	mu       sync.Mutex
	prs      []map[string]any
	comments map[int][]string
	posts    int
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer ghp-test" {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	const repo = "/repos/acme/app"
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)

	switch path := r.URL.Path; {
	case path == repo:
		send(map[string]any{"clone_url": f.repo, "default_branch": "main", "permissions": map[string]bool{"push": f.push}})
	case path == repo+"/pulls" && r.Method == "GET":
		out := []map[string]any{}
		for _, pr := range f.prs {
			if "acme:"+pr["head"].(map[string]any)["ref"].(string) == r.URL.Query().Get("head") && pr["state"] == r.URL.Query().Get("state") {
				out = append(out, pr)
			}
		}
		send(out)
	case path == repo+"/pulls" && r.Method == "POST":
		f.posts++
		head := body["head"].(string)
		if body["draft"] == true && f.noDraft {
			http.Error(w, `{"message":"Validation Failed","errors":[{"message":"Draft pull requests are not supported in this repository."}]}`, http.StatusUnprocessableEntity)
			return
		}
		for _, pr := range f.prs {
			if pr["head"].(map[string]any)["ref"] == head {
				http.Error(w, `{"message":"Validation Failed","errors":[{"message":"A pull request already exists for acme:`+head+`."}]}`, http.StatusUnprocessableEntity)
				return
			}
		}
		if gittest.Git(f.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+head) != nil {
			http.Error(w, `{"message":"Validation Failed","errors":[{"field":"head","code":"invalid"}]}`, http.StatusUnprocessableEntity)
			return
		}
		n := len(f.prs) + 1
		pr := map[string]any{
			"number": n, "html_url": "https://github.test/acme/app/pull/" + strconv.Itoa(n), "state": "open",
			"head": map[string]any{"ref": head}, "base": map[string]any{"ref": body["base"]},
			"title": body["title"], "body": body["body"], "draft": body["draft"],
		}
		f.prs = append(f.prs, pr)
		send(pr)
	case strings.HasPrefix(path, repo+"/pulls/") || strings.HasPrefix(path, repo+"/issues/"):
		rest := strings.Split(strings.TrimPrefix(strings.TrimPrefix(path, repo+"/pulls/"), repo+"/issues/"), "/")
		n, _ := strconv.Atoi(rest[0])
		if n < 1 || n > len(f.prs) {
			http.NotFound(w, r)
			return
		}
		pr := f.prs[n-1]
		switch {
		case len(rest) == 2 && rest[1] == "comments" && r.Method == "POST":
			f.comments[n] = append(f.comments[n], body["body"].(string))
			send(map[string]any{})
		case len(rest) == 2 && rest[1] == "labels" && r.Method == "POST":
			pr["labels"] = body["labels"]
			send([]any{})
		case len(rest) == 2 && rest[1] == "requested_reviewers" && r.Method == "POST":
			for _, u := range body["reviewers"].([]any) {
				if u != "ana" {
					http.Error(w, `{"message":"Reviews may only be requested from collaborators."}`, http.StatusUnprocessableEntity)
					return
				}
			}
			pr["reviewers"] = body["reviewers"]
			send(pr)
		case len(rest) == 1 && r.Method == "PATCH":
			pr["body"] = body["body"]
			send(pr)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func newTestForge(t *testing.T, opts Options) (*Forge, *fakeGitHub) {
	repo := filepath.Join(t.TempDir(), "app.git")
	if _, err := gittest.NewBare(repo, "main", map[string]string{"README.md": "app\n"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeGitHub{repo: repo, push: true, comments: map[int][]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Repo, opts.Token, opts.BaseURL = "acme/app", "ghp-test", srv.URL
	return New(opts), f
}

func TestConformance(t *testing.T) {
	fg, _ := newTestForge(t, Options{})
	forgetest.Run(t, fg)
}

func TestOpenChangeRequest(t *testing.T) {
	fg, fake := newTestForge(t, Options{Labels: []string{"hinziel"}})
	ctx := context.Background()
	if err := gittest.Git(fake.repo, "branch", "feature", "main"); err != nil {
		t.Fatal(err)
	}
	in := forge.ChangeRequestInput{
		SourceBranch: "feature", TargetBranch: "main", Title: "T-1: Export", Draft: true,
		Description: core.Message{Body: "Implements T-1."}, Labels: []string{"preview"}, Reviewers: []string{"ana"},
	}
	cr, err := fg.OpenChangeRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	pr := fake.prs[0]
	if pr["title"] != "T-1: Export" || pr["draft"] != true || pr["body"] != "Implements T-1." {
		t.Errorf("pull request %+v", pr)
	}
	if labels, _ := json.Marshal(pr["labels"]); string(labels) != `["hinziel","preview"]` {
		t.Errorf("labels %s", labels)
	}
	if reviewers, _ := json.Marshal(pr["reviewers"]); string(reviewers) != `["ana"]` {
		t.Errorf("reviewers %s", reviewers)
	}
	if cr.ID != "1" || cr.URL != "https://github.test/acme/app/pull/1" || cr.SourceBranch != "feature" || cr.TargetBranch != "main" {
		t.Errorf("change request %+v", cr)
	}

	again, err := fg.OpenChangeRequest(ctx, in)
	if err != nil || again.ID != "1" || fake.posts != 1 {
		t.Errorf("second open: %+v, %v, %d posts", again, err, fake.posts)
	}

	gittest.Git(fake.repo, "branch", "other", "main")
	in.SourceBranch, in.Reviewers = "other", []string{"nobody"}
	if _, err := fg.OpenChangeRequest(ctx, in); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("an unknown reviewer must be an error: %v", err)
	}
}

func TestDraftFallsBackToTitle(t *testing.T) {
	fg, fake := newTestForge(t, Options{})
	fake.noDraft = true
	gittest.Git(fake.repo, "branch", "feature", "main")
	in := forge.ChangeRequestInput{SourceBranch: "feature", TargetBranch: "main", Title: "T-1: Export", Draft: true}
	if _, err := fg.OpenChangeRequest(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if pr := fake.prs[0]; pr["title"] != "Draft: T-1: Export" || pr["draft"] != false {
		t.Errorf("pull request %+v", pr)
	}
}

func TestConflictResolvesToExisting(t *testing.T) {
	fg, fake := newTestForge(t, Options{})
	gittest.Git(fake.repo, "branch", "feature", "main")
	// The pull request exists but the list endpoint misses it the first time,
	// as when two attempts race; the 422 must be resolved by looking again.
	fake.prs = append(fake.prs, map[string]any{"number": 1, "html_url": "u", "state": "open",
		"head": map[string]any{"ref": "feature"}, "base": map[string]any{"ref": "main"}})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pulls") && calls == 0 {
			calls++
			w.Write([]byte("[]"))
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	fg = New(Options{Repo: "acme/app", Token: "ghp-test", BaseURL: srv.URL})
	cr, err := fg.OpenChangeRequest(context.Background(), forge.ChangeRequestInput{SourceBranch: "feature", TargetBranch: "main", Title: "x"})
	if err != nil || cr.ID != "1" {
		t.Fatalf("%+v %v", cr, err)
	}
}

func TestCheck(t *testing.T) {
	fg, fake := newTestForge(t, Options{})
	if err := fg.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{Repo: "acme/app", Token: "wrong", BaseURL: fg.api.BaseURL})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("a bad token must fail the check")
	}
	fake.push = false
	readOnly := New(Options{Repo: "acme/app", Token: "ghp-test", BaseURL: fg.api.BaseURL})
	if err := readOnly.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "push") {
		t.Errorf("a token that can't push must fail the check: %v", err)
	}
}

// TestIntegration runs the conformance suite against a real GitHub
// repository. It pushes a branch and opens a pull request, so use a sandbox
// repository.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_GITHUB") == "" {
		t.Skip("set HINZIEL_IT_GITHUB=1, GITHUB_TOKEN and GITHUB_TEST_REPO (and GITHUB_API_URL for Enterprise Server)")
	}
	forgetest.Run(t, New(Options{Repo: os.Getenv("GITHUB_TEST_REPO"), Token: os.Getenv("GITHUB_TOKEN"), BaseURL: os.Getenv("GITHUB_API_URL")}))
}
