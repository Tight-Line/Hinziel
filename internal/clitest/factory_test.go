package clitest

import (
	"bytes"
	"context"
	"errors"
	"github.com/Tight-Line/hinziel"
	"io"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/dns"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/forge"
	"github.com/Tight-Line/hinziel/internal/envcheck"
	"github.com/Tight-Line/hinziel/tracker"
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

func validFactory() hinziel.Factory {
	return hinziel.Factory{
		Name:        "blanktracker",
		Tracker:     fakeTracker{},
		Repos:       []hinziel.Repo{{Name: "app", Forge: fakeForge{}, TargetBranch: "staging"}},
		Agent:       fakeAgent{},
		Environment: fakeEnv{},
		DNS:         fakeDNS{},
		Endpoints: []hinziel.Endpoint{{
			Name: "api",
			URL:  func(e *env.Environment) string { return "https://" + e.Name + "-api.example.com" },
			DNS:  hinziel.HostAddress(0),
		}},
		Deployers: []hinziel.Deployer{{Name: "api", Repo: "app", Deployer: fakeDeployer{}}},
		Gates:     []hinziel.Step{hinziel.StepDeploy},
	}
}

// validate runs f's validation, which Run does before anything else.
func validate(f hinziel.Factory) error {
	return hinziel.Run(context.Background(), nil, io.Discard, f)
}

func TestValidateAcceptsValidFactory(t *testing.T) {
	f := validFactory()
	if err := validate(f); err != nil {
		t.Fatal(err)
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	f := validFactory()
	f.Name = "Blank Tracker"
	f.Agent = nil
	f.Repos = append(f.Repos, hinziel.Repo{Name: "app", Forge: fakeForge{}, TargetBranch: "main"})
	f.Deployers[0].Repo = "web"
	f.Gates = []hinziel.Step{"deploy_everything"}

	err := validate(f)
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
	if err := validate(f); err == nil || !strings.Contains(err.Error(), "need an Environment") {
		t.Fatalf("got %v", err)
	}
}

func TestValidateRequiresDNSProviderForDNSTargets(t *testing.T) {
	f := validFactory()
	f.DNS = nil
	if err := validate(f); err == nil || !strings.Contains(err.Error(), "no DNS provider") {
		t.Fatalf("got %v", err)
	}
}

func TestHostAddress(t *testing.T) {
	e := &env.Environment{Hosts: []env.Host{{Address: "203.0.113.7"}, {Address: "2001:db8::1"}, {Address: "box.example.com"}}}
	for i, want := range []string{"A", "AAAA", "CNAME"} {
		if typ, _ := hinziel.HostAddress(i)(e); typ != want {
			t.Errorf("host %d: got %q, want %q", i, typ, want)
		}
	}
	if typ, _ := hinziel.HostAddress(5)(e); typ != "" {
		t.Errorf("out of range: got %q, want no record", typ)
	}
}

func TestRunRejectsDuplicateFactories(t *testing.T) {
	err := hinziel.Run(context.Background(), nil, &bytes.Buffer{}, validFactory(), validFactory())
	if err == nil || !strings.Contains(err.Error(), "duplicate factory name") {
		t.Fatalf("got %v", err)
	}
}

func TestRunCheckCallsCheckers(t *testing.T) {
	f := validFactory()
	f.Tracker = fakeTracker{checkErr: errors.New("bad token")}
	var out bytes.Buffer
	err := hinziel.Run(context.Background(), []string{"check"}, &out, f)
	if err == nil {
		t.Fatal("expected check to fail")
	}
	if !strings.Contains(out.String(), "FAIL  blanktracker: tracker: bad token") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestRunReportsMissingEnv(t *testing.T) {
	envcheck.Reset()
	defer envcheck.Reset()
	hinziel.Env("HINZIEL_TEST_MISSING")
	err := hinziel.Run(context.Background(), nil, &bytes.Buffer{}, validFactory())
	if err == nil || !strings.Contains(err.Error(), "missing environment variables: HINZIEL_TEST_MISSING") {
		t.Fatalf("got %v", err)
	}
}

func TestEnvReadAfterRunIsReportedAsLate(t *testing.T) {
	envcheck.Reset()
	defer envcheck.Reset()
	if err := hinziel.Run(context.Background(), nil, &bytes.Buffer{}, validFactory()); err != nil {
		t.Fatal(err)
	}
	hinziel.Env("HINZIEL_TEST_LATE")
	if err := envcheck.LateError(); err == nil || !strings.Contains(err.Error(), "HINZIEL_TEST_LATE") {
		t.Fatalf("got %v", err)
	}
}

func TestRunCheckReportsMissingEnvAndStillChecks(t *testing.T) {
	envcheck.Reset()
	defer envcheck.Reset()
	hinziel.Env("HINZIEL_TEST_MISSING")
	f := validFactory()
	f.Tracker = fakeTracker{checkErr: errors.New("bad token")}
	var out bytes.Buffer
	err := hinziel.Run(context.Background(), []string{"check"}, &out, f)
	if err == nil {
		t.Fatal("expected check to fail")
	}
	for _, want := range []string{
		"FAIL  hinziel: missing environment variables: HINZIEL_TEST_MISSING",
		"FAIL  blanktracker: tracker: bad token",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestRunCheckReportsMissingGit(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	err := hinziel.Run(context.Background(), []string{"check"}, &out, validFactory())
	if err == nil || !strings.Contains(out.String(), "FAIL  hinziel: not installed on this host (or not on PATH): git") {
		t.Fatalf("err %v, output:\n%s", err, out.String())
	}
}

type checkedDeployer struct {
	fakeDeployer
	err error
}

func (d checkedDeployer) Check(context.Context) error { return d.err }

// checkFactory has a failing tracker and two deployers, one of them failing.
func checkFactory() hinziel.Factory {
	f := validFactory()
	f.Tracker = fakeTracker{checkErr: errors.New("bad token")}
	f.Deployers = []hinziel.Deployer{
		{Name: "api", Repo: "app", Deployer: checkedDeployer{}},
		{Name: "web", Repo: "app", Deployer: checkedDeployer{err: errors.New("no wrangler")}},
	}
	return f
}

func TestRunCheckSelectsComponents(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		want, skip []string
		fails      bool
	}{
		{
			args: []string{"check", "deployer api"},
			want: []string{"ok    blanktracker: deployer api"},
			skip: []string{": tracker", "deployer web", "hinziel: git"},
		},
		{
			args:  []string{"check", "deployer"},
			want:  []string{"ok    blanktracker: deployer api", "FAIL  blanktracker: deployer web: no wrangler"},
			skip:  []string{": tracker"},
			fails: true,
		},
		{
			args:  []string{"check", "--factory", "blanktracker", "tracker", "hinziel"},
			want:  []string{"FAIL  blanktracker: tracker: bad token", "ok    hinziel: git"},
			skip:  []string{"deployer"},
			fails: true,
		},
	} {
		var out bytes.Buffer
		err := hinziel.Run(context.Background(), tc.args, &out, checkFactory())
		if (err != nil) != tc.fails {
			t.Errorf("%v: err %v", tc.args, err)
		}
		for _, w := range tc.want {
			if !strings.Contains(out.String(), w) {
				t.Errorf("%v: output lacks %q:\n%s", tc.args, w, out.String())
			}
		}
		for _, s := range tc.skip {
			if strings.Contains(out.String(), s) {
				t.Errorf("%v: output mentions %q:\n%s", tc.args, s, out.String())
			}
		}
	}
}

func TestRunCheckRejectsUnknownSelector(t *testing.T) {
	err := hinziel.Run(context.Background(), []string{"check", "deployer nova"}, &bytes.Buffer{}, checkFactory())
	if err == nil || !strings.Contains(err.Error(), `nothing to check called "deployer nova"; choose from: hinziel, tracker, deployer api, deployer web`) {
		t.Fatalf("got %v", err)
	}
	err = hinziel.Run(context.Background(), []string{"check", "--factory", "other"}, &bytes.Buffer{}, checkFactory())
	if err == nil || !strings.Contains(err.Error(), `unknown factory "other"`) {
		t.Fatalf("got %v", err)
	}
}

func TestRunCheckWithSelectorOnlyNotesMissingEnv(t *testing.T) {
	envcheck.Reset()
	defer envcheck.Reset()
	hinziel.Env("HINZIEL_TEST_MISSING")
	var out bytes.Buffer
	if err := hinziel.Run(context.Background(), []string{"check", "deployer api"}, &out, checkFactory()); err != nil {
		t.Fatalf("a missing variable must not fail a selected check: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "note  hinziel: missing environment variables: HINZIEL_TEST_MISSING") {
		t.Errorf("unexpected output:\n%s", out.String())
	}
}

func TestValidateRejectsBadAgentEnv(t *testing.T) {
	for _, name := range []string{"", "A=B", "HINZIEL_SOCKET"} {
		f := validFactory()
		f.AgentEnv = map[string]string{name: "x"}
		if err := validate(f); err == nil || !strings.Contains(err.Error(), "AgentEnv") {
			t.Errorf("AgentEnv %q: got %v", name, err)
		}
	}
}
