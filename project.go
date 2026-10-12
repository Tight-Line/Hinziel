package hinziel

import (
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/internal/engine"
	"github.com/Tight-Line/hinziel/internal/sandbox"
)

// sandboxRunner starts the agents' sandboxes, in the host's Docker.
var sandboxRunner = &sandbox.Docker{}

func (f *Factory) sandboxContents() sandbox.Contents {
	return sandbox.Contents{Docker: f.Sandbox.Docker, Dockerfile: f.Sandbox.Dockerfile}
}

// project converts a validated factory into the engine's form.
func (f *Factory) project() *engine.Project {
	p := &engine.Project{
		Name:        f.Name,
		Tracker:     f.Tracker,
		Agent:       f.Agent,
		Provisioner: f.Environment,
		DNS:         f.DNS,
		Cleaners:    f.Cleanup,
		Sandbox:     sandboxRunner,
		Settings: engine.Settings{
			Instructions:        f.Instructions,
			BranchName:          f.BranchName,
			EnvName:             f.EnvName,
			EnvironmentActions:  f.EnvironmentActions,
			Gates:               stepNames(f.Gates),
			DisabledSteps:       stepNames(f.DisabledSteps),
			DraftChangeRequests: f.DraftChangeRequests,
			AgentEnv:            f.AgentEnv,
			Teardown:            engine.Teardown{WhenDone: f.Teardown.WhenDone, After: f.Teardown.After},
			Sandbox:             f.sandboxContents(),
		},
	}
	for _, r := range f.Repos {
		p.Repos = append(p.Repos, engine.Repo{Name: r.Name, Forge: r.Forge, TargetBranch: r.TargetBranch})
	}
	for _, ep := range f.Endpoints {
		e := engine.Endpoint{Name: ep.Name, URL: ep.URL}
		if ep.DNS != nil {
			e.DNS = func(en *env.Environment) (string, string) { return ep.DNS(en) }
		}
		p.Endpoints = append(p.Endpoints, e)
	}
	for _, d := range f.Deployers {
		p.Deployers = append(p.Deployers, engine.Deployer{Name: d.Name, Repo: d.Repo, Deployer: d.Deployer})
	}
	for _, n := range f.Notifiers {
		p.Notifiers = append(p.Notifiers, engine.Notifier{On: n.On, Notifier: n.Notifier})
	}
	return p
}
