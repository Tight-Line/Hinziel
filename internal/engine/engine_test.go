package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/agent"
	"github.com/Tight-Line/hinziel/agent/agenttest"
	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy/deploytest"
	"github.com/Tight-Line/hinziel/dns/dnstest"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/env/envtest"
	"github.com/Tight-Line/hinziel/forge/forgetest"
	"github.com/Tight-Line/hinziel/internal/gittest"
	"github.com/Tight-Line/hinziel/internal/store"
	"github.com/Tight-Line/hinziel/notify"
	"github.com/Tight-Line/hinziel/notify/notifytest"
	"github.com/Tight-Line/hinziel/run"
	"github.com/Tight-Line/hinziel/tracker"
	"github.com/Tight-Line/hinziel/tracker/trackertest"
)

// fixture is a project built entirely from fakes: two repos ("api", changed
// by the agent, and "docs", left alone), an environment with DNS, a deployer
// per repo and a recording notifier.
type fixture struct {
	t       *testing.T
	ctx     context.Context
	eng     *Engine
	project *Project

	tracker  *trackertest.Fake
	api      *forgetest.Fake
	docs     *forgetest.Fake
	env      *envtest.Fake
	dns      *dnstest.Fake
	apiDep   *deploytest.Fake
	docsDep  *deploytest.Fake
	notifier *notifytest.Fake
	agent    *agenttest.Fake
}

type logWriter struct{ t *testing.T }

func (w logWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	tmp := t.TempDir()
	f := &fixture{t: t, ctx: context.Background()}

	f.tracker = trackertest.NewFake(tracker.Ticket{Key: "T-1", Title: "Add CSV export", URL: "https://tracker.test/T-1", Description: "Users want CSV."})
	var err error
	if f.api, err = forgetest.NewFake(filepath.Join(tmp, "api.git"), "main", map[string]string{"main.go": "package main\n"}); err != nil {
		t.Fatal(err)
	}
	if f.docs, err = forgetest.NewFake(filepath.Join(tmp, "docs.git"), "main", map[string]string{"README.md": "docs\n"}); err != nil {
		t.Fatal(err)
	}
	f.env = envtest.NewFake()
	f.dns = dnstest.NewFake()
	f.apiDep = &deploytest.Fake{URL: "https://api.test"}
	f.docsDep = &deploytest.Fake{}
	f.notifier = &notifytest.Fake{}
	f.agent = &agenttest.Fake{}

	f.project = &Project{
		Name:    "shop",
		Tracker: f.tracker,
		Repos: []Repo{
			{Name: "api", Forge: f.api, TargetBranch: "main"},
			{Name: "docs", Forge: f.docs, TargetBranch: "main"},
		},
		Agent:       f.agent,
		Provisioner: f.env,
		DNS:         f.dns,
		Endpoints: []Endpoint{{
			Name: "api",
			URL:  func(e *env.Environment) string { return "https://" + e.Name + "-api.preview.test" },
			DNS:  func(e *env.Environment) (string, string) { return "A", e.Hosts[0].Address },
		}},
		Deployers: []Deployer{
			{Name: "api", Repo: "api", Deployer: f.apiDep},
			{Name: "docs", Repo: "docs", Deployer: f.docsDep},
		},
		Notifiers: []Notifier{{Notifier: f.notifier}},
	}

	st, err := store.New(filepath.Join(tmp, "state"))
	if err != nil {
		t.Fatal(err)
	}
	f.eng = New(st, []*Project{f.project}, Options{
		Out:        logWriter{t},
		Bin:        "/usr/local/bin/shop-factory",
		RetryDelay: time.Millisecond,
	})
	return f
}

// start submits and executes a run for T-1.
func (f *fixture) start() (*run.Run, error) {
	f.t.Helper()
	r, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", Ticket: tracker.TicketRef{ID: "T-1"}})
	if err != nil {
		f.t.Fatal(err)
	}
	execErr := f.eng.Execute(f.ctx, r.ID)
	return f.load(r.ID), execErr
}

