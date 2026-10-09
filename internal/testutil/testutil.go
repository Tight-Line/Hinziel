// Package testutil holds small helpers shared by the conformance suites.
package testutil

import (
	"crypto/rand"
	"encoding/hex"
)

// Suffix returns a short random lowercase hex string for unique test names.
func Suffix() string {
	b := make([]byte, 4)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// PopErr removes and returns the first error of a queue of injected errors.
func PopErr(errs *[]error) error {
	if len(*errs) == 0 {
		return nil
	}
	err := (*errs)[0]
	*errs = (*errs)[1:]
	return err
}
