package heinzel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/tracker"
)

type fakeTracker struct{ checkErr error }

func (fakeTracker) Fetch(context.Context, string) (*tracker.Ticket, error) { return nil, nil }
func (fakeTracker) Comment(context.Context, string, core.Message) error    { return nil }
func (t fakeTracker) Check(context.Context) error                          { return t.checkErr }

type fakeForge struct{}

func (fakeForge) Remote(context.Context) (*forge.GitRemote, error) { return nil, nil }
func (fakeForge) OpenChangeRequest(context.Context, forge.ChangeRequestInput) (*forge.ChangeRequest, error) {
	return nil, nil
}
func (fakeForge) CommentChangeRequest(context.Context, *forge.ChangeRequest, core.Message) error {
	return nil
}

type fakeAgent struct{}

func (fakeAgent) Implement(context.Context, *agent.Task) (*agent.Outcome, error) { return nil, nil }

type fakeEnv struct{}

func (fakeEnv) Ensure(context.Context, env.Spec) (*env.Environment, error) { return nil, nil }
func (fakeEnv) Destroy(context.Context, *env.Environment) error            { return nil }

type fakeDeployer struct{}

func (fakeDeployer) Deploy(context.Context, *env.Environment, *deploy.Source) (*deploy.Deployment, error) {
	return nil, nil
}

type fakeDNS struct{}

func (fakeDNS) Ensure(context.Context, dns.Record) error { return nil }
func (fakeDNS) Delete(context.Context, string) error     { return nil }

func validFactory() Factory {
	return Factory{
		Name:        "blanktracker",
		Tracker:     fakeTracker{},
		Repos:       []Repo{{Name: "app", Forge: fakeForge{}, TargetBranch: "staging"}},
		Agent:       fakeAgent{},
		Environment: fakeEnv{},
		DNS:         fakeDNS{},
		Endpoints: []Endpoint{{
			Name: "api",
			URL:  func(e *env.Environment) string { return "https://" + e.Name + "-api.example.com" },
			DNS:  HostAddress(0),
		}},
		Deployers: []Deployer{{Name: "api", Repo: "app", Deployer: fakeDeployer{}}},
		Gates:     []Step{StepDeploy},
	}
}

func TestValidateAcceptsValidFactory(t *testing.T) {
	f := validFactory()
	if err := f.validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	f := validFactory()
	f.Name = "Blank Tracker"
	f.Agent = nil
	f.Repos = append(f.Repos, Repo{Name: "app", Forge: fakeForge{}, TargetBranch: "main"})
	f.Deployers[0].Repo = "web"
	f.Gates = []Step{"deploy_everything"}

	err := f.validate()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, want := range []string{
		"must be lowercase",
		"Agent is required",
		`duplicate name "app"`,
		`Repo "web" is not one of the factory's repos`,
		`unknown step "deploy_everything"`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q:\n%v", want, err)
		}
	}
}

func TestValidateRequiresEnvironmentForDeployers(t *testing.T) {
	f := validFactory()
	f.Environment = nil
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "need an Environment") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateRequiresDNSProviderForDNSTargets(t *testing.T) {
	f := validFactory()
	f.DNS = nil
	if err := f.validate(); err == nil || !strings.Contains(err.Error(), "no DNS provider") {
		t.Fatalf("got %v", err)
	}
}

func TestHostAddress(t *testing.T) {
	e := &env.Environment{Hosts: []env.Host{{Address: "203.0.113.7"}, {Address: "2001:db8::1"}, {Address: "box.example.com"}}}
	for i, want := range []string{"A", "AAAA", "CNAME"} {
		if typ, _ := HostAddress(i)(e); typ != want {
			t.Errorf("host %d: got %q, want %q", i, typ, want)
		}
	}
	if typ, _ := HostAddress(5)(e); typ != "" {
		t.Errorf("out of range: got %q, want no record", typ)
	}
}

func TestRunRejectsDuplicateFactories(t *testing.T) {
	err := Run(context.Background(), nil, &bytes.Buffer{}, validFactory(), validFactory())
	if err == nil || !strings.Contains(err.Error(), "duplicate factory name") {
		t.Fatalf("got %v", err)
	}
}

func TestRunCheckCallsCheckers(t *testing.T) {
	f := validFactory()
	f.Tracker = fakeTracker{checkErr: errors.New("bad token")}
	var out bytes.Buffer
	err := Run(context.Background(), []string{"check"}, &out, f)
	if err == nil {
		t.Fatal("expected check to fail")
	}
	if !strings.Contains(out.String(), "FAIL  blanktracker: tracker: bad token") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestEnvRecordsMissingVariables(t *testing.T) {
	t.Setenv("HEINZEL_TEST_PRESENT", "x")
	Env("HEINZEL_TEST_PRESENT")
	Env("HEINZEL_TEST_MISSING")
	Env("HEINZEL_TEST_MISSING")

	got := missingEnvNames()
	if len(got) != 1 || got[0] != "HEINZEL_TEST_MISSING" {
		t.Fatalf("got %v", got)
	}
	err := Run(context.Background(), nil, &bytes.Buffer{}, validFactory())
	if err == nil || !strings.Contains(err.Error(), "HEINZEL_TEST_MISSING") {
		t.Fatalf("got %v", err)
	}
	missingEnv.names = nil
}