func (f *fixture) load(id run.ID) *run.Run {
	f.t.Helper()
	r, err := f.eng.Store().Load(f.ctx, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) step(r *run.Run, name string) run.StepRecord {
	for _, s := range r.Steps {
		if s.Name == name {
			return s
		}
	}
	f.t.Fatalf("no step %s", name)
	return run.StepRecord{}
}

// commitAndPush is what an agent does in a checkout: write, commit, push.
func commitAndPush(t *testing.T, dir, file, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "-m", "agent: " + file}, {"push", "--quiet"}} {
		if err := gittest.Git(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
}

func checkout(task *agent.Task, repo string) string {
	for _, c := range task.Repos {
		if c.Name == repo {
			return c.Path
		}
	}
	return ""
}

func envVar(task *agent.Task, name string) string {
	for _, kv := range slices.Backward(task.Env) {
		if v, ok := strings.CutPrefix(kv, name+"="); ok {
			return v
		}
	}
	return ""
}

// agentDeploy is the agent running `$HINZIEL_BIN deploy`.
func agentDeploy(ctx context.Context, task *agent.Task) (string, error) {
	return Call(ctx, envVar(task, "HINZIEL_SOCKET"), "deploy")
}

func TestAcceptanceTwoReposEndToEnd(t *testing.T) {
	f := newFixture(t)
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		api := checkout(task, "api")
		commitAndPush(t, api, "export.go", "package main // csv export\n")
		out, err := agentDeploy(ctx, task)
		if err != nil {
			t.Errorf("agent deploy: %v", err)
		}
		if !strings.Contains(out, "api: deployed") || !strings.Contains(out, "docs: deployed") {
			t.Errorf("deploy output: %q", out)
		}
		// Left uncommitted on purpose: finalize_branch must commit it.
		os.WriteFile(filepath.Join(api, "export_test.go"), []byte("package main\n"), 0o644)
		return &agent.Outcome{Summary: "Added CSV export.", ManualSteps: []string{"Enable the export flag in production"}}, nil
	}

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusSucceeded {
		t.Fatalf("status %s: %s", r.Status, r.Error)
	}
	for _, s := range r.Steps {
		want := stepDone
		if s.Name == StepRetrieve {
			want = stepSkipped // no deployer retrieves
		}
		if s.Status != want {
			t.Errorf("step %s is %s, want %s", s.Name, s.Status, want)
		}
	}

	// Branch and change requests: only the changed repo gets one.
	if r.Branch != "hinziel/t-1-add-csv-export" {
		t.Errorf("branch %q", r.Branch)
	}
	if crs := f.api.ChangeRequests(); len(crs) != 1 || crs[r.Branch].TargetBranch != "main" {
		t.Errorf("api change requests: %+v", crs)
	}
	if crs := f.docs.ChangeRequests(); len(crs) != 0 {
		t.Errorf("docs is unchanged but got change requests: %+v", crs)
	}
	if f.docs.BranchExists(r.Branch) {
		t.Error("the unchanged repo's run branch should have been deleted")
	}
	if _, err := gittest.Output(f.api.Dir, "show", r.Branch+":export_test.go"); err != nil {
		t.Errorf("uncommitted file was not committed and pushed: %v", err)
	}
	if len(r.ChangeRequests) != 1 || r.ChangeRequests["api"] == nil {
		t.Errorf("run change requests: %+v", r.ChangeRequests)
	}
	cr := r.ChangeRequests["api"]
	if desc := f.api.Input(cr.ID).Description.Body; !strings.Contains(desc, "Added CSV export.") || !strings.Contains(desc, "Enable the export flag") {
		t.Errorf("change request description: %q", desc)
	}

	// Environment, endpoints and DNS.
	wantURL := "https://hz-shop-t-1-api.preview.test"
	if r.Environment == nil || r.Environment.Endpoints["api"] != wantURL {
		t.Fatalf("environment: %+v", r.Environment)
	}
	if rec := f.dns.Records()["hz-shop-t-1-api.preview.test"]; rec.Type != "A" || rec.Value != "203.0.113.10" {
		t.Errorf("dns record: %+v", rec)
	}

	// Deploys: base, the agent's deploy, and the final deploy of the commit
	// finalize_branch made. docs never changed, so its final deploy is skipped.
	apiDeploys, docsDeploys := f.apiDep.Deployed(), f.docsDep.Deployed()
	if len(apiDeploys) != 3 {
		t.Fatalf("api deploys: %+v", apiDeploys)
	}
	if apiDeploys[2].Commit != r.Commits["api"] || apiDeploys[0].Commit == apiDeploys[1].Commit {
		t.Errorf("api deploys out of order: %+v (final commit %s)", apiDeploys, r.Commits["api"])
	}
	if len(docsDeploys) != 2 {
		t.Errorf("docs deploys: %+v", docsDeploys)
	}
	if r.Deployments["api"].Commit != r.Commits["api"] {
		t.Errorf("recorded deployment %+v", r.Deployments["api"])
	}

	// Ticket and notifications.
	if f.tracker.Stage("T-1") != tracker.StageInReview {
		t.Errorf("ticket stage %q", f.tracker.Stage("T-1"))
	}
	comments := f.tracker.Comments("T-1")
	if len(comments) != 1 || !strings.Contains(comments[0].Body, cr.URL) || !strings.Contains(comments[0].Body, wantURL) {
		t.Errorf("ticket comments: %+v", comments)
	}
	if !slices.ContainsFunc(f.tracker.Links("T-1"), func(l core.Link) bool { return l.URL == cr.URL }) {
		t.Errorf("ticket links: %+v", f.tracker.Links("T-1"))
	}
	if got := f.notifier.Types(); !slices.Equal(got, []notify.EventType{notify.EventRunStarted, notify.EventRunSucceeded}) {
		t.Errorf("events: %v", got)
	}

	// The agent got the workspace, the environment and the instructions.
	tasks := f.agent.Tasks()
	if len(tasks) != 1 {
		t.Fatalf("agent ran %d times", len(tasks))
	}
	task := tasks[0]
	if len(task.Repos) != 2 || task.Environment == nil || envVar(&task, "HINZIEL_BIN") != "/usr/local/bin/shop-factory" {
		t.Errorf("task: %+v", task)
	}
	for _, want := range []string{"T-1: Add CSV export", "Users want CSV.", r.Branch, "$HINZIEL_BIN deploy", wantURL} {
		if !strings.Contains(task.Prompt, want) {
			t.Errorf("prompt lacks %q:\n%s", want, task.Prompt)
		}
	}
}

