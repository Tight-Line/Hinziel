package engine

import (
	"strings"
	"testing"

	"github.com/Tight-Line/skrat/agent"
	"github.com/Tight-Line/skrat/env"
	"github.com/Tight-Line/skrat/run"
	"github.com/Tight-Line/skrat/tracker"
)

func TestPromptTellsTheAgentHowToReachHosts(t *testing.T) {
	r := &run.Run{}
	r.Ticket = &tracker.Ticket{Key: "T-1", Title: "Export"}
	r.Branch = "skrat/t-1-export"
	task := &agent.Task{Workdir: "/w", Environment: &env.Environment{
		Name:      "sk-t-1",
		Endpoints: map[string]string{"api": "https://sk-t-1-api.example.com"},
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
