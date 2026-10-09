package gitlab

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

	"github.com/Tight-Line/skrat/core"
	"github.com/Tight-Line/skrat/forge"
	"github.com/Tight-Line/skrat/forge/forgetest"
	"github.com/Tight-Line/skrat/internal/gittest"
)

// fakeGitLab serves the slice of the GitLab API this package uses, for one
// project whose repository is a local bare repository.
type fakeGitLab struct {
	repo string

	mu    sync.Mutex
	mrs   []map[string]any
	notes map[int][]string
	posts int
}

func (f *fakeGitLab) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("PRIVATE-TOKEN") != "glpat-test" {
		http.Error(w, `{"message":"401 Unauthorized"}`, http.StatusUnauthorized)
		return
	}
	path := r.URL.EscapedPath()
	const proj = "/api/v4/projects/acme%2Fapp"
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)

	switch {
	case path == proj:
		send(map[string]any{"id": 7, "http_url_to_repo": f.repo, "default_branch": "main"})
	case path == "/api/v4/users":
		if r.URL.Query().Get("username") == "ana" {
			send([]map[string]int{{"id": 42}})
			return
		}
		send([]any{})
	case path == proj+"/merge_requests" && r.Method == "GET":
		out := []map[string]any{}
		for _, mr := range f.mrs {
			if mr["source_branch"] == r.URL.Query().Get("source_branch") && mr["state"] == r.URL.Query().Get("state") {
				out = append(out, mr)
			}
		}
		send(out)
	case path == proj+"/merge_requests" && r.Method == "POST":
		f.posts++
		src := body["source_branch"].(string)
		for _, mr := range f.mrs {
			if mr["source_branch"] == src {
				http.Error(w, `{"message":["Another open merge request already exists for this source branch"]}`, http.StatusConflict)
				return
			}
		}
		if gittest.Git(f.repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+src) != nil {
			http.Error(w, `{"message":"source branch does not exist"}`, http.StatusUnprocessableEntity)
			return
		}
		iid := len(f.mrs) + 1
		mr := map[string]any{
			"iid": iid, "web_url": "https://gitlab.test/acme/app/-/merge_requests/" + strconv.Itoa(iid),
			"source_branch": src, "target_branch": body["target_branch"], "state": "opened",
			"title": body["title"], "description": body["description"], "labels": body["labels"], "reviewer_ids": body["reviewer_ids"],
		}
		f.mrs = append(f.mrs, mr)
		send(mr)
	case strings.HasPrefix(path, proj+"/merge_requests/"):
		rest := strings.Split(strings.TrimPrefix(path, proj+"/merge_requests/"), "/")
		iid, _ := strconv.Atoi(rest[0])
		if iid < 1 || iid > len(f.mrs) {
			http.NotFound(w, r)
			return
		}
		mr := f.mrs[iid-1]
		switch {
		case len(rest) == 2 && rest[1] == "notes" && r.Method == "POST":
			f.notes[iid] = append(f.notes[iid], body["body"].(string))
			send(map[string]any{})
		case len(rest) == 1 && r.Method == "PUT":
			mr["title"], mr["description"] = body["title"], body["description"]
			send(mr)
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func newTestForge(t *testing.T, opts Options) (*Forge, *fakeGitLab) {
	repo := filepath.Join(t.TempDir(), "app.git")
	if _, err := gittest.NewBare(repo, "main", map[string]string{"README.md": "app\n"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeGitLab{repo: repo, notes: map[int][]string{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Project, opts.Token, opts.BaseURL = "acme/app", "glpat-test", srv.URL
	return New(opts), f
}

func TestConformance(t *testing.T) {
	fg, _ := newTestForge(t, Options{})
	forgetest.Run(t, fg)
}

func TestOpenChangeRequest(t *testing.T) {
	fg, fake := newTestForge(t, Options{Labels: []string{"skrat"}})
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
	mr := fake.mrs[0]
	if mr["title"] != "Draft: T-1: Export" || mr["labels"] != "skrat,preview" || mr["description"] != "Implements T-1." {
		t.Errorf("merge request %+v", mr)
	}
	if ids, _ := json.Marshal(mr["reviewer_ids"]); string(ids) != "[42]" {
		t.Errorf("reviewers %s", ids)
	}
	if cr.ID != "1" || cr.URL != "https://gitlab.test/acme/app/-/merge_requests/1" {
		t.Errorf("change request %+v", cr)
	}

	again, err := fg.OpenChangeRequest(ctx, in)
	if err != nil || again.ID != "1" || fake.posts != 1 {
		t.Errorf("second open: %+v, %v, %d posts", again, err, fake.posts)
	}

	in.Reviewers = []string{"nobody"}
	in.SourceBranch = "main"
	if _, err := fg.OpenChangeRequest(ctx, in); err == nil || !strings.Contains(err.Error(), "nobody") {
		t.Errorf("an unknown reviewer must be an error: %v", err)
	}
}

func TestConflictResolvesToExisting(t *testing.T) {
	fg, fake := newTestForge(t, Options{})
	gittest.Git(fake.repo, "branch", "feature", "main")
	// The MR exists but the list endpoint misses it the first time, as when
	// two attempts race; the 409 must be resolved by looking again.
	fake.mrs = append(fake.mrs, map[string]any{"iid": 1, "web_url": "u", "source_branch": "feature", "target_branch": "main", "state": "opened"})
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/merge_requests") && calls == 0 {
			calls++
			w.Write([]byte("[]"))
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	fg = New(Options{Project: "acme/app", Token: "glpat-test", BaseURL: srv.URL})
	cr, err := fg.OpenChangeRequest(context.Background(), forge.ChangeRequestInput{SourceBranch: "feature", TargetBranch: "main", Title: "x"})
	if err != nil || cr.ID != "1" {
		t.Fatalf("%+v %v", cr, err)
	}
}

func TestCheck(t *testing.T) {
	fg, _ := newTestForge(t, Options{})
	if err := fg.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{Project: "acme/app", Token: "wrong", BaseURL: strings.TrimSuffix(fg.api.BaseURL, "/api/v4")})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("a bad token must fail the check")
	}
}

// TestIntegration runs the conformance suite against a real GitLab project. It
// pushes a branch and opens a draft merge request, so use a sandbox project.
func TestIntegration(t *testing.T) {
	if os.Getenv("SKRAT_IT_GITLAB") == "" {
		t.Skip("set SKRAT_IT_GITLAB=1, GITLAB_TOKEN and GITLAB_TEST_PROJECT (and GITLAB_URL for self-managed)")
	}
	forgetest.Run(t, New(Options{Project: os.Getenv("GITLAB_TEST_PROJECT"), Token: os.Getenv("GITLAB_TOKEN"), BaseURL: os.Getenv("GITLAB_URL")}))
}
