package engine

import (
	"testing"

	"github.com/Tight-Line/hinziel/env"
)

func TestDeployerStateIsKeptWithTheEnvironment(t *testing.T) {
	f := newFixture(t)
	f.apiDep.State = map[string]string{"api.setup": "done", "api.revision": "7"}
	f.agentChangesAPI()
	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Environment.State; got["api.setup"] != "done" || got["api.revision"] != "7" {
		t.Fatalf("environment state %v", got)
	}
	deps := f.apiDep.Deployed()
	if len(deps) != 2 || deps[0].State["api.setup"] != "" || deps[1].State["api.setup"] != "done" {
		t.Fatalf("the second deploy must see what the first kept: %+v", deps)
	}

	// A later run gets the same environment back, and what was kept with it.
	f.apiDep.State = map[string]string{"api.revision": ""}
	again, err := f.envUp("T-1")
	if err != nil {
		t.Fatal(err)
	}
	if again.Environment.ID != r.Environment.ID {
		t.Fatalf("env up for T-1 must reuse %s, got %s", r.Environment.ID, again.Environment.ID)
	}
	deps = f.apiDep.Deployed()
	if last := deps[len(deps)-1]; last.State["api.setup"] != "done" || last.State["api.revision"] != "7" {
		t.Errorf("a later run's deployer must see the kept state: %v", last.State)
	}
	if got := again.Environment.State; got["api.setup"] != "done" || got["api.revision"] != "" {
		t.Errorf("an empty value must delete the key: %v", got)
	}
}

func TestDeployerStateIsNotCarriedToANewEnvironment(t *testing.T) {
	f := newFixture(t)
	f.apiDep.State = map[string]string{"api.setup": "done"}
	first, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}

	// The environment disappears outside Hinziel (a scratch org expires),
	// and the next Ensure creates a new one under the same name.
	if err := f.env.Destroy(f.ctx, &env.Environment{Name: first.Environment.Name}); err != nil {
		t.Fatal(err)
	}
	f.apiDep.State = nil
	second, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}
	if second.Environment.ID == first.Environment.ID {
		t.Fatalf("expected a new environment, got the same ID %s", first.Environment.ID)
	}
	deps := f.apiDep.Deployed()
	if last := deps[len(deps)-1]; len(last.State) != 0 {
		t.Errorf("a new environment must start without the old one's state: %v", last.State)
	}

	// Torn down through Hinziel: the same.
	f.apiDep.State = map[string]string{"api.setup": "done"}
	if _, err := f.envUp("base"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.eng.Destroy(f.ctx, "", "hz-shop-base", "test"); err != nil {
		t.Fatal(err)
	}
	f.apiDep.State = nil
	third, err := f.envUp("base")
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Environment.State) != 0 {
		t.Errorf("state survived a teardown: %v", third.Environment.State)
	}
}
