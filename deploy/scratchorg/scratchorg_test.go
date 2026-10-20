package scratchorg

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/Tight-Line/hinziel/core"

	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/deploy/deploytest"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/env/envtest"
)

// The test binary doubles as a fake sf: with FAKE_SF_STATE set, it runs
// fakeSF instead of the tests. The state file is the fake Dev Hub and its
// scratch orgs.
func TestMain(m *testing.M) {
	if path := os.Getenv("FAKE_SF_STATE"); path != "" {
		os.Exit(fakeSF(path, os.Args[1:]))
	}
	os.Exit(m.Run())
}

type fakeOrg struct {
	OrgID, Username, Description string
	Active, LoggedIn             bool
	Packages, PermSets           []string
	Revision                     int // source tracking revision
	Deploys                      int
	Clicks                       []click
}

// click is a change made in the org by hand, at a tracking revision.
type click struct {
	Rev           int
	Path, Content string
}

type fakeState struct {
	Orgs         []*fakeOrg
	Calls        []string // "cwd|args"
	HubLoggedOut bool     // the Dev Hub needs logging in
	HubLogins    []string // SFDX auth URLs and aliases it was logged in with
}

// namedError is an sf error with sf's name for it.
type namedError struct{ name, msg string }

func (e namedError) Error() string { return e.msg }

const devHub = "hub"

func fakeSF(path string, args []string) int {
	var st fakeState
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &st)
	cwd, _ := os.Getwd()
	st.Calls = append(st.Calls, cwd+"|"+strings.Join(args, " "))
	result, err := fakeCommand(&st, cwd, args)
	data, _ = json.Marshal(&st)
	os.WriteFile(path, data, 0o644)
	if err != nil {
		name := "FakeError"
		if ne, ok := err.(namedError); ok {
			name = ne.name
		}
		json.NewEncoder(os.Stdout).Encode(map[string]any{"status": 1, "name": name, "message": err.Error(), "result": result})
		return 1
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"status": 0, "result": result})
	return 0
}

func flagValues(args []string, name string) []string {
	var out []string
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			out = append(out, args[i+1])
		}
	}
	return out
}

func flagValue(args []string, name string) string {
	if v := flagValues(args, name); len(v) > 0 {
		return v[0]
	}
	return ""
}

func (st *fakeState) org(username string) *fakeOrg {
	for _, o := range st.Orgs {
		if o.Username == username && o.Active {
			return o
		}
	}
	return nil
}

