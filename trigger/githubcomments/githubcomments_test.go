package githubcomments

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	ghtracker "github.com/Tight-Line/hinziel/tracker/github"
	"github.com/Tight-Line/hinziel/trigger"
)

// fakeGitHub serves one repository's issue comments and records replies.
// Issue 9 is a pull request.
type fakeGitHub struct {
	mu       sync.Mutex
	comments []map[string]any // oldest first
	replies  map[string][]string
	login    string
}

func (f *fakeGitHub) comment(by, issue, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kind := "issues"
	if issue == "9" {
		kind = "pull"
	}
	id := len(f.comments) + 1
	f.comments = append(f.comments, map[string]any{
		"id": id, "body": body, "user": map[string]any{"login": by},
		"html_url":   "https://github.test/acme/app/" + kind + "/" + issue + "#issuecomment-" + strconv.Itoa(id),
		"issue_url":  "https://api.github.test/repos/acme/app/issues/" + issue,
		"created_at": time.Now().UTC().Format(time.RFC3339),
		"updated_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// edit changes a comment's body, as when someone edits it on GitHub.
func (f *fakeGitHub) edit(i int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.comments[i]["body"] = body
	f.comments[i]["updated_at"] = time.Now().UTC().Format(time.RFC3339)
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer ghp-test" {
		http.Error(w, `{"message":"Bad credentials"}`, http.StatusUnauthorized)
		return
	}
	const repo = "/repos/acme/app"
	switch path := r.URL.Path; {
	case path == "/user":
		json.NewEncoder(w).Encode(map[string]string{"login": f.login})
	case path == repo+"/issues/comments" && r.Method == "GET":
		// Like GitHub: updated at or after `since`, sorted by creation,
		// a page at a time.
		q := r.URL.Query()
		since, _ := time.Parse(time.RFC3339, q.Get("since"))
		var out []map[string]any
		for _, c := range f.comments {
			updated, _ := time.Parse(time.RFC3339, c["updated_at"].(string))
			if !updated.Before(since) {
				out = append(out, c)
			}
		}
		if q.Get("direction") == "desc" {
			slices.Reverse(out)
		}
		per, _ := strconv.Atoi(q.Get("per_page"))
		page, _ := strconv.Atoi(q.Get("page"))
		page = max(page, 1)
		out = out[min((page-1)*per, len(out)):min(page*per, len(out))]
		json.NewEncoder(w).Encode(out)
	case r.Method == "POST" && strings.HasPrefix(path, repo+"/issues/") && strings.HasSuffix(path, "/comments"):
		var body struct{ Body string }
		json.NewDecoder(r.Body).Decode(&body)
		issue := strings.TrimSuffix(strings.TrimPrefix(path, repo+"/issues/"), "/comments")
		f.replies[issue] = append(f.replies[issue], body.Body)
		w.Write([]byte("{}"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGitHub) repliesTo(issue string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.replies[issue])
}

// fakeDispatcher accepts one run per ticket, and tears down environments
// only of tickets in envs.
type fakeDispatcher struct {
	mu        sync.Mutex
	reqs      []run.Request
	envs      map[string]bool
	teardowns []string
	asking    map[string]bool
	answers   []string
}

func (d *fakeDispatcher) Teardown(_ context.Context, ticket tracker.TicketRef, requestedBy string) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.envs[ticket.ID] {
		return "", errors.New(`no environment for #` + ticket.ID)
	}
	delete(d.envs, ticket.ID)
	d.teardowns = append(d.teardowns, ticket.ID+" by "+requestedBy)
	return "hz-" + ticket.ID, nil
}

func (d *fakeDispatcher) Submit(_ context.Context, req run.Request) (run.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, r := range d.reqs {
		if r.Ticket == req.Ticket {
			return "", errors.New("ticket already has run r1 (running)")
		}
	}
	d.reqs = append(d.reqs, req)
	return run.ID("r" + strconv.Itoa(len(d.reqs))), nil
}

func (d *fakeDispatcher) Get(context.Context, run.ID) (*run.Snapshot, error) { return nil, nil }

func (d *fakeDispatcher) Resume(context.Context, tracker.TicketRef, string) (run.ID, error) {
	return "", trigger.ErrNotFailed
}

// Answer takes answers for the tickets in asking.
func (d *fakeDispatcher) Answer(_ context.Context, ticket tracker.TicketRef, text, by string) (run.ID, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.asking[ticket.ID] {
		return "", trigger.ErrNoQuestion
	}
	d.answers = append(d.answers, ticket.ID+": "+text+" by "+by)
	return "r7", nil
}
func (d *fakeDispatcher) Latest(context.Context, tracker.TicketRef) (*run.Snapshot, error) {
	return nil, nil
}

func (d *fakeDispatcher) requests() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, r := range d.reqs {
		out = append(out, r.Ticket.ID+" by "+r.RequestedBy)
	}
	return out
}

func newTest(t *testing.T, opts Options) (*Trigger, *fakeGitHub) {
	f := &fakeGitHub{replies: map[string][]string{}, login: "hinziel"}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Repo, opts.Token, opts.Interval, opts.BaseURL = "acme/app", "ghp-test", 5*time.Millisecond, srv.URL
	return New(opts), f
}

func start(t *testing.T, tr *Trigger, d *fakeDispatcher) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := tr.Start(ctx, d, http.NewServeMux()); err != nil {
		t.Fatal(err)
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCommentsStartRuns(t *testing.T) {
	tr, f := newTest(t, Options{})
	f.comment("ante", "1", "@hinziel implement") // before Start: ignored
	d := &fakeDispatcher{}
	start(t, tr, d)

	f.edit(0, "@hinziel implement, please") // an edit isn't a new request
	f.comment("ante", "2", "@hinziel implement")
	f.comment("ante", "3", "we should implement this later")                       // no mention
	f.comment("hinziel", "4", "On it, @hinziel implement\n\n"+ghtracker.Signature) // Hinziel's own
	f.comment("ante", "5", "@Hinziel please IMPLEMENT this, thanks")               // any case
	f.comment("ante", "6", "@hinziel-bot implement")                               // another user
	f.comment("ante", "7", "@hinziel implementation notes")                        // not the word
	f.comment("ante", "9", "@hinziel implement")                                   // a pull request
	f.comment("hinziel", "8", "@hinziel implement")                                // a person with the factory's token
	f.comment("luka", "2", "@hinziel implement")                                   // 2 already has a run

	eventually(t, "the second reply on #2", func() bool { return len(f.repliesTo("2")) == 2 })
	eventually(t, "a reply on #8", func() bool { return len(f.repliesTo("8")) == 1 })

	if want := []string{"2 by github:ante", "5 by github:ante", "8 by github:hinziel"}; !slices.Equal(d.requests(), want) {
		t.Errorf("runs requested: %v, want %v", d.requests(), want)
	}
	r := f.repliesTo("2")
	if r[0] != "On it, @ante: started run `r1`.\n\n"+ghtracker.Signature || !strings.Contains(r[1], "@luka, I couldn't start a run: ticket already has run") {
		t.Errorf("replies on #2: %q", r)
	}
	for _, issue := range []string{"1", "3", "4", "6", "7", "9"} {
		if r := f.repliesTo(issue); len(r) > 0 {
			t.Errorf("#%s must be left alone, got %q", issue, r)
		}
	}
}

func TestMentionOption(t *testing.T) {
	tr, f := newTest(t, Options{Mention: "@sfplay"})
	d := &fakeDispatcher{}
	start(t, tr, d)
	f.comment("ante", "1", "@hinziel implement")
	f.comment("ante", "2", "@sfplay implement")
	eventually(t, "a reply on #2", func() bool { return len(f.repliesTo("2")) == 1 })
	if want := []string{"2 by github:ante"}; !slices.Equal(d.requests(), want) {
		t.Errorf("runs requested: %v, want %v", d.requests(), want)
	}
}

func TestCommentsTearDown(t *testing.T) {
	tr, f := newTest(t, Options{})
	d := &fakeDispatcher{envs: map[string]bool{"1": true}}
	start(t, tr, d)

	f.comment("ante", "1", "@hinziel teardown")
	f.comment("ante", "2", "@hinziel Teardown please")        // no environment
	f.comment("ante", "3", "@hinziel implement or teardown?") // both words
	f.comment("ante", "4", "time to teardown")                // no mention

	eventually(t, "a reply on #3", func() bool { return len(f.repliesTo("3")) == 1 })
	eventually(t, "a reply on #2", func() bool { return len(f.repliesTo("2")) == 1 })
	d.mu.Lock()
	teardowns := slices.Clone(d.teardowns)
	d.mu.Unlock()
	if !slices.Equal(teardowns, []string{"1 by github:ante"}) {
		t.Errorf("teardowns: %v", teardowns)
	}
	if r := f.repliesTo("1"); len(r) > 0 {
		t.Errorf("a teardown that worked is announced by the notifiers, not a reply: %q", r)
	}
	if r := f.repliesTo("2"); !strings.Contains(r[0], "@ante, I couldn't tear down the environment: no environment for #2") {
		t.Errorf("reply on #2: %q", r)
	}
	if r := f.repliesTo("3"); !strings.Contains(r[0], "should I implement or teardown?") {
		t.Errorf("reply on #3: %q", r)
	}
	if len(d.requests()) != 0 || len(f.repliesTo("4")) != 0 {
		t.Error("no run starts, and #4 is left alone")
	}
}

// A burst of comments longer than a page is read in full, in order.
func TestPollReadsEveryPage(t *testing.T) {
	tr, f := newTest(t, Options{})
	d := &fakeDispatcher{}
	start(t, tr, d)
	n := pageSize + 7
	for i := range n {
		f.comment("ante", strconv.Itoa(100+i), "@hinziel implement")
	}
	eventually(t, "a run per comment", func() bool { return len(d.requests()) == n })
	for i, r := range d.requests() {
		if want := strconv.Itoa(100+i) + " by github:ante"; r != want {
			t.Fatalf("run %d is %s, want %s: comments must be handled oldest first", i, r, want)
		}
	}
}

func TestCheck(t *testing.T) {
	tr, _ := newTest(t, Options{})
	if err := tr.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr.opts.Repo = ""
	if err := tr.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "not set: Repo") {
		t.Errorf("an empty repository must fail the check: %v", err)
	}
	bad := New(Options{Repo: "acme/app", Token: "wrong", BaseURL: tr.api.BaseURL})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("a bad token must fail the check")
	}
}

func TestCommentsAnswer(t *testing.T) {
	tr, f := newTest(t, Options{})
	d := &fakeDispatcher{asking: map[string]bool{"1": true}}
	start(t, tr, d)

	url := "http://localhost:1717/OauthRedirect?code=abc%3D&state=42"
	f.comment("ante", "1", "@hinziel "+url)
	f.comment("ante", "2", "@hinziel thanks for the help!") // nothing asked: only talk
	f.comment("ante", "1", "here it is: "+url)              // no mention

	eventually(t, "a reply on #1", func() bool { return len(f.repliesTo("1")) == 1 })
	f.comment("ante", "3", "@hinziel implement") // handled after the others
	eventually(t, "a reply on #3", func() bool { return len(f.repliesTo("3")) == 1 })

	d.mu.Lock()
	answers := slices.Clone(d.answers)
	d.mu.Unlock()
	if want := []string{"1: " + url + " by github:ante"}; !slices.Equal(answers, want) {
		t.Errorf("answers: %q, want %q", answers, want)
	}
	if r := f.repliesTo("1"); !strings.HasPrefix(r[0], "Thanks, @ante: run `r7` continues.") {
		t.Errorf("reply on #1: %q", r)
	}
	if r := f.repliesTo("2"); len(r) > 0 {
		t.Errorf("a mention with nothing asked gets no reply: %q", r)
	}
}
