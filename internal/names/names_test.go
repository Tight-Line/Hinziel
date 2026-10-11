package names

import (
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"PROJ-123", 0, "proj-123"},
		{"  Fix: the *login* page!  ", 0, "fix-the-login-page"},
		{"Ünïcode & co", 0, "n-code-co"},
		{"abc-def-ghi", 4, "abc"},
		{"---", 0, ""},
	} {
		if got := Slug(c.in, c.max); got != c.want {
			t.Errorf("Slug(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func TestBranch(t *testing.T) {
	if got := Branch("PROJ-7", "Add CSV export to the reports page"); got != "hinziel/proj-7-add-csv-export-to-the-reports" {
		t.Errorf("got %q", got)
	}
	if got := Branch("aB3dE9", ""); got != "hinziel/ab3de9" {
		t.Errorf("got %q", got)
	}
}

func TestEnvIsADNSLabel(t *testing.T) {
	got := Env("blanktracker", strings.Repeat("X", 100))
	if len(got) > 63 || strings.HasSuffix(got, "-") || !strings.HasPrefix(got, "hz-blanktracker-") {
		t.Errorf("got %q", got)
	}
}