func fakeCommand(st *fakeState, cwd string, args []string) (any, error) {
	cmd := strings.Join(args, " ")
	target := flagValue(args, "--target-org")
	query := flagValue(args, "--query")
	switch {
	case strings.HasPrefix(cmd, "data query") && slices.Contains(args, "--use-tooling-api"):
		o := st.org(target)
		if o == nil {
			return nil, fmt.Errorf("no org %s", target)
		}
		var m any
		if o.Revision > 0 {
			m = o.Revision
		}
		return map[string]any{"records": []map[string]any{{"m": m}}}, nil
	case strings.HasPrefix(cmd, "org login sfdx-url "):
		data, err := os.ReadFile(flagValue(args, "--sfdx-url-file"))
		if err != nil {
			return nil, err
		}
		st.HubLogins = append(st.HubLogins, string(data)+" as "+flagValue(args, "--alias"))
		st.HubLoggedOut = false
		return map[string]string{"username": "admin@hub.example.com"}, nil
	case strings.HasPrefix(cmd, "data query") && target == devHub && st.HubLoggedOut:
		return nil, namedError{"NamedOrgNotFoundError", "No authorization information found for " + devHub + "."}
	case strings.HasPrefix(cmd, "data query"):
		if target != devHub {
			return nil, fmt.Errorf("%s is not a Dev Hub", target)
		}
		var recs []map[string]string
		for _, o := range st.Orgs {
			if !o.Active {
				continue
			}
			switch {
			case strings.Contains(query, "FROM ScratchOrgInfo"):
				recs = append(recs, map[string]string{"Id": "2SR" + o.OrgID[3:], "ScratchOrg": o.OrgID, "SignupUsername": o.Username, "Description": o.Description})
			case strings.Contains(query, "FROM ActiveScratchOrg") && strings.Contains(query, "'"+o.OrgID+"'"):
				recs = append(recs, map[string]string{"Id": "act-" + o.OrgID})
			}
		}
		return map[string]any{"records": recs}, nil
	case strings.HasPrefix(cmd, "data delete record "):
		for _, o := range st.Orgs {
			if "act-"+o.OrgID == flagValue(args, "--record-id") && o.Active {
				o.Active = false
				return map[string]string{"id": flagValue(args, "--record-id")}, nil
			}
		}
		return nil, fmt.Errorf("no record")
	case strings.HasPrefix(cmd, "org create scratch "):
		if flagValue(args, "--target-dev-hub") != devHub {
			return nil, fmt.Errorf("not a Dev Hub")
		}
		if _, err := os.Stat(flagValue(args, "--definition-file")); err != nil {
			return nil, err
		}
		n := len(st.Orgs) + 1
		o := &fakeOrg{OrgID: fmt.Sprintf("00D%012d", n), Username: fmt.Sprintf("test-%d@example.com", n), Description: flagValue(args, "--description"), Active: true, LoggedIn: true}
		st.Orgs = append(st.Orgs, o)
		return map[string]string{"orgId": o.OrgID + "AAA", "username": o.Username}, nil
	case strings.HasPrefix(cmd, "org display "):
		o := st.org(target)
		if o == nil || !o.LoggedIn {
			return nil, fmt.Errorf("no authorization information found for %s", target)
		}
		return map[string]string{"id": o.OrgID + "AAA", "username": o.Username, "instanceUrl": "https://" + strings.TrimSuffix(o.Username, "@example.com") + ".scratch.my.salesforce.com"}, nil
	case strings.HasPrefix(cmd, "org open "):
		o := st.org(target)
		if o == nil || !o.LoggedIn {
			return nil, fmt.Errorf("no authorization information found for %s", target)
		}
		return map[string]string{"url": "https://" + strings.TrimSuffix(o.Username, "@example.com") + ".scratch.my.salesforce.com/secur/frontdoor.jsp?sid=fake", "username": o.Username}, nil
	case strings.HasPrefix(cmd, "org logout "):
		for _, o := range st.Orgs {
			if o.Username == target {
				o.LoggedIn = false
			}
		}
		return nil, nil
	case strings.HasPrefix(cmd, "package installed list "):
		var out []map[string]string
		for _, p := range st.org(target).Packages {
			out = append(out, map[string]string{"SubscriberPackageVersionId": p})
		}
		return out, nil
	case strings.HasPrefix(cmd, "package install "):
		o := st.org(target)
		o.Packages = append(o.Packages, flagValue(args, "--package"))
		return nil, nil
	case strings.HasPrefix(cmd, "project deploy start "):
		o := st.org(target)
		if _, err := os.Stat(filepath.Join(cwd, "sfdx-project.json")); err != nil {
			return nil, fmt.Errorf("not in a project: %s", cwd)
		}
		for _, d := range flagValues(args, "--source-dir") {
			if _, err := os.Stat(filepath.Join(cwd, d)); err != nil {
				return nil, err
			}
		}
		o.Deploys++
		o.Revision += 3
		return nil, nil
	case strings.HasPrefix(cmd, "org assign permset "):
		o := st.org(target)
		var ok, failed []map[string]string
		for _, p := range flagValues(args, "--name") {
			switch {
			case p == "Missing":
				failed = append(failed, map[string]string{"name": target, "message": "Permission set not found in target org: Missing"})
			case slices.Contains(o.PermSets, p):
				failed = append(failed, map[string]string{"name": target, "message": "Duplicate PermissionSetAssignment.  Assignee: 005; Permission Set: 0PS"})
			default:
				o.PermSets = append(o.PermSets, p)
				ok = append(ok, map[string]string{"name": target, "value": p})
			}
		}
		res := map[string]any{"successes": ok, "failures": failed}
		if len(failed) > 0 {
			return res, fmt.Errorf("permission set assignment failed")
		}
		return res, nil
	case strings.HasPrefix(cmd, "project reset tracking "):
		os.MkdirAll(filepath.Join(cwd, ".sf"), 0o755)
		return nil, os.WriteFile(filepath.Join(cwd, ".sf", "fake-revision"), []byte(flagValue(args, "--revision")), 0o644)
	case strings.HasPrefix(cmd, "project retrieve start "):
		o := st.org(target)
		data, err := os.ReadFile(filepath.Join(cwd, ".sf", "fake-revision"))
		if err != nil {
			return nil, fmt.Errorf("no source tracking in %s", cwd)
		}
		var since int
		fmt.Sscan(string(data), &since)
		var files []string
		for _, c := range o.Clicks {
			if c.Rev > since {
				p := filepath.Join(cwd, c.Path)
				os.MkdirAll(filepath.Dir(p), 0o755)
				os.WriteFile(p, []byte(c.Content), 0o644)
				files = append(files, c.Path)
			}
		}
		return map[string]any{"files": files}, nil
	}
	return nil, fmt.Errorf("fake sf doesn't know %q", strings.Join(args, " "))
}

