package skrat_test

import (
	"context"
	"strings"
	"testing"

	"github.com/Tight-Line/skrat/agent/claudecode"
	"github.com/Tight-Line/skrat/core"
	"github.com/Tight-Line/skrat/deploy/cfworkers"
	"github.com/Tight-Line/skrat/deploy/dockercompose"
	"github.com/Tight-Line/skrat/env/digitalocean"
)

// On a host with nothing installed, every component's Check names the
// tools it needs. Checks that also call an API fail there too (nothing
// listens at the BaseURL), but the missing tools must be in the error.
func TestChecksNameMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for name, c := range map[string]struct {
		checker core.Checker
		tools   string
	}{
		"digitalocean":  {digitalocean.New(digitalocean.Options{BaseURL: "http://127.0.0.1:1"}), "ssh"},
		"dockercompose": {dockercompose.New(dockercompose.Options{}), "ssh"},
		"cfworkers":     {cfworkers.New(cfworkers.Options{Worker: "w", Build: "npm run build", BaseURL: "http://127.0.0.1:1"}), "node, npx, sh"},
		"claudecode":    {claudecode.New(claudecode.Options{}), "claude"},
	} {
		err := c.checker.Check(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not installed on this host (or not on PATH): "+c.tools) {
			t.Errorf("%s: want missing %s, got %v", name, c.tools, err)
		}
	}
}
