package engine

import (
	"strings"
	"testing"

	"github.com/Tight-Line/hinzele/agent"
	"github.com/Tight-Line/hinzele/env"
	"github.com/Tight-Line/hinzele/run"
	"github.com/Tight-Line/hinzele/tracker"
)

func TestPromptTellsTheAgentHowToReachHosts(t *testing.T) {
	r := &run.Run{}
	r.Ticket = &tracker.Ticket{Key: "T-1", Title: "Export"}
	r.Branch = "hinzele/t-1-export"
	task := &agent.Task{Workdir: "/w", Environment: &env.Environment{
		Name:      "hz-t-1",
		Endpoints: map[string]string{"api": "https://hz-t-1-api.example.com"},
		Hosts: []env.Host{
			{Name: "main", Address: "203.0.113.10", SSH: &env.SSHAccess{User: "root", Port: 22, KeyPath: "/k/id", KnownHostsPath: "/k/hosts"}},
			{Name: "db", Address: "203.0.113.11", SSH: &env.SSHAccess{Port: 2222}},
			{Name: "lb", Address: "203.0.113.12"}, // no SSH access
		},
	}}
	p := prompt(&Project{}, r, task)
	for _, want := range []string{
		"- main: `ssh -i /k/id -o UserKnownHostsFile=/k/hosts root@203.0.113.10`",
		"- db: `ssh -p 2222 203.0.113.11`",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt must contain %q:\n%s", want, p)
		}
	}
	if strings.Contains(p, "203.0.113.12") {
		t.Error("a host without SSH access must not be offered")
	}
}