type fixture struct {
	t     *testing.T
	ctx   context.Context
	org   *Org
	state string
	log   string // what Setup and PostDeploy wrote
}

// newFixture builds an Org on the fake sf, with a definition file and a
// DX project in a checkout.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	f := &fixture{t: t, ctx: context.Background(), state: filepath.Join(dir, "sf-state.json"), log: filepath.Join(dir, "commands.log")}
	t.Setenv("FAKE_SF_STATE", f.state)
	os.MkdirAll(filepath.Join(dir, "factory", "config"), 0o755)
	def := filepath.Join(dir, "factory", "config", "project-scratch-def.json")
	os.WriteFile(def, []byte(`{"edition":"Developer"}`), 0o644)
	f.org = New(Options{
		DevHub:         devHub,
		Definition:     def,
		Packages:       []string{"04tKA000000cCA1YAM"},
		Dir:            "sf",
		PermissionSets: []string{"Admin_Extras", "Subscription_Admin"},
		Setup:          []string{`echo "setup $SF_TARGET_ORG" >> ` + f.log},
		PostDeploy:     []string{`echo "post-deploy $SF_TARGET_ORG" >> ` + f.log},
		SF:             []string{os.Args[0]},
	})
	return f
}

func (f *fixture) checkout() *deploy.Source {
	f.t.Helper()
	dir := f.t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sf", "force-app", "main", "default"), 0o755)
	os.WriteFile(filepath.Join(dir, "sf", "sfdx-project.json"), []byte(`{"packageDirectories":[{"path":"force-app","default":true}]}`), 0o644)
	return &deploy.Source{Repo: "salesforce", Workdir: dir, Branch: "hinziel/t-1", Commit: "0123456789abcdef"}
}

func (f *fixture) fake() *fakeState {
	f.t.Helper()
	var st fakeState
	data, err := os.ReadFile(f.state)
	if err != nil {
		return &st
	}
	if err := json.Unmarshal(data, &st); err != nil {
		f.t.Fatal(err)
	}
	return &st
}

func (f *fixture) save(st *fakeState) {
	data, _ := json.Marshal(st)
	os.WriteFile(f.state, data, 0o644)
}

// clickIn changes a file in the org by hand.
func (f *fixture) clickIn(username, path, content string) {
	st := f.fake()
	o := st.org(username)
	o.Revision++
	o.Clicks = append(o.Clicks, click{Rev: o.Revision, Path: path, Content: content})
	f.save(st)
}

