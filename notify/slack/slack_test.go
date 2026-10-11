package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/notify/notifytest"
)

type fakeWebhook struct {
	mu       sync.Mutex
	payloads []map[string]any
}

func (f *fakeWebhook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p map[string]any
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p["text"] == "" {
		http.Error(w, "invalid_payload", http.StatusBadRequest)
		return
	}
	f.payloads = append(f.payloads, p)
	w.Write([]byte("ok"))
}

func TestConformance(t *testing.T) {
	f := &fakeWebhook{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	notifytest.Run(t, New(srv.URL))
	if len(f.payloads) != 7 {
		t.Errorf("%d posts", len(f.payloads))
	}
}

func TestPayload(t *testing.T) {
	p := payload(core.Message{
		Title: "shop T-1: action needed: Allow Google login",
		Body:  "## Do this\n\n- [ ] add **both** URIs from [the console](https://console.test)\n- [x] done\n- plain <tag> & co",
		Links: []core.Link{{Title: "Preview", URL: "https://preview.test"}, {Title: "Console", URL: "https://console.test"}},
	})
	blocks := p["blocks"].([]block)
	if blocks[0].Type != "header" || blocks[0].Text.Text != "shop T-1: action needed: Allow Google login" {
		t.Errorf("header %+v", blocks[0])
	}
	want := "*Do this*\n\n☐ add *both* URIs from <https://console.test|the console>\n☑ done\n• plain &lt;tag&gt; &amp; co\n\n<https://preview.test|Preview>"
	if got := blocks[1].Text.Text; got != want {
		t.Errorf("section\n%s\nwant\n%s", got, want)
	}
	if p["text"] != "shop T-1: action needed: Allow Google login" {
		t.Errorf("fallback %q", p["text"])
	}
}

func TestLongBodiesAreSplit(t *testing.T) {
	line := strings.Repeat("x", 99) + "\n"
	p := payload(core.Message{Body: strings.Repeat(line, 100)}) // 10000 characters
	blocks := p["blocks"].([]block)
	if len(blocks) != 4 {
		t.Fatalf("%d blocks", len(blocks))
	}
	for _, b := range blocks {
		if n := len([]rune(b.Text.Text)); n > maxSection {
			t.Errorf("section of %d characters", n)
		}
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no_service", http.StatusNotFound)
	}))
	defer srv.Close()
	err := New(srv.URL).Notify(context.Background(), &notify.Event{Message: core.Message{Title: "x"}})
	if err == nil || !strings.Contains(err.Error(), "no_service") {
		t.Errorf("a rejected post must be an error: %v", err)
	}
	if New("http://insecure.test/hook").Check(context.Background()) == nil {
		t.Error("a non-https webhook must fail the check")
	}
}

// TestIntegration posts one message of each event type to a real channel.
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_SLACK") == "" {
		t.Skip("set HINZIEL_IT_SLACK=1 and SLACK_TEST_WEBHOOK")
	}
	notifytest.Run(t, New(os.Getenv("SLACK_TEST_WEBHOOK")))
}
