package notifytest

import "testing"

func TestFakeConforms(t *testing.T) {
	f := &Fake{}
	Run(t, f)
	if len(f.Events()) != 9 {
		t.Fatalf("got %d events", len(f.Events()))
	}
}
