package dnstest

import "testing"

func TestFakeConforms(t *testing.T) {
	Run(t, NewFake(), "preview.test")
}
