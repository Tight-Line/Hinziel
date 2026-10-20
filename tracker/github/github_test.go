package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trackertest"
)

// fakeGitHub serves the slice of the GitHub API this package uses, for one
// repository: issue 1, pull request 2 and issue 3, which is closed.
type fakeGitHub struct {
	mu       sync.Mutex
	issues   map[int]map[string]any
	comments map[int][]map[string]any
	labels   []string // the repository's
	noIssues bool
}

func newFakeGitHub() *fakeGitHub {
	f := &fakeGitHub{
		issues: map[int]map[string]any{
			1: {"number": 1, "title": "Add CSV export", "body": "Users want **CSV**.", "state": "open",
				"html_url": "https://github.test/acme/app/issues/1", "labels": []any{map[string]any{"name": "backend"}}},
			2: {"number": 2, "title": "A pull request", "state": "open", "html_url": "https://github.test/acme/app/pull/2",
				"labels": []any{}, "pull_request": map[string]any{"url": "x"}},
			3: {"number": 3, "title": "Old", "state": "closed", "html_url": "https://github.test/acme/app/issues/3", "labels": []any{}},
		},
		comments: map[int][]map[string]any{},
		labels:   []string{"backend", "hinziel: working", "hinziel: review", "blocked", "done"},
	}
	for i := range 150 {
		f.comments[1] = append(f.comments[1], map[string]any{
			"user": map[string]any{"login": "ana"}, "body": "comment " + strconv.Itoa(i), "created_at": "2026-10-20T10:00:00Z",
		})
	}
	return f
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
	page := func(items []map[string]any) []map[string]any {
		per, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		lo, hi := min((p-1)*per, len(items)), min(p*per, len(items))
		return items[lo:hi]
	}

	switch path := r.URL.Path; {
	case path == repo:
		send(map[string]any{"has_issues": !f.noIssues})
	case path == repo+"/labels":
		var out []map[string]any
		for _, l := range f.labels {
			out = append(out, map[string]any{"name": l})
		}
		send(page(out))
	case strings.HasPrefix(path, repo+"/issues/"):
		rest := strings.SplitN(strings.TrimPrefix(path, repo+"/issues/"), "/", 3)
		n, _ := strconv.Atoi(rest[0])
		is, ok := f.issues[n]
		if !ok {
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
			return
		}
		switch {
		case len(rest) == 1 && r.Method == "GET":
			send(is)
		case len(rest) == 2 && rest[1] == "comments" && r.Method == "GET":
			send(page(f.comments[n]))
		case len(rest) == 2 && rest[1] == "comments" && r.Method == "POST":
			f.comments[n] = append(f.comments[n], map[string]any{"user": map[string]any{"login": "hinziel"}, "body": body["body"]})
			send(map[string]any{})
		case len(rest) == 2 && rest[1] == "labels" && r.Method == "POST":
			for _, l := range body["labels"].([]any) {
				is["labels"] = append(is["labels"].([]any), map[string]any{"name": l})
			}
			send(is["labels"])
		case len(rest) == 3 && rest[1] == "labels" && r.Method == "DELETE":
			labels := is["labels"].([]any)
			i := slices.IndexFunc(labels, func(l any) bool { return l.(map[string]any)["name"] == rest[2] })
			if i < 0 {
				http.Error(w, `{"message":"Label does not exist"}`, http.StatusNotFound)
				return
			}
			is["labels"] = slices.Delete(labels, i, i+1)
			send(is["labels"])
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) issueLabels(n int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, l := range f.issues[n]["labels"].([]any) {
		out = append(out, l.(map[string]any)["name"].(string))
	}
	return out
}

var stages = map[tracker.Stage]string{
	tracker.StageInProgress: "hinziel: working",
	tracker.StageInReview:   "hinziel: review",
	tracker.StageBlocked:    "blocked",
	tracker.StageDone:       "done",
}

func newTestTracker(t *testing.T, opts Options) (*Tracker, *fakeGitHub) {
	f := newFakeGitHub()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Repo, opts.Token, opts.BaseURL = "acme/app", "ghp-test", srv.URL
	return New(opts), f
}

func TestConformance(t *testing.T) {
	tr, _ := newTestTracker(t, Options{Stages: stages})
	trackertest.Run(t, tr, "1")
}

func TestFetch(t *testing.T) {
	tr, _ := newTestTracker(t, Options{})
	ctx := context.Background()
	for _, id := range []string{"1", "#1", " https://github.com/acme/app/issues/1 "} {
		tk, err := tr.Fetch(ctx, id)
		if err != nil {
			t.Fatalf("Fetch(%q): %v", id, err)
		}
		if tk.Ref.ID != "1" || tk.Key != "#1" || tk.Title != "Add CSV export" || tk.Description != "Users want **CSV**." ||
			tk.URL != "https://github.test/acme/app/issues/1" || !slices.Equal(tk.Labels, []string{"backend"}) {
			t.Errorf("Fetch(%q) = %+v", id, tk)
		}
		if len(tk.Comments) != 150 || tk.Comments[0].Body != "comment 0" || tk.Comments[149].Author != "ana" || tk.Comments[0].At.IsZero() {
			t.Errorf("Fetch(%q): %d comments, want all 150 in order", id, len(tk.Comments))
		}
	}
	for _, id := range []string{"2", "https://github.com/other/app/issues/1", "abc", "0", "9"} {
		if _, err := tr.Fetch(ctx, id); err == nil {
			t.Errorf("Fetch(%q) must fail", id)
		}
	}
}

func TestCommentIsTruncated(t *testing.T) {
	tr, fake := newTestTracker(t, Options{})
	if err := tr.Comment(context.Background(), "#3", core.Message{Body: strings.Repeat("é", maxComment+10)}); err != nil {
		t.Fatal(err)
	}
	got := fake.comments[3][0]["body"].(string)
	if n := len([]rune(got)); n != maxComment || !strings.HasSuffix(got, "…(truncated)") {
		t.Errorf("comment of %d characters, ending %q", n, got[len(got)-20:])
	}
}

func TestMoveTo(t *testing.T) {
	tr, fake := newTestTracker(t, Options{Stages: stages})
	ctx := context.Background()
	for _, s := range []tracker.Stage{tracker.StageInProgress, tracker.StageBlocked, tracker.StageInProgress, tracker.StageInReview} {
		if err := tr.MoveTo(ctx, "1", s); err != nil {
			t.Fatalf("MoveTo(%s): %v", s, err)
		}
	}
	if got := fake.issueLabels(1); !slices.Equal(got, []string{"backend", "hinziel: review"}) {
		t.Errorf("labels %q", got)
	}
	if err := tr.MoveTo(ctx, "1", tracker.StageInReview); err != nil {
		t.Fatal(err)
	}
	if got := fake.issueLabels(1); len(got) != 2 {
		t.Errorf("moving to the same stage again must not add a label: %q", got)
	}
}

func TestDone(t *testing.T) {
	tr, _ := newTestTracker(t, Options{Stages: stages})
	ctx := context.Background()
	for id, want := range map[string]bool{"1": false, "3": true, "9": true} {
		if done, err := tr.Done(ctx, id); err != nil || done != want {
			t.Errorf("Done(%s) = %v, %v; want %v", id, done, err, want)
		}
	}
	tr.MoveTo(ctx, "1", tracker.StageDone)
	if done, err := tr.Done(ctx, "1"); err != nil || !done {
		t.Errorf("an issue with the done label must be done: %v, %v", done, err)
	}
}

func TestCheck(t *testing.T) {
	tr, fake := newTestTracker(t, Options{Stages: stages})
	ctx := context.Background()
	if err := tr.Check(ctx); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{Repo: "acme/app", Token: "wrong", BaseURL: tr.api.BaseURL})
	if err := bad.Check(ctx); err == nil {
		t.Error("a bad token must fail the check")
	}
	missing := New(Options{Repo: "acme/app", Token: "ghp-test", BaseURL: tr.api.BaseURL,
		Stages: map[tracker.Stage]string{tracker.StageBlocked: "Stuck"}})
	if err := missing.Check(ctx); err == nil || !strings.Contains(err.Error(), `"Stuck"`) || !strings.Contains(err.Error(), "blocked") {
		t.Errorf("a missing label must be named, with the repository's: %v", err)
	}
	fake.noIssues = true
	if err := tr.Check(ctx); err == nil || !strings.Contains(err.Error(), "issues turned off") {
		t.Errorf("issues turned off must fail the check: %v", err)
	}
}

// TestIntegration runs the conformance suite against a real GitHub issue. It
// posts a comment on it, so use a sandbox repository.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_GITHUB") == "" {
		t.Skip("set HINZIEL_IT_GITHUB=1, GITHUB_TOKEN, GITHUB_TEST_REPO and GITHUB_TEST_ISSUE (and GITHUB_API_URL for Enterprise Server)")
	}
	tr := New(Options{Repo: os.Getenv("GITHUB_TEST_REPO"), Token: os.Getenv("GITHUB_TOKEN"), BaseURL: os.Getenv("GITHUB_API_URL")})
	trackertest.Run(t, tr, os.Getenv("GITHUB_TEST_ISSUE"))
}
