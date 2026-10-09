package heinzel

import (
	"os"
	"slices"
	"sync"
)

var missingEnv struct {
	sync.Mutex
	names []string
}

// Env returns the value of the environment variable name. A missing or empty
// variable is recorded rather than reported here, so Main can list every
// missing variable at once before doing anything.
func Env(name string) string {
	v := os.Getenv(name)
	if v == "" {
		missingEnv.Lock()
		if !slices.Contains(missingEnv.names, name) {
			missingEnv.names = append(missingEnv.names, name)
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
