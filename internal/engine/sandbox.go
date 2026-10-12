package engine

import (
	"os"
	"path/filepath"

	"github.com/Tight-Line/hinziel/internal/sandbox"
)

// sandboxName is the run's sandbox, the same on every attempt, so starting
// it again replaces what an interrupted attempt left behind.
func (x *execution) sandboxName() string { return sandboxName(x.p.Name, string(x.r.ID)) }

func sandboxName(factory, id string) string { return "hinziel-" + factory + "-" + id }

// agentDir holds the agent component's own files and the agent's socket.
func (x *execution) agentDir() string { return filepath.Join(x.e.store.Dir(x.r.ID), "agent") }

// sandboxSpec is what the run's sandbox gets from the factory's host: the
// checkouts, the ticket's attachments, the agent's directory and socket, the
// factory binary for `$HINZIEL_BIN deploy`, and the environment's SSH key.
// Nothing else.
func (x *execution) sandboxSpec(socket string) sandbox.Spec {
	spec := sandbox.Spec{
		Name:     x.sandboxName(),
		Contents: x.p.Settings.Sandbox,
		Labels:   map[string]string{"hinziel.factory": x.p.Name, "hinziel.run": string(x.r.ID)},
		Mounts: []sandbox.Mount{
			{Path: x.reposDir()},
			{Path: x.agentDir()},
			{Path: x.e.opts.Bin, ReadOnly: true},
		},
	}
	if filepath.Dir(socket) != x.agentDir() {
		spec.Mounts = append(spec.Mounts, sandbox.Mount{Path: socket})
	}
	readOnly := func(path string) {
		if _, err := os.Stat(path); path != "" && err == nil {
			spec.Mounts = append(spec.Mounts, sandbox.Mount{Path: path, ReadOnly: true})
		}
	}
	readOnly(filepath.Join(x.e.store.Dir(x.r.ID), "attachments"))
	if e := x.r.Environment; e != nil {
		for _, h := range e.Hosts {
			if h.SSH != nil {
				readOnly(h.SSH.KeyPath)
				readOnly(h.SSH.KnownHostsPath)
			}
		}
	}
	return spec
}
