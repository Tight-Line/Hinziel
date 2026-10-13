package cert_test

import (
	"context"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/cert"
	"github.com/Tight-Line/hinziel/cert/certtest"
)

func TestCurrent(t *testing.T) {
	names := []string{"hz-abc-api.example.com", "hz-abc-nova.example.com"}
	key, csr, err := cert.NewRequest(names)
	if err != nil {
		t.Fatal(err)
	}
	ca := certtest.NewFake()
	ca.Validity = 30 * 24 * time.Hour
	crt, err := ca.Issue(context.Background(), names, csr)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, _, _ := cert.NewRequest(names)
	now := time.Now()
	for name, c := range map[string]struct {
		key   []byte
		names []string
		at    time.Time
		want  bool
	}{
		"fresh":           {key, names, now, true},
		"reordered names": {key, []string{"HZ-abc-nova.example.com.", "hz-abc-api.example.com"}, now, true},
		"at two thirds":   {key, names, now.Add(19 * 24 * time.Hour), true},
		"last third":      {key, names, now.Add(21 * 24 * time.Hour), false},
		"another key":     {otherKey, names, now, false},
		"fewer names":     {key, names[:1], now, false},
		"more names":      {key, append(names, "hz-abc.example.com"), now, false},
		"not yet valid":   {key, names, now.Add(-time.Hour), false},
	} {
		if got := cert.Current(crt, c.key, c.names, c.at); got != c.want {
			t.Errorf("%s: Current = %v, want %v", name, got, c.want)
		}
	}
	if cert.Current([]byte("garbage"), key, names, now) || cert.Current(nil, nil, names, now) {
		t.Error("a missing or broken certificate is not current")
	}
}
