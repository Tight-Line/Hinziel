package core

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Checker is implemented by any component that can verify what it needs at
// run time: credentials, access, and the tools it runs on the Hinzele host.
// Used by the `check` command, which reports every failure at once.
type Checker interface {
	Check(ctx context.Context) error
}

// NeedTools reports, in one error, which of the commands are not on PATH.
// Components call it from Check, so a fresh host learns everything it's
// missing in one `check`, not one failure at a time mid-run.
func NeedTools(commands ...string) error {
	var missing []string
	for _, c := range commands {
		if _, err := exec.LookPath(c); err != nil {
			missing = append(missing, c)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("not installed on this host (or not on PATH): %s", strings.Join(missing, ", "))
}