func (f *fixture) calls(prefix string) []string {
	var out []string
	for _, c := range f.fake().Calls {
		if _, args, _ := strings.Cut(c, "|"); strings.HasPrefix(args, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// deploy deploys and merges the deployment's State into e, as the engine does.
func (f *fixture) deploy(e *env.Environment, src *deploy.Source) *deploy.Deployment {
	f.t.Helper()
	dep, err := f.org.Deploy(f.ctx, e, src)
	if err != nil {
		f.t.Fatal(err)
	}
	maps.Copy(e.State, dep.State)
	return dep
}

func TestOpenURL(t *testing.T) {
	f := newFixture(t)
	e, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-open"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := f.org.OpenURL(f.ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(u, e.Endpoints["org"]+"/secur/frontdoor.jsp?sid=") {
		t.Errorf("OpenURL = %q, want the org's frontdoor link", u)
	}
	if _, err := f.org.OpenURL(f.ctx, &env.Environment{Name: "hz-sf-none"}); err == nil {
		t.Error("OpenURL of an environment with no org user must fail")
	}
}

func TestConformance(t *testing.T) {
	f := newFixture(t)
	envtest.Run(t, f.org)

	e, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-conformance"})
	if err != nil {
		t.Fatal(err)
	}
	deploytest.Run(t, f.org, e, f.checkout())
}

func TestEnsureCreatesTheOrgOnceAndInstallsThePackages(t *testing.T) {
	f := newFixture(t)
	e, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != e.ID || e.ID != "00D000000000001" {
		t.Errorf("IDs %s, %s", e.ID, again.ID)
	}
	if e.State[StateUsername] != "test-1@example.com" || e.State[StateInstanceURL] != "https://test-1.scratch.my.salesforce.com" || e.Endpoints["org"] != e.State[StateInstanceURL] {
		t.Errorf("state %v", e.State)
	}
	creates := f.calls("org create scratch")
	if len(creates) != 1 {
		t.Fatalf("creates: %v", creates)
	}
	def := f.org.opts.Definition
	cwd, args, _ := strings.Cut(creates[0], "|")
	for _, want := range []string{"--definition-file " + def, "--description hinziel:hz-sf-t-1", "--alias hz-sf-t-1", "--duration-days 7", "--target-dev-hub hub"} {
		if !strings.Contains(args, want) {
			t.Errorf("create %q lacks %q", args, want)
		}
	}
	if cwd != filepath.Dir(def) {
		t.Errorf("create ran in %s, want next to the definition, so sf finds its project", cwd)
	}
	if installs := f.calls("package install "); len(installs) != 1 {
		t.Errorf("the package must be installed once: %v", installs)
	}
}

func TestEnsureNeedsTheOrgLoggedIn(t *testing.T) {
	f := newFixture(t)
	if _, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"}); err != nil {
		t.Fatal(err)
	}
	st := f.fake()
	st.Orgs[0].LoggedIn = false
	f.save(st)
	_, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	if err == nil || !strings.Contains(err.Error(), "isn't logged in on this host") {
		t.Errorf("want a clear error, got %v", err)
	}
}

func TestDeployRunsSetupOnceAndTheRestEveryTime(t *testing.T) {
	f := newFixture(t)
	e, err := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	if err != nil {
		t.Fatal(err)
	}
	src := f.checkout()
	dep := f.deploy(e, src)
	if dep.URL != "https://test-1.scratch.my.salesforce.com" || dep.Commit != src.Commit {
		t.Errorf("deployment %+v", dep)
	}
	f.deploy(e, src) // the permission sets are already assigned: not an error

	deploys := f.calls("project deploy start")
	if len(deploys) != 2 {
		t.Fatalf("deploys %v", deploys)
	}
	if cwd, args, _ := strings.Cut(deploys[0], "|"); cwd != filepath.Join(src.Workdir, "sf") || !strings.Contains(args, "--source-dir force-app") {
		t.Errorf("deploy %s", deploys[0])
	}
	log, _ := os.ReadFile(f.log)
	want := "setup test-1@example.com\npost-deploy test-1@example.com\npost-deploy test-1@example.com\n"
	if string(log) != want {
		t.Errorf("commands ran:\n%s\nwant\n%s", log, want)
	}
	if got := f.fake().Orgs[0].PermSets; !slices.Equal(got, []string{"Admin_Extras", "Subscription_Admin"}) {
		t.Errorf("permission sets %v", got)
	}
	if e.State[stateRevision] != "6" {
		t.Errorf("revision %q, want the org's after the second deploy", e.State[stateRevision])
	}
}

func TestDeployFailsOnAPermissionSetThatIsntThere(t *testing.T) {
	f := newFixture(t)
	f.org.opts.PermissionSets = []string{"Subscription_Admin", "Missing"}
	e, _ := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	_, err := f.org.Deploy(f.ctx, e, f.checkout())
	if err == nil || !strings.Contains(err.Error(), "Permission set not found in target org: Missing") {
		t.Errorf("got %v", err)
	}
}

func TestRetrieveBringsBackOnlyWhatChangedAfterTheLastDeploy(t *testing.T) {
	f := newFixture(t)
	e, _ := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	user := e.State[StateUsername]
	src := f.checkout()

	// Changed before the deploy, so the deploy overwrote it.
	f.clickIn(user, "force-app/main/default/flows/Old.flow-meta.xml", "old")
	f.deploy(e, src)
	f.clickIn(user, "force-app/main/default/flows/Kickoff.flow-meta.xml", "clicked")

	checkout := f.checkout().Workdir // the agent's, never synced with the org
	if err := f.org.Retrieve(f.ctx, e, checkout); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(checkout, "sf", "force-app", "main", "default", "flows")
	if data, _ := os.ReadFile(filepath.Join(dir, "Kickoff.flow-meta.xml")); string(data) != "clicked" {
		t.Errorf("the click after the deploy must come back: %q", data)
	}
	if _, err := os.Stat(filepath.Join(dir, "Old.flow-meta.xml")); err == nil {
		t.Error("a change the deploy overwrote came back")
	}
}

func TestDestroyOnlyTouchesItsOwnOrgs(t *testing.T) {
	f := newFixture(t)
	e, _ := f.org.Ensure(f.ctx, env.Spec{Name: "hz-sf-t-1"})
	st := f.fake()
	st.Orgs = append(st.Orgs, &fakeOrg{OrgID: "00D000000000099", Username: "dev@example.com", Description: "Ana's org for hz-sf-t-1", Active: true, LoggedIn: true})
	f.save(st)

	envs, err := f.org.List(f.ctx)
	if err != nil || len(envs) != 1 || envs[0].Name != "hz-sf-t-1" || envs[0].ID != e.ID {
		t.Fatalf("list %+v, %v", envs, err)
	}
	// By name only, as after losing the run's state.
	if err := f.org.Destroy(f.ctx, &env.Environment{Name: "hz-sf-t-1"}); err != nil {
		t.Fatal(err)
	}
	st = f.fake()
	if st.Orgs[0].Active || st.Orgs[0].LoggedIn {
		t.Errorf("the org must be deleted and logged out: %+v", st.Orgs[0])
	}
	if !st.Orgs[1].Active {
		t.Error("an org Hinziel didn't create was deleted")
	}
	if err := f.org.Destroy(f.ctx, e); err != nil {
		t.Errorf("destroying again: %v", err)
	}
}

func TestCheck(t *testing.T) {
	f := newFixture(t)
	if err := f.org.Check(f.ctx); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{DevHub: "not-a-hub", Definition: "/nonexistent.json", DurationDays: 31, SF: f.org.opts.SF})
	err := bad.Check(f.ctx)
	for _, want := range []string{"DurationDays is 31", "scratch org definition", "not-a-hub is not a Dev Hub"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("check must say %q: %v", want, err)
		}
	}
}

