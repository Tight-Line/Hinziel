package trello

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trackertest"
)

// fakeTrello serves the slice of the Trello API this package uses.
type fakeTrello struct {
	mu          sync.Mutex
	cards       map[string]map[string]any // by ID
	comments    map[string][]string
	attachments map[string][]map[string]string
	lists       map[string][]list  // by board ID
	labels      map[string][]label // by board ID
}

func newFakeTrello() *fakeTrello {
	f := &fakeTrello{
		cards: map[string]map[string]any{
			"c1": {
				"id": "c1", "name": "Add CSV export", "desc": "Users want **CSV**.",
				"shortLink": "AbCd1234", "shortUrl": "https://trello.com/c/AbCd1234",
				"idBoard": "b1", "idList": "l-todo",
				"labels": []map[string]string{{"name": "backend"}, {"name": "", "color": "red"}},
				"attachments": []map[string]any{
					{"id": "a1", "name": "mock.png", "url": "https://trello.com/1/cards/c1/attachments/a1/download/mock.png", "mimeType": "image/png", "isUpload": true},
					{"id": "a2", "name": "MR", "url": "https://gitlab.test/mr/1", "isUpload": false},
				},
				"checklists": []map[string]any{{"name": "Done when", "checkItems": []map[string]any{
					{"name": "second", "state": "incomplete", "pos": 2},
					{"name": "first", "state": "complete", "pos": 1},
				}}},
				"actions": []map[string]any{
					{"date": "2026-10-09T10:00:00Z", "memberCreator": map[string]string{"fullName": "Ana"}, "data": map[string]string{"text": "newer"}},
					{"date": "2026-10-08T10:00:00Z", "memberCreator": map[string]string{"username": "bo"}, "data": map[string]string{"text": "older"}},
				},
			},
			"c2": {"id": "c2", "name": "Elsewhere", "shortLink": "Zz999999", "idBoard": "b2", "idList": "x"},
		},
		comments:    map[string][]string{},
		attachments: map[string][]map[string]string{},
		lists: map[string][]list{"b1": {
			{"l-new", "New tickets"}, {"l-doing", "  In Progress"}, {"l-review", "Review"},
			{"l-dup1", "Done"}, {"l-dup2", "done"},
		}},
		labels: map[string][]label{"b1": {{ID: "lb-blocked", Name: "Blocked"}, {ID: "lb-review", Name: "Hinziel: review"}, {ID: "lb-red", Color: "red"}}},
	}
	return f
}

func (f *fakeTrello) card(id string) map[string]any {
	for _, c := range f.cards {
		if c["id"] == id || c["shortLink"] == id {
			return c
		}
	}
	return nil
}

