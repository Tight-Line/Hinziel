package envcheck

import (
	"strings"
	"testing"
)

func TestGetRecordsMissingVariables(t *testing.T) {
	Reset()
	defer Reset()
	t.Setenv("HINZIEL_TEST_PRESENT", "x")
	Get("HINZIEL_TEST_PRESENT")
	Get("HINZIEL_TEST_MISSING")
	Get("HINZIEL_TEST_MISSING")

	if got := Missing(); len(got) != 1 || got[0] != "HINZIEL_TEST_MISSING" {
		t.Fatalf("got %v", got)
	}
}

func TestReadAfterCheckedIsReportedAsLate(t *testing.T) {
	Reset()
	defer Reset()
	Checked()
	if err := LateError(); err != nil {
		t.Fatalf("nothing read late yet: %v", err)
	}
	t.Setenv("HINZIEL_TEST_PRESENT", "x")
	Get("HINZIEL_TEST_PRESENT") // read late but set: fine
	Get("HINZIEL_TEST_LATE")
	if got := Missing(); len(got) != 0 {
		t.Errorf("a late read must not count as missing at startup: %v", got)
	}
	err := LateError()
	if err == nil || !strings.Contains(err.Error(), "missing environment variables: HINZIEL_TEST_LATE.") {
		t.Fatalf("got %v", err)
	}
}
