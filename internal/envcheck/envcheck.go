// Package envcheck records the environment variables a factory reads and
// finds missing, so Main can list them all at once before doing anything.
package envcheck

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

var missing struct {
	sync.Mutex
	started bool     // Main has checked the variables read so far
	names   []string // missing, read before started
	late    []string // missing, read after started
}

// Get returns the value of the environment variable name, recording it if
// it's missing or empty.
func Get(name string) string {
	v := os.Getenv(name)
	if v == "" {
		missing.Lock()
		list := &missing.names
		if missing.started {
			list = &missing.late
		}
		if !slices.Contains(*list, name) {
			*list = append(*list, name)
		}
		missing.Unlock()
	}
	return v
}

// Missing returns the variables read before Checked and found missing.
func Missing() []string {
	missing.Lock()
	defer missing.Unlock()
	return slices.Clone(missing.names)
}

// Checked marks the variables read so far as checked by Main: any read
// from now on happens during a command.
func Checked() {
	missing.Lock()
	missing.started = true
	missing.Unlock()
}

// LateError reports variables read after Main's check and found missing.
// The process can't pick up a variable set later, so it keeps failing until
// the factory is restarted with it, or reads it at startup.
func LateError() error {
	missing.Lock()
	defer missing.Unlock()
	if len(missing.late) == 0 {
		return nil
	}
	return fmt.Errorf("missing environment variables: %s. They are read with hinziel.Env inside a func "+
		"of the factory, so they weren't checked at startup; set them and resume, and read them while "+
		"building the Factory so Main checks them", strings.Join(missing.late, ", "))
}

// Reset forgets everything recorded, for tests.
func Reset() {
	missing.Lock()
	missing.started, missing.names, missing.late = false, nil, nil
	missing.Unlock()
}
