package hinziel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/tracker"
)

// loginTracker is a tracker that logs in through a link: the answer "ok"
// works, anything else doesn't.
type loginTracker struct {
	in      bool
	started int
}

func (l *loginTracker) Fetch(context.Context, string) (*tracker.Ticket, error) { return nil, nil }
func (l *loginTracker) Comment(context.Context, string, core.Message) error    { return nil }
func (l *loginTracker) LoggedIn(context.Context) (bool, error)                 { return l.in, nil }
func (l *loginTracker) StartLogin(context.Context) (string, error) {
	l.started++
	return "open https://login.test/" + string(rune('0'+l.started)), nil
}
func (l *loginTracker) FinishLogin(_ context.Context, answer string) error {
	if strings.TrimSpace(answer) != "ok" {
		return errors.New("not ok")
	}
	l.in = true
	return nil
}

func runLogin(t *testing.T, input string, factories []Factory, args ...string) (string, error) {
	t.Helper()
	old := stdin
	stdin = strings.NewReader(input)
	t.Cleanup(func() { stdin = old })
	var out bytes.Buffer
	err := login(context.Background(), &out, factories, args)
	return out.String(), err
}

func TestLogin(t *testing.T) {
	l := &loginTracker{}
	// The same component in two factories logs in once.
	factories := []Factory{{Name: "a", Tracker: l}, {Name: "b", Tracker: l}}
	out, err := runLogin(t, "nope\nok\n", factories)
	if err != nil {
		t.Fatal(err)
	}
	if l.started != 2 || !strings.Contains(out, "https://login.test/2") || !strings.Contains(out, "That didn't work: not ok") ||
		!strings.Contains(out, "ok    a: tracker: logged in") || strings.Contains(out, "b: tracker") {
		t.Errorf("%d links, output:\n%s", l.started, out)
	}

	out, err = runLogin(t, "", factories)
	if err != nil || !strings.Contains(out, "already logged in") || l.started != 2 {
		t.Errorf("logged in already: %v\n%s", err, out)
	}
	if _, err := runLogin(t, "ok\n", factories, "--force"); err != nil || l.started != 3 {
		t.Errorf("--force logs in again: %v, %d links", err, l.started)
	}

	l.in = false
	if _, err := runLogin(t, "a\nb\nc\nok\n", factories); err == nil || !strings.Contains(err.Error(), "not ok") {
		t.Errorf("three bad answers must give up: %v", err)
	}
	if _, err := runLogin(t, "", factories); err == nil || !strings.Contains(err.Error(), "nothing pasted") {
		t.Errorf("no input must fail: %v", err)
	}
	if _, err := runLogin(t, "", []Factory{{Name: "c", Tracker: &struct{ tracker.Tracker }{}}}); err == nil {
		t.Error("a factory with nothing to log in to must say so")
	}
}