func (f *fakeTrello) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != `OAuth oauth_consumer_key="key", oauth_token="token"` {
		http.Error(w, "invalid key", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/1/"), "/")
	send := func(v any) { json.NewEncoder(w).Encode(v) }
	var body map[string]string
	json.NewDecoder(r.Body).Decode(&body)

	switch {
	case parts[0] == "members" && parts[1] == "me":
		send(map[string]string{"username": "hinziel"})
	case parts[0] == "boards" && len(parts) == 2:
		if parts[1] != "b1" && parts[1] != "BoArD111" {
			http.NotFound(w, r)
			return
		}
		send(map[string]string{"id": "b1"})
	case parts[0] == "boards" && parts[2] == "lists":
		send(f.lists[parts[1]])
	case parts[0] == "boards" && parts[2] == "labels":
		send(f.labels[parts[1]])
	case parts[0] == "cards":
		c := f.card(parts[1])
		if c == nil {
			http.Error(w, "The requested resource was not found.", http.StatusNotFound)
			return
		}
		id := c["id"].(string)
		switch {
		case len(parts) == 2 && r.Method == "GET":
			send(c)
		case len(parts) == 2 && r.Method == "PUT":
			c["idList"] = body["idList"]
			send(c)
		case len(parts) == 4 && parts[2] == "actions" && r.Method == "POST":
			f.comments[id] = append(f.comments[id], body["text"])
			send(map[string]string{})
		case parts[2] == "idLabels" && r.Method == "POST":
			c["idLabels"] = append(idLabels(c), body["value"])
			send([]string{})
		case parts[2] == "idLabels" && r.Method == "DELETE":
			kept := []string{}
			for _, l := range idLabels(c) {
				if l != parts[3] {
					kept = append(kept, l)
				}
			}
			c["idLabels"] = kept
			send(map[string]any{})
		case parts[2] == "attachments" && r.Method == "GET":
			send(f.attachments[id])
		case parts[2] == "attachments" && r.Method == "POST":
			f.attachments[id] = append(f.attachments[id], map[string]string{"url": body["url"], "name": body["name"]})
			send(map[string]string{})
		default:
			http.NotFound(w, r)
		}
	default:
		http.NotFound(w, r)
	}
}

func newTestTracker(t *testing.T, opts Options) (*Tracker, *fakeTrello) {
	f := newFakeTrello()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.APIKey, opts.Token, opts.BaseURL = "key", "token", srv.URL
	return New(opts), f
}

func idLabels(c map[string]any) []string {
	ids, _ := c["idLabels"].([]string)
	return ids
}

var stages = map[tracker.Stage]Stage{
	tracker.StageInProgress: {List: "in progress"}, // the board says "  In Progress"
	tracker.StageInReview:   {List: "l-review", Label: "hinziel:  REVIEW"},
	tracker.StageBlocked:    {Label: "Blocked"},
}

func TestConformance(t *testing.T) {
	tr, _ := newTestTracker(t, Options{Board: "b1", Stages: stages})
	trackertest.Run(t, tr, "c1")
}

func TestFetch(t *testing.T) {
	tr, _ := newTestTracker(t, Options{})
	for _, id := range []string{"c1", "AbCd1234", "https://trello.com/c/AbCd1234/12-add-csv-export"} {
		tk, err := tr.Fetch(context.Background(), id)
		if err != nil {
			t.Fatalf("Fetch(%q): %v", id, err)
		}
		if tk.Ref.ID != "c1" || tk.Key != "AbCd1234" || tk.URL != "https://trello.com/c/AbCd1234" {
			t.Errorf("Fetch(%q): %+v", id, tk)
		}
	}
	tk, _ := tr.Fetch(context.Background(), "c1")
	if strings.Join(tk.Labels, ",") != "backend,red" {
		t.Errorf("labels %v", tk.Labels)
	}
	if len(tk.Comments) != 2 || tk.Comments[0].Body != "older" || tk.Comments[0].Author != "bo" || tk.Comments[1].Author != "Ana" {
		t.Errorf("comments must be oldest first: %+v", tk.Comments)
	}
	if got := tk.Fields["Checklist: Done when"]; got != "- [x] first\n- [ ] second\n" {
		t.Errorf("checklist %q", got)
	}
	if len(tk.Attachments) != 2 || tk.Attachments[0].MediaType != "image/png" || !tk.Attachments[0].IsFile || tk.Attachments[1].IsFile {
		t.Errorf("attachments %+v", tk.Attachments)
	}
}

func TestBoardRestriction(t *testing.T) {
	tr, _ := newTestTracker(t, Options{Board: "BoArD111"})
	if _, err := tr.Fetch(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Fetch(context.Background(), "c2"); err == nil || !strings.Contains(err.Error(), "not on board") {
		t.Errorf("a card on another board must be refused: %v", err)
	}
}

func TestStages(t *testing.T) {
	tr, f := newTestTracker(t, Options{Stages: stages})
	ctx := context.Background()
	card := func() (string, []string) { return f.cards["c1"]["idList"].(string), idLabels(f.cards["c1"]) }

	if err := tr.MoveTo(ctx, "AbCd1234", tracker.StageBlocked); err != nil {
		t.Fatal(err)
	}
	if list, labels := card(); list != "l-todo" || !slices.Equal(labels, []string{"lb-blocked"}) {
		t.Errorf("blocked: list %s, labels %v (a label-only stage must not move the card)", list, labels)
	}
	if err := tr.MoveTo(ctx, "c1", tracker.StageInProgress); err != nil {
		t.Fatal(err)
	}
	if list, labels := card(); list != "l-doing" || len(labels) != 0 {
		t.Errorf("in progress: list %s, labels %v (names match ignoring case and spaces; other stages' labels come off)", list, labels)
	}
	if err := tr.MoveTo(ctx, "c1", tracker.StageInReview); err != nil {
		t.Fatal(err)
	}
	if list, labels := card(); list != "l-review" || !slices.Equal(labels, []string{"lb-review"}) {
		t.Errorf("in review: list %s, labels %v (lists match by ID too)", list, labels)
	}

	tr2, _ := newTestTracker(t, Options{Stages: map[tracker.Stage]Stage{tracker.StageBlocked: {List: "Doing"}}})
	if err := tr2.MoveTo(ctx, "c1", tracker.StageBlocked); err == nil || !strings.Contains(err.Error(), `"New tickets"`) {
		t.Errorf("a missing list must name the board's lists: %v", err)
	}
	tr3, _ := newTestTracker(t, Options{Stages: map[tracker.Stage]Stage{tracker.StageBlocked: {List: "DONE"}}})
	if err := tr3.MoveTo(ctx, "c1", tracker.StageBlocked); err == nil || !strings.Contains(err.Error(), "use the ID") {
		t.Errorf("an ambiguous name must ask for the ID: %v", err)
	}
	if err := New(Options{}).MoveTo(ctx, "c1", tracker.StageBlocked); err != nil {
		t.Errorf("without Stages, MoveTo does nothing: %v", err)
	}
}

func TestDone(t *testing.T) {
	ctx := context.Background()
	done := func(tr *Tracker, id string) bool {
		t.Helper()
		ok, err := tr.Done(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	byList, f := newTestTracker(t, Options{Stages: map[tracker.Stage]Stage{tracker.StageDone: {List: "l-dup1"}}})
	if done(byList, "c1") {
		t.Error("a card in another list is not done")
	}
	f.cards["c1"]["idList"] = "l-dup1"
	if !done(byList, "AbCd1234") {
		t.Error("a card in StageDone's list is done")
	}

	byLabel, f := newTestTracker(t, Options{Stages: map[tracker.Stage]Stage{tracker.StageDone: {Label: "Blocked"}}})
	if done(byLabel, "c1") {
		t.Error("a card without StageDone's label is not done")
	}
	f.cards["c1"]["idLabels"] = []string{"lb-blocked"}
	if !done(byLabel, "c1") {
		t.Error("a card with StageDone's label is done")
	}

	plain, f := newTestTracker(t, Options{})
	if done(plain, "c1") {
		t.Error("without StageDone, an open card is not done")
	}
	f.cards["c1"]["closed"] = true
	if !done(plain, "c1") {
		t.Error("an archived card is done")
	}
	if !done(plain, "gone1234") {
		t.Error("a deleted card is done")
	}
}

func TestLinks(t *testing.T) {
	tr, f := newTestTracker(t, Options{})
	ctx := context.Background()
	link := core.Link{Title: "MR", URL: "https://gitlab.test/mr/1"}
	tr.AddLink(ctx, "c1", link)
	tr.AddLink(ctx, "c1", link)
	if n := len(f.attachments["c1"]); n != 1 {
		t.Errorf("%d attachments, want 1", n)
	}
}

func TestCommentIsMarkdownAndTruncated(t *testing.T) {
	tr, f := newTestTracker(t, Options{})
	tr.Comment(context.Background(), "c1", core.Message{Title: "Done", Body: "See below."})
	if got := f.comments["c1"][0]; got != "**Done**\n\nSee below." {
		t.Errorf("comment %q", got)
	}
	tr.Comment(context.Background(), "c1", core.Message{Body: strings.Repeat("é", maxComment)})
	if got := f.comments["c1"][1]; len(got) > maxComment || !strings.HasSuffix(got, "(truncated)") {
		t.Errorf("long comment: %d bytes", len(got))
	}
}

func TestCheck(t *testing.T) {
	tr, _ := newTestTracker(t, Options{Board: "b1", Stages: stages})
	if err := tr.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	tr, _ = newTestTracker(t, Options{Board: "b1", Stages: map[tracker.Stage]Stage{
		tracker.StageInProgress: {List: "Doing"},
		tracker.StageBlocked:    {Label: "Waiting"},
	}})
	err := tr.Check(context.Background())
	for _, want := range []string{`stage blocked: no label "Waiting"`, `stage in_progress: no list "Doing" on the board (it has: "New tickets", "  In Progress"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check must report %q: %v", want, err)
		}
	}
	bad := New(Options{APIKey: "key", Token: "wrong", BaseURL: tr.api.BaseURL[:len(tr.api.BaseURL)-2]})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("bad credentials must fail the check")
	}
}

// TestIntegration runs the conformance suite against real Trello. It posts a
// comment, adds a link and moves the card, so use a sandbox card.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_TRELLO") == "" {
		t.Skip("set HINZIEL_IT_TRELLO=1, TRELLO_API_KEY, TRELLO_TOKEN and TRELLO_TEST_CARD")
	}
	tr := New(Options{APIKey: os.Getenv("TRELLO_API_KEY"), Token: os.Getenv("TRELLO_TOKEN")})
	if err := tr.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	trackertest.Run(t, tr, os.Getenv("TRELLO_TEST_CARD"))
}

func TestCheckReportsEmptyCredentialsFirst(t *testing.T) {
	err := New(Options{Board: "b", Token: "t"}).Check(context.Background())
	if err == nil || !strings.HasPrefix(err.Error(), "not set: APIKey ") {
		t.Fatalf("got %v", err)
	}
}
