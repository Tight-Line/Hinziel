package envtest

import "testing"

func TestFakeConforms(t *testing.T) {
	Run(t, NewFake())
}