func TestResumeAfterFailure(t *testing.T) {
	f := newFixture(t)
	f.env.EnsureErrs = []error{errors.New("droplet quota exceeded")}
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err == nil || !strings.Contains(err.Error(), "droplet quota") {
		t.Fatalf("expected the provisioning error, got %v", err)
	}
	if r.Status != run.StatusFailed || f.step(r, StepProvisionEnvironment).Status != stepFailed {
		t.Fatalf("status %s, step %+v", r.Status, f.step(r, StepProvisionEnvironment))
	}
	if !slices.Contains(f.notifier.Types(), notify.EventRunFailed) {
		t.Error("no run.failed event")
	}
	if f.tracker.Stage("T-1") != tracker.StageBlocked {
		t.Errorf("ticket stage %q", f.tracker.Stage("T-1"))
	}

	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	r = f.load(r.ID)
	if r.Status != run.StatusSucceeded {
		t.Fatalf("after resume: %s %s", r.Status, r.Error)
	}
	if f.tracker.Fetches() != 1 {
		t.Errorf("resuming a failed run must not fetch the ticket again (fetches: %d)", f.tracker.Fetches())
	}
	if f.env.Ensures() != 2 {
		t.Errorf("ensures: %d", f.env.Ensures())
	}
}

func TestRetryableErrorsAreRetried(t *testing.T) {
	f := newFixture(t)
	f.apiDep.DeployErrs = []error{&core.RetryableError{Err: errors.New("server busy")}}
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}
	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("%v %s", err, r.Status)
	}
}

