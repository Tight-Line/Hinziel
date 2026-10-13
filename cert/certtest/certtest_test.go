package certtest

import "testing"

func TestFakeConforms(t *testing.T) {
	f := NewFake()
	Run(t, f, "example.com")
	if f.Revoked() != 2 {
		t.Errorf("issuing twice and revoking must revoke two certificates, revoked %d", f.Revoked())
	}
}