func TestCheckNamesMissingTools(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := New(Options{DevHub: "hub", Definition: "def.json", Setup: []string{"true"}}).Check(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not installed on this host (or not on PATH): sf, sh") {
		t.Errorf("want missing sf, sh, got %v", err)
	}
}

// integration returns an Org on a real Dev Hub. Each test using it creates
// one scratch org, which counts against the Dev Hub's daily limit.
func integration(t *testing.T) (*Org, string) {
	t.Helper()
	if os.Getenv("HINZIEL_IT_SCRATCHORG") == "" {
		t.Skip("set HINZIEL_IT_SCRATCHORG=1, SF_DEVHUB (alias) and SF_PROJECT (a DX project with config/project-scratch-def.json); optionally SF_PACKAGES and SF_PERMSETS (comma-separated) and SF_SETUP (a command)")
	}
	t.Setenv("FAKE_SF_STATE", "")
	project := os.Getenv("SF_PROJECT")
	split := func(s string) []string {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	opts := Options{
		DevHub:         os.Getenv("SF_DEVHUB"),
		Definition:     filepath.Join(project, "config", "project-scratch-def.json"),
		DurationDays:   1,
		Packages:       split(os.Getenv("SF_PACKAGES")),
		PermissionSets: split(os.Getenv("SF_PERMSETS")),
	}
	if s := os.Getenv("SF_SETUP"); s != "" {
		opts.Setup = []string{s}
	}
	org := New(opts)
	if err := org.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	return org, project
}

func TestIntegrationProvisioner(t *testing.T) {
	org, _ := integration(t)
	envtest.Run(t, org)
}

// TestIntegrationDeploy deploys the project, then changes the org by hand
// and retrieves the change.
func TestIntegrationDeploy(t *testing.T) {
	org, project := integration(t)
	ctx := context.Background()

	// A copy of the project stands for the checkouts, so nothing is
	// retrieved into the original.
	checkout := func() string {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(project)); err != nil {
			t.Fatal(err)
		}
		for _, d := range []string{".git", ".sf", ".sfdx", "node_modules"} {
			os.RemoveAll(filepath.Join(dir, d))
		}
		return dir
	}
	spec := env.Spec{Name: "skit-" + strings.ToLower(fmt.Sprint(os.Getpid()))}
	t.Cleanup(func() { org.Destroy(context.Background(), &env.Environment{Name: spec.Name}) })
	e, err := org.Ensure(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	src := &deploy.Source{Repo: "salesforce", Workdir: checkout(), Branch: "main", Commit: "0123456789abcdef"}
	dep, err := org.Deploy(ctx, e, src)
	if err != nil {
		t.Fatal(err)
	}
	maps.Copy(e.State, dep.State)
	deploytest.Run(t, org, e, src)
	// deploytest.Run doesn't keep the State its deploys return, as the
	// engine would; deploy once more and keep it.
	if dep, err = org.Deploy(ctx, e, src); err != nil {
		t.Fatal(err)
	}
	maps.Copy(e.State, dep.State)

	t.Run("RetrieveBringsBackAClick", func(t *testing.T) {
		label := "Hinziel_Click_" + fmt.Sprint(os.Getpid())
		err := org.sf(ctx, "", nil, "data", "create", "record", "--target-org", e.State[StateUsername], "--use-tooling-api", "--sobject", "ExternalString",
			"--values", "Name="+label+" MasterLabel="+label+" Value=clicked Language=en_US")
		if err != nil {
			t.Fatal(err)
		}
		dir := checkout()
		if err := org.Retrieve(ctx, e, dir); err != nil {
			t.Fatal(err)
		}
		var changed []string
		filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || strings.Contains(path, "/.sf/") {
				return err
			}
			orig, _ := os.ReadFile(filepath.Join(project, strings.TrimPrefix(path, dir)))
			if now, _ := os.ReadFile(path); string(now) != string(orig) {
				changed = append(changed, strings.TrimPrefix(path, dir))
			}
			return nil
		})
		labels, _ := os.ReadFile(filepath.Join(dir, "force-app", "main", "default", "labels", "CustomLabels.labels-meta.xml"))
		if !strings.Contains(string(labels), label) {
			t.Errorf("the label must come back; changed files: %v", changed)
		}
		if len(changed) != 1 {
			t.Errorf("only the click must come back, got %v", changed)
		}
	})
}

