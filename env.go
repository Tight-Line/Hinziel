package hinzele

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
)

var missingEnv struct {
	sync.Mutex
	started bool     // Main has checked the variables read so far
	names   []string // missing, read before started
	late    []string // missing, read after started
}

// Env returns the value of the environment variable name. A missing or empty
// variable is recorded rather than reported here, so Main can list every
// missing variable at once before doing anything.
//
// Call it while building the Factory, not inside a func the Factory holds
// (a deployer's Env, an Endpoint's URL, …): those run later, during a run,
// after Main's check. A variable found missing there fails the step that
// read it instead of being used as empty.
func Env(name string) string {
	v := os.Getenv(name)
	if v == "" {
		missingEnv.Lock()
		list := &missingEnv.names
		if missingEnv.started {
			list = &missingEnv.late
		}
		if !slices.Contains(*list, name) {
			*list = append(*list, name)
		}
		missingEnv.Unlock()
	}
	return v
}

func missingEnvNames() []string {
	missingEnv.Lock()
	defer missingEnv.Unlock()
	return slices.Clone(missingEnv.names)
}

// envChecked marks the variables read so far as checked by Main: any read
// from now on happens during a command.
func envChecked() {
	missingEnv.Lock()
	missingEnv.started = true
	missingEnv.Unlock()
}

// lateEnvError reports variables read after Main's check and found missing.
// The process can't pick up a variable set later, so it keeps failing until
// the factory is restarted with it, or reads it at startup.
func lateEnvError() error {
	missingEnv.Lock()
	defer missingEnv.Unlock()
	if len(missingEnv.late) == 0 {
		return nil
	}
	return fmt.Errorf("missing environment variables: %s. They are read with hinzele.Env inside a func "+
		"of the factory, so they weren't checked at startup; set them and resume, and read them while "+
		"building the Factory so Main checks them", strings.Join(missingEnv.late, ", "))
}