func TestGatePausesUntilApproved(t *testing.T) {
	f := newFixture(t)
	f.project.Settings.Gates = []string{StepDeploy}
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusAwaitingApproval || r.CurrentStep != StepDeploy {
		t.Fatalf("status %s at %s", r.Status, r.CurrentStep)
	}
	if !slices.Contains(f.notifier.Types(), notify.EventRunAwaitingApproval) {
		t.Error("no run.awaiting_approval event")
	}
	if err := f.eng.Resume(f.ctx, r.ID); err == nil {
		t.Error("resume must not pass a gate")
	}
	if err := f.eng.Approve(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded {
		t.Fatalf("after approve: %s", r.Status)
	}
}

func TestNeedsInputThenResumeSeesTheAnswer(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		calls++
		if calls == 1 {
			return nil, &core.NeedsInputError{Question: "Which columns go in the CSV?"}
		}
		if !strings.Contains(task.Prompt, "Name and email") {
			t.Errorf("resumed agent doesn't see the answer:\n%s", task.Prompt)
		}
		commitAndPush(t, checkout(task, "api"), "export.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusNeedsInput {
		t.Fatalf("status %s", r.Status)
	}
	comments := f.tracker.Comments("T-1")
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "Which columns") {
		t.Fatalf("question not posted: %+v", comments)
	}

	f.tracker.AddComment("T-1", "pm", "Name and email")
	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded {
		t.Fatalf("after resume: %s %s", r.Status, r.Error)
	}
	if f.tracker.Fetches() != 2 {
		t.Errorf("resuming from needs_input must fetch the ticket again (fetches: %d)", f.tracker.Fetches())
	}
}

func TestNoChangesNeedsInput(t *testing.T) {
	f := newFixture(t)
	f.agent.Func = func(context.Context, *agent.Task) (*agent.Outcome, error) {
		return &agent.Outcome{Summary: "The export already exists."}, nil
	}
	r, err := f.start()
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != run.StatusNeedsInput || r.CurrentStep != StepFinalizeBranch {
		t.Fatalf("status %s at %s", r.Status, r.CurrentStep)
	}
	if !strings.Contains(r.Error, "The export already exists.") {
		t.Errorf("question should include the agent's summary: %q", r.Error)
	}
}

func TestDeployRefusesUncommittedAndUnpushedWork(t *testing.T) {
	f := newFixture(t)
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		api := checkout(task, "api")
		os.WriteFile(filepath.Join(api, "wip.go"), []byte("package main\n"), 0o644)
		if _, err := agentDeploy(ctx, task); err == nil || !strings.Contains(err.Error(), "uncommitted") {
			t.Errorf("deploy with uncommitted work: %v", err)
		}
		gittest.Git(api, "add", "--all")
		gittest.Git(api, "commit", "--quiet", "-m", "wip")
		if _, err := agentDeploy(ctx, task); err == nil || !strings.Contains(err.Error(), "not pushed") {
			t.Errorf("deploy with unpushed work: %v", err)
		}
		gittest.Git(api, "push", "--quiet")
		if _, err := agentDeploy(ctx, task); err != nil {
			t.Errorf("deploy of pushed work: %v", err)
		}
		return nil, nil
	}
	if r, err := f.start(); err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("%v %s", err, r.Status)
	}
}

func TestDeployRefusesUnretrievedEnvironmentChanges(t *testing.T) {
	f := newFixture(t)
	org := &deploytest.FakeRetriever{}
	f.project.Deployers[0].Deployer = org
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		if !strings.Contains(task.Prompt, "$HINZIEL_BIN retrieve") {
			t.Error("prompt doesn't explain retrieve")
		}
		org.SetPending("flows/Approval.flow-meta.xml", "<Flow/>") // the agent clicked in the org
		if _, err := agentDeploy(ctx, task); err == nil || !strings.Contains(err.Error(), "changes not in git") {
			t.Errorf("deploy over unretrieved clicks: %v", err)
		}
		// The guard already retrieved the click; the agent commits and pushes it.
		api := checkout(task, "api")
		if _, err := os.Stat(filepath.Join(api, "flows/Approval.flow-meta.xml")); err != nil {
			t.Errorf("click not retrieved: %v", err)
		}
		gittest.Git(api, "add", "--all")
		gittest.Git(api, "commit", "--quiet", "-m", "approval flow")
		gittest.Git(api, "push", "--quiet")
		if _, err := agentDeploy(ctx, task); err != nil {
			t.Errorf("deploy after committing the click: %v", err)
		}
		return nil, nil
	}
	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("%v %s", err, r.Status)
	}
	if _, err := gittest.Output(f.api.Dir, "show", r.Branch+":flows/Approval.flow-meta.xml"); err != nil {
		t.Errorf("click is not in the change: %v", err)
	}
}