// fakeSalesforceLogin is Salesforce's token endpoint: it takes the code
// "good-code" with the verifier that matches the challenge in the last link.
type fakeSalesforceLogin struct {
	challenge string
}

func (l *fakeSalesforceLogin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if r.URL.Path != "/services/oauth2/token" || r.PostForm.Get("client_id") != "PlatformCLI" ||
		r.PostForm.Get("code") != "good-code" || base64.RawURLEncoding.EncodeToString(sum[:]) != l.challenge {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"expired authorization code"}`))
		return
	}
	w.Write([]byte(`{"access_token":"at","refresh_token":"rt-123","instance_url":"https://hub.my.salesforce.com"}`))
}

func TestEnsureAsksForADevHubLogin(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	login := &fakeSalesforceLogin{}
	srv := httptest.NewServer(login)
	defer srv.Close()
	f.org.opts.LoginURL = srv.URL
	f.save(&fakeState{HubLoggedOut: true})
	spec := env.Spec{Name: "hz-t-1"}

	// ask returns the login link Ensure asks with, and remembers its challenge.
	ask := func(ctx context.Context, want string) url.Values {
		t.Helper()
		_, err := f.org.Ensure(ctx, spec)
		var needs *core.NeedsInputError
		if !errors.As(err, &needs) || needs.ID != "scratchorg.login.hub" || !strings.Contains(needs.Question, want) {
			t.Fatalf("Ensure must ask for a login (%q): %v", want, err)
		}
		m := regexp.MustCompile(`\]\((http[^)]+)\)`).FindStringSubmatch(needs.Question)
		u, _ := url.Parse(m[1])
		q := u.Query()
		if !strings.HasPrefix(m[1], srv.URL+"/services/oauth2/authorize?") || q.Get("client_id") != "PlatformCLI" ||
			q.Get("redirect_uri") != "http://localhost:1717/OauthRedirect" || q.Get("code_challenge") == "" {
			t.Fatalf("login link %s", m[1])
		}
		login.challenge = q.Get("code_challenge")
		return q
	}
	answer := func(text string) context.Context {
		return core.WithAnswers(f.ctx, map[string]string{"scratchorg.login.hub": text})
	}
	redirect := func(code, state string) string {
		return "@hinziel http://localhost:1717/OauthRedirect?code=" + url.QueryEscape(code) + "&state=" + state + " thanks"
	}

	first := ask(f.ctx, "isn't logged in")
	ask(answer("I logged in!"), "has no address")
	second := ask(answer(redirect("good-code", first.Get("state"))), "older login link")
	third := ask(answer(redirect("old-code", second.Get("state"))), "may have expired")
	if len(f.fake().HubLogins) > 0 {
		t.Fatal("no login may happen before a good answer")
	}

	e, err := f.org.Ensure(answer(redirect("good-code", third.Get("state"))), spec)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"force://PlatformCLI::rt-123@hub.my.salesforce.com as hub"}; !slices.Equal(f.fake().HubLogins, want) {
		t.Errorf("logins %q, want %q", f.fake().HubLogins, want)
	}
	if e.State[StateUsername] == "" || len(f.calls("org create scratch")) != 1 {
		t.Errorf("after the login, the org must be created: %+v", e)
	}
	if path, _ := f.org.loginFile(); fileExists(path) {
		t.Error("the pending login must be gone once used")
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "hinziel-sfdx-url-*")); len(left) > 0 {
		t.Errorf("the refresh token must not be left on disk: %v", left)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestLoginCommand(t *testing.T) {
	f := newFixture(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	login := &fakeSalesforceLogin{}
	srv := httptest.NewServer(login)
	defer srv.Close()
	f.org.opts.LoginURL = srv.URL
	f.save(&fakeState{HubLoggedOut: true})

	if in, err := f.org.LoggedIn(f.ctx); in || err != nil {
		t.Fatalf("LoggedIn = %v, %v; want false", in, err)
	}
	how, err := f.org.StartLogin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(regexp.MustCompile(`https?://\S+authorize\S+`).FindString(how))
	if err != nil || u.Query().Get("state") == "" {
		t.Fatalf("no login link in:\n%s", how)
	}
	login.challenge = u.Query().Get("code_challenge")
	if err := f.org.FinishLogin(f.ctx, "http://localhost:1717/OauthRedirect?code=good-code&state="+u.Query().Get("state")+"\n"); err != nil {
		t.Fatal(err)
	}
	if in, err := f.org.LoggedIn(f.ctx); !in || err != nil {
		t.Errorf("after the login, LoggedIn = %v, %v", in, err)
	}
}
