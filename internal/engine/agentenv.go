package engine

import (
	"os"
	"slices"
	"strings"
)

// baseAgentEnv names the variables of the factory's own environment that
// reach the agent: what any program needs to run (paths, locale, terminal,
// the user's directories, which Docker to use). Everything else stays out,
// the factory's credentials first: the agent reads tickets anyone on the
// board can write, and runs with permission prompts off. What the agent
// should have on purpose, the factory declares in Settings.AgentEnv; an
// agent component adds its own credentials.
var baseAgentEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TZ", "TERM", "TMPDIR",
	"XDG_RUNTIME_DIR", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
	"DOCKER_HOST", "DOCKER_CONTEXT",
}

// agentEnv is the agent's environment: the base variables and the locale
// (LC_*) from this process, then declared, then extra.
func agentEnv(declared map[string]string, extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if slices.Contains(baseAgentEnv, name) || strings.HasPrefix(name, "LC_") {
			out = append(out, kv)
		}
	}
	names := make([]string, 0, len(declared))
	for name := range declared {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		out = append(out, name+"="+declared[name])
	}
	return append(out, extra...)
}