func TestSecondRunForSameTicketIsRefused(t *testing.T) {
	f := newFixture(t)
	f.env.EnsureErrs = []error{errors.New("boom")}
	f.start()
	_, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", Ticket: tracker.TicketRef{ID: "T-1"}})
	if err == nil || !strings.Contains(err.Error(), "resume or cancel") {
		t.Fatalf("got %v", err)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	f.env.EnsureErrs = []error{errors.New("boom")}
	r, _ := f.start()
	if err := f.eng.Cancel(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusCancelled {
		t.Fatalf("status %s", r.Status)
	}
	if err := f.eng.Resume(f.ctx, r.ID); err == nil {
		t.Error("a cancelled run must not resume")
	}
	if _, err := f.eng.Submit(f.ctx, run.Request{Factory: "shop", Ticket: tracker.TicketRef{ID: "T-1"}}); err != nil {
		t.Errorf("a new run after cancelling: %v", err)
	}
}

func TestWithoutEnvironmentTheEnvironmentStepsAreSkipped(t *testing.T) {
	f := newFixture(t)
	f.project.Provisioner, f.project.DNS, f.project.Endpoints, f.project.Deployers = nil, nil, nil, nil
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		if task.Environment != nil {
			t.Error("agent got an environment")
		}
		if strings.Contains(task.Prompt, "HINZIEL_BIN deploy") {
			t.Error("prompt mentions deploy without an environment")
		}
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}
	r, err := f.start()
	if err != nil || r.Status != run.StatusSucceeded {
		t.Fatalf("%v %s", err, r.Status)
	}
	for _, name := range []string{StepProvisionEnvironment, StepDeployBase, StepRetrieve, StepDeploy} {
		if s := f.step(r, name); s.Status != stepSkipped {
			t.Errorf("%s is %s, want skipped", name, s.Status)
		}
	}
}

// A configuration error found while running (a variable the factory reads
// inside a func, say) fails the step that hit it, and resuming once it's
// fixed carries on from there.
func TestConfigErrorFailsTheStepThatHitIt(t *testing.T) {
	f := newFixture(t)
	var unusable error
	url := f.project.Endpoints[0].URL
	f.project.Endpoints[0].URL = func(e *env.Environment) string {
		unusable = errors.New("missing environment variables: API_HOST")
		return url(e)
	}
	f.eng.opts.Config = func() error { return unusable }
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		return nil, nil
	}

	r, err := f.start()
	if err == nil || !strings.Contains(err.Error(), "API_HOST") {
		t.Fatalf("expected the config error, got %v", err)
	}
	if got := f.step(r, StepProvisionEnvironment); got.Status != stepFailed {
		t.Fatalf("provision step %+v", got)
	}

	f.project.Endpoints[0].URL = url
	unusable = nil
	if err := f.eng.Resume(f.ctx, r.ID); err != nil {
		t.Fatal(err)
	}
	if r = f.load(r.ID); r.Status != run.StatusSucceeded {
		t.Fatalf("after resume: %s %s", r.Status, r.Error)
	}
}

// The agent's deploy command reports a configuration error at once, rather
// than the agent going on against a deployment that used it.
func TestConfigErrorFailsTheAgentsDeploy(t *testing.T) {
	f := newFixture(t)
	f.agent.Func = func(ctx context.Context, task *agent.Task) (*agent.Outcome, error) {
		commitAndPush(t, checkout(task, "api"), "fix.go", "package main\n")
		f.eng.opts.Config = func() error { return errors.New("missing environment variables: API_HOST") }
		if _, err := agentDeploy(ctx, task); err == nil || !strings.Contains(err.Error(), "API_HOST") {
			t.Errorf("deploy: %v", err)
		}
		return nil, nil
	}
	if _, err := f.start(); err == nil {
		t.Fatal("expected the run to fail")
	}
}
