package hinziel

import "github.com/Tight-Line/hinziel/internal/envcheck"

// Env returns the value of the environment variable name. A missing or empty
// variable is recorded rather than reported here, so Main can list every
// missing variable at once before doing anything.
//
// Call it while building the Factory, not inside a func the Factory holds
// (a deployer's Env, an Endpoint's URL, …): those run later, during a run,
// after Main's check. A variable found missing there fails the step that
// read it instead of being used as empty.
func Env(name string) string { return envcheck.Get(name) }
