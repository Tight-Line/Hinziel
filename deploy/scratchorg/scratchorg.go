// Package scratchorg gives every environment its own Salesforce scratch org,
// deploys a Salesforce DX project to it, and retrieves what was changed in
// the org by hand (clicks in Setup) back into the working tree. One Org is
// the env.Provisioner, the deploy.Deployer and the deploy.Retriever.
//
// Everything goes through the sf CLI on the factory's host. Orgs are created
// from the team's own scratch org definition, and each one's Dev Hub record
// (ScratchOrgInfo) carries "hinziel:<environment name>" as its description,
// so Ensure finds an org again from any run, and Destroy and List never touch
// an org Hinziel didn't create. On the host, the org's sf alias is the
// environment's name, and its URL is the environment's "org" endpoint.
//
// Deploy deploys the project's package directories, assigns the permission
// sets, runs Setup once per org (loading data isn't safe to repeat) and then
// PostDeploy. It then remembers the org's latest source tracking revision.
// Retrieve brings back only the changes made after it, using sf's own source
// tracking in the checkout, so what a deploy itself touched (such as the
// Admin profile) doesn't come back as a change. The repository's .forceignore
// applies, and .sf/ must be git-ignored, as the Salesforce DX template does.
// Components deleted in the org aren't deleted from the working tree.
//
// # Credentials
//
//   - A Dev Hub logged in on the factory's host under the alias given as
//     DevHub. Turn Dev Hub on in the org that will own the scratch orgs
//     (Setup → Dev Hub), then log in: on a host without a browser with
//     `sf org login jwt --username <user> --jwt-key-file <key>
//     --client-id <consumer key> --alias <DevHub>`, using a connected app
//     with a certificate (Setup → App Manager → New Connected App: "Use
//     digital signatures", OAuth scopes "api" and "refresh_token", and the
//     user pre-authorised through a profile or permission set); on a
//     developer's machine `sf org login web --alias <DevHub>` is enough.
//     The least access that works is a user with the "Salesforce DX" or
//     "Dev Hub" permission set licence and object access to
//     ScratchOrgInfo and ActiveScratchOrg (create, read, edit, delete).
//   - Without a Dev Hub logged in (a fresh host, or a login that expired),
//     Ensure asks for one through the run's ticket: a login link, and the
//     address the browser ends on as the answer, from which it logs sf in
//     as DevHub. That needs no connected app in the org; see login.go.
//   - The Dev Hub's limits decide how many environments can exist: a free
//     Developer Edition allows 3 active scratch orgs and 6 new ones a day.
//   - Scratch orgs are logged in on the host when they're created, so they
//     need nothing else.
package scratchorg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
)

// Options configure the scratch orgs and the deploy.
type Options struct {
	// DevHub is the sf alias or username of the Dev Hub, logged in on the
	// factory's host.
	DevHub string
	// Definition is the scratch org definition file on the factory's host,
	// usually the project's config/project-scratch-def.json.
	Definition string
	// DurationDays is how long an org lives before Salesforce deletes it,
	// 1 to 30; 0 = 7. A run that finds its org expired gets a new one.
	DurationDays int
	// Packages are package version IDs (04t…) Ensure installs, in order,
	// before anything is deployed.
	Packages []string
	// Dir is the directory in the repository holding sfdx-project.json;
	// "" = the root.
	Dir string
	// PermissionSets are assigned to the org's admin after every deploy.
	PermissionSets []string
	// Setup are shell commands run once per org, after its first deploy, in
	// Dir, with SF_TARGET_ORG set to the org: loading data, say
	// "sf data import tree --plan data/plan.json".
	Setup []string
	// PostDeploy are shell commands run after every deploy, like Setup.
	// They must be safe to repeat.
	PostDeploy []string
	// SF is the sf command; nil = {"sf"}.
	SF []string
	// Wait bounds creating an org, installing a package and deploying;
	// 0 = 30 minutes.
	Wait time.Duration
	// LoginURL is where the Dev Hub's users log in, for the login Ensure
	// asks a human for when the Dev Hub isn't logged in (see Credentials);
	// "" = https://login.salesforce.com.
	LoginURL string
}

// Keys in Environment.State.
const (
	StateUsername    = "scratchorg.username"
	StateInstanceURL = "scratchorg.instance_url"
	stateSetup       = "scratchorg.setup"    // "done" once Setup ran
	stateRevision    = "scratchorg.revision" // source tracking revision after the last deploy
)

// descPrefix marks the Dev Hub records of the orgs this package creates.
const descPrefix = "hinziel:"

// Org is a scratch org provisioner, deployer and retriever.
type Org struct {
	opts Options
}

var (
	_ env.Provisioner  = (*Org)(nil)
	_ env.Lister       = (*Org)(nil)
	_ deploy.Deployer  = (*Org)(nil)
	_ deploy.Retriever = (*Org)(nil)
	_ core.Checker     = (*Org)(nil)
)

// New returns a scratch org component.
func New(opts Options) *Org {
	return &Org{opts: opts}
}

// scratchOrgInfo is a Dev Hub's record of one scratch org.
type scratchOrgInfo struct {
	ID             string `json:"Id"`
	ScratchOrg     string `json:"ScratchOrg"` // the org's 15-character ID
	SignupUsername string `json:"SignupUsername"`
	Description    string `json:"Description"`
}

func (i *scratchOrgInfo) envName() string { return strings.TrimPrefix(i.Description, descPrefix) }

// active returns the Dev Hub's active orgs that this package created.
// Description can't be filtered in SOQL, so the filtering is done here.
func (o *Org) active(ctx context.Context) ([]scratchOrgInfo, error) {
	var res struct {
		Records []scratchOrgInfo `json:"records"`
	}
	q := "SELECT Id, ScratchOrg, SignupUsername, Description FROM ScratchOrgInfo WHERE Status = 'Active' ORDER BY CreatedDate DESC"
	if err := o.sf(ctx, "", &res, "data", "query", "--target-org", o.opts.DevHub, "--query", q); err != nil {
		return nil, fmt.Errorf("listing scratch orgs in Dev Hub %s: %w", o.opts.DevHub, err)
	}
	var out []scratchOrgInfo
	for _, r := range res.Records {
		if strings.HasPrefix(r.Description, descPrefix) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (o *Org) find(ctx context.Context, name string) (*scratchOrgInfo, error) {
	infos, err := o.active(ctx)
	if err != nil {
		return nil, err
	}
	for i := range infos {
		if infos[i].envName() == name {
			return &infos[i], nil
		}
	}
	return nil, nil
}

// Ensure returns the active scratch org of the environment, or creates one,
// then installs the packages it doesn't have yet. When the Dev Hub isn't
// logged in, it asks a human for a login first (core.NeedsInputError).
func (o *Org) Ensure(ctx context.Context, spec env.Spec) (*env.Environment, error) {
	info, err := o.find(ctx, spec.Name)
	if needsLogin(err) {
		if err = o.devHubLogin(ctx, err); err == nil {
			info, err = o.find(ctx, spec.Name)
		}
	}
	if err != nil {
		return nil, err
	}
	if info == nil {
		if err := o.create(ctx, spec.Name); err != nil {
			return nil, err
		}
		if info, err = o.find(ctx, spec.Name); err != nil {
			return nil, err
		}
		if info == nil {
			return nil, fmt.Errorf("scratch org for %s was created, but Dev Hub %s doesn't list it as active", spec.Name, o.opts.DevHub)
		}
	}
	var org struct {
		InstanceURL string `json:"instanceUrl"`
	}
	if err := o.sf(ctx, "", &org, "org", "display", "--target-org", info.SignupUsername); err != nil {
		return nil, fmt.Errorf("scratch org %s (%s) exists, but isn't logged in on this host; destroy the environment to start over: %w", spec.Name, info.SignupUsername, err)
	}
	if err := o.installPackages(ctx, info.SignupUsername); err != nil {
		return nil, err
	}
	return &env.Environment{
		Name:      spec.Name,
		Provider:  "scratchorg",
		ID:        info.ScratchOrg,
		Endpoints: map[string]string{"org": org.InstanceURL},
		State:     map[string]string{StateUsername: info.SignupUsername, StateInstanceURL: org.InstanceURL},
	}, nil
}

func (o *Org) create(ctx context.Context, name string) error {
	def, err := filepath.Abs(o.opts.Definition)
	if err != nil {
		return err
	}
	days := o.opts.DurationDays
	if days == 0 {
		days = 7
	}
	// Run next to the definition, so sf finds the project it belongs to.
	err = o.sf(ctx, filepath.Dir(def), nil, "org", "create", "scratch",
		"--target-dev-hub", o.opts.DevHub, "--definition-file", def,
		"--duration-days", strconv.Itoa(days), "--alias", name,
		"--description", descPrefix+name, "--wait", o.waitMinutes())
	if err != nil {
		return fmt.Errorf("creating scratch org %s: %w", name, err)
	}
	return nil
}

func (o *Org) installPackages(ctx context.Context, username string) error {
	if len(o.opts.Packages) == 0 {
		return nil
	}
	var installed []struct {
		VersionID string `json:"SubscriberPackageVersionId"`
	}
	if err := o.sf(ctx, "", &installed, "package", "installed", "list", "--target-org", username); err != nil {
		return fmt.Errorf("listing installed packages: %w", err)
	}
	have := map[string]bool{}
	for _, i := range installed {
		have[i.VersionID[:min(15, len(i.VersionID))]] = true
	}
	for _, p := range o.opts.Packages {
		if have[p[:min(15, len(p))]] {
			continue
		}
		if err := o.sf(ctx, "", nil, "package", "install", "--target-org", username, "--package", p, "--wait", o.waitMinutes(), "--no-prompt"); err != nil {
			return fmt.Errorf("installing package %s: %w", p, err)
		}
	}
	return nil
}

// Destroy deletes the environment's scratch org through the Dev Hub, which
// works whether or not the org is logged in on this host, then forgets its
// login.
func (o *Org) Destroy(ctx context.Context, e *env.Environment) error {
	infos, err := o.active(ctx)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(infos, func(i scratchOrgInfo) bool {
		return (e.ID != "" && sameID(i.ScratchOrg, e.ID)) || i.envName() == e.Name
	})
	if i < 0 {
		return nil
	}
	info := infos[i]
	var res struct {
		Records []struct {
			ID string `json:"Id"`
		} `json:"records"`
	}
	q := "SELECT Id FROM ActiveScratchOrg WHERE ScratchOrg = '" + info.ScratchOrg + "'"
	if err := o.sf(ctx, "", &res, "data", "query", "--target-org", o.opts.DevHub, "--query", q); err != nil {
		return fmt.Errorf("finding scratch org %s in Dev Hub %s: %w", e.Name, o.opts.DevHub, err)
	}
	for _, r := range res.Records {
		if err := o.sf(ctx, "", nil, "data", "delete", "record", "--target-org", o.opts.DevHub, "--sobject", "ActiveScratchOrg", "--record-id", r.ID); err != nil {
			return fmt.Errorf("deleting scratch org %s: %w", e.Name, err)
		}
	}
	// The org is gone; its local login only clutters `sf org list`.
	_ = o.sf(ctx, "", nil, "org", "logout", "--target-org", info.SignupUsername, "--no-prompt")
	return nil
}

// List returns every active scratch org this package created in the Dev Hub.
func (o *Org) List(ctx context.Context) ([]env.Environment, error) {
	infos, err := o.active(ctx)
	if err != nil {
		return nil, err
	}
	var out []env.Environment
	for _, i := range infos {
		out = append(out, env.Environment{Name: i.envName(), Provider: "scratchorg", ID: i.ScratchOrg, State: map[string]string{StateUsername: i.SignupUsername}})
	}
	return out, nil
}

// Deploy deploys the project's package directories, assigns the permission
// sets, runs Setup if it hasn't run in this org yet, then PostDeploy, and
// remembers the org's source tracking revision for Retrieve.
func (o *Org) Deploy(ctx context.Context, e *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	user := e.State[StateUsername]
	if user == "" {
		return nil, fmt.Errorf("environment %s has no scratch org username; was it provisioned by scratchorg?", e.Name)
	}
	dir := filepath.Join(src.Workdir, o.opts.Dir)
	pkgDirs, err := packageDirs(dir)
	if err != nil {
		return nil, err
	}
	args := []string{"project", "deploy", "start", "--target-org", user, "--ignore-conflicts", "--wait", o.waitMinutes()}
	for _, d := range pkgDirs {
		args = append(args, "--source-dir", d)
	}
	if err := o.sf(ctx, dir, nil, args...); err != nil {
		return nil, fmt.Errorf("deploying to %s: %w", e.Name, err)
	}
	if err := o.assignPermissionSets(ctx, user); err != nil {
		return nil, err
	}
	state := map[string]string{}
	if e.State[stateSetup] != "done" {
		if err := o.shell(ctx, dir, user, o.opts.Setup); err != nil {
			return nil, fmt.Errorf("setup: %w", err)
		}
		state[stateSetup] = "done"
	}
	if err := o.shell(ctx, dir, user, o.opts.PostDeploy); err != nil {
		return nil, fmt.Errorf("post-deploy: %w", err)
	}
	rev, err := o.revision(ctx, user)
	if err != nil {
		return nil, err
	}
	state[stateRevision] = rev
	return &deploy.Deployment{
		URL:    e.State[StateInstanceURL],
		Commit: src.Commit,
		At:     time.Now(),
		Info:   map[string]string{"username": user, "org_id": e.ID},
		State:  state,
	}, nil
}

// packageDirs reads the package directories from sfdx-project.json.
func packageDirs(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "sfdx-project.json"))
	if err != nil {
		return nil, fmt.Errorf("not a Salesforce DX project: %w", err)
	}
	var p struct {
		PackageDirectories []struct {
			Path string `json:"path"`
		} `json:"packageDirectories"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("sfdx-project.json: %w", err)
	}
	var out []string
	for _, d := range p.PackageDirectories {
		out = append(out, d.Path)
	}
	if len(out) == 0 {
		return nil, errors.New("sfdx-project.json has no packageDirectories")
	}
	return out, nil
}

// assignPermissionSets assigns them all; one that's already assigned isn't
// an error, so it's safe after every deploy.
func (o *Org) assignPermissionSets(ctx context.Context, user string) error {
	if len(o.opts.PermissionSets) == 0 {
		return nil
	}
	args := []string{"org", "assign", "permset", "--target-org", user}
	for _, p := range o.opts.PermissionSets {
		args = append(args, "--name", p)
	}
	var res struct {
		Failures []struct {
			Message string `json:"message"`
		} `json:"failures"`
	}
	err := o.sf(ctx, "", &res, args...)
	var msgs []string
	for _, f := range res.Failures {
		if !strings.Contains(f.Message, "Duplicate PermissionSetAssignment") {
			msgs = append(msgs, f.Message)
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("assigning permission sets: %s", strings.Join(msgs, "; "))
	}
	if err != nil && len(res.Failures) == 0 {
		return fmt.Errorf("assigning permission sets: %w", err)
	}
	return nil
}

// revision is the org's latest source tracking revision.
func (o *Org) revision(ctx context.Context, user string) (string, error) {
	var res struct {
		Records []struct {
			Max *float64 `json:"m"`
		} `json:"records"`
	}
	if err := o.sf(ctx, "", &res, "data", "query", "--target-org", user, "--use-tooling-api", "--query", "SELECT MAX(RevisionCounter) m FROM SourceMember"); err != nil {
		return "", fmt.Errorf("reading the source tracking revision: %w", err)
	}
	if len(res.Records) == 0 || res.Records[0].Max == nil {
		return "0", nil
	}
	return strconv.FormatInt(int64(*res.Records[0].Max), 10), nil
}

// Retrieve brings the changes made in the org since the last deploy into
// workdir: sf's source tracking there is reset to the revision Deploy
// remembered, then a tracked retrieve fetches what changed after it.
func (o *Org) Retrieve(ctx context.Context, e *env.Environment, workdir string) error {
	user := e.State[StateUsername]
	if user == "" {
		return fmt.Errorf("environment %s has no scratch org username; was it provisioned by scratchorg?", e.Name)
	}
	rev := e.State[stateRevision]
	if rev == "" {
		rev = "0" // never deployed: everything is a change
	}
	dir := filepath.Join(workdir, o.opts.Dir)
	if err := o.sf(ctx, dir, nil, "project", "reset", "tracking", "--target-org", user, "--revision", rev, "--no-prompt"); err != nil {
		return fmt.Errorf("resetting source tracking: %w", err)
	}
	if err := o.sf(ctx, dir, nil, "project", "retrieve", "start", "--target-org", user, "--ignore-conflicts", "--wait", o.waitMinutes()); err != nil {
		return fmt.Errorf("retrieving from %s: %w", e.Name, err)
	}
	return nil
}

// Check verifies the options, that the definition file is readable, and
// that the Dev Hub is logged in and has Dev Hub turned on.
func (o *Org) Check(ctx context.Context) error {
	tools := []string{o.sfCommand()[0]}
	if len(o.opts.Setup)+len(o.opts.PostDeploy) > 0 {
		tools = append(tools, "sh")
	}
	errs := []error{core.NeedTools(tools...)}
	if err := core.NeedSettings(map[string]string{"DevHub": o.opts.DevHub, "Definition": o.opts.Definition}); err != nil {
		return errors.Join(append(errs, err)...)
	}
	if d := o.opts.DurationDays; d < 0 || d > 30 {
		errs = append(errs, fmt.Errorf("DurationDays is %d; it must be 1 to 30 (0 = 7)", d))
	}
	if _, err := os.ReadFile(o.opts.Definition); err != nil {
		errs = append(errs, fmt.Errorf("scratch org definition: %w", err))
	}
	if errs[0] == nil {
		if _, err := o.active(ctx); needsLogin(err) {
			errs = append(errs, fmt.Errorf("Dev Hub %s isn't logged in on this host: the first run will ask for a login on its ticket, or log in with `sf org login web --alias %s` (%w)", o.opts.DevHub, o.opts.DevHub, err))
		} else if err != nil {
			errs = append(errs, fmt.Errorf("%w (is %s logged in on this host, with Dev Hub turned on?)", err, o.opts.DevHub))
		}
	}
	return errors.Join(errs...)
}

func (o *Org) sfCommand() []string {
	if len(o.opts.SF) > 0 {
		return o.opts.SF
	}
	return []string{"sf"}
}

func (o *Org) waitMinutes() string {
	w := o.opts.Wait
	if w == 0 {
		w = 30 * time.Minute
	}
	return strconv.Itoa(max(1, int(w.Minutes())))
}

// sf runs an sf command with --json in dir ("" = the current directory)
// and decodes its result into out, if given. On failure the error carries
// sf's message.
func (o *Org) sf(ctx context.Context, dir string, out any, args ...string) error {
	cmd := o.sfCommand()
	c := exec.CommandContext(ctx, cmd[0], append(append(slices.Clone(cmd[1:]), args...), "--json")...)
	c.Dir = dir
	c.Env = append(os.Environ(), "SF_DISABLE_TELEMETRY=true", "SF_AUTOUPDATE_DISABLE=true")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	runErr := c.Run()
	var resp struct {
		Status  int             `json:"status"`
		Name    string          `json:"name"`
		Message string          `json:"message"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		if runErr != nil {
			return fmt.Errorf("sf %s: %w\n%s", strings.Join(args[:min(3, len(args))], " "), runErr, tail(stdout.String()+stderr.String()))
		}
		return fmt.Errorf("sf %s: unexpected output: %w\n%s", strings.Join(args[:min(3, len(args))], " "), err, tail(stdout.String()))
	}
	if out != nil && len(resp.Result) > 0 {
		// A failed command may still carry a result (failures in a batch).
		if err := json.Unmarshal(resp.Result, out); err != nil && runErr == nil && resp.Status == 0 {
			return fmt.Errorf("sf %s: decoding result: %w", strings.Join(args[:min(3, len(args))], " "), err)
		}
	}
	if runErr != nil || resp.Status != 0 {
		msg := resp.Message
		if msg == "" {
			msg = tail(stderr.String())
		}
		return &sfError{Command: strings.Join(args[:min(3, len(args))], " "), Name: resp.Name, Message: msg}
	}
	return nil
}

// sfError is a command sf ran and reported as failed.
type sfError struct {
	Command string // its first words, e.g. "data query --target-org"
	Name    string // sf's error name, e.g. "NamedOrgNotFoundError"
	Message string
}

func (e *sfError) Error() string {
	if e.Name != "" {
		return fmt.Sprintf("sf %s: %s: %s", e.Command, e.Name, e.Message)
	}
	return fmt.Sprintf("sf %s: %s", e.Command, e.Message)
}

// shell runs commands in dir with SF_TARGET_ORG set to the org.
func (o *Org) shell(ctx context.Context, dir, user string, commands []string) error {
	for _, command := range commands {
		c := exec.CommandContext(ctx, "sh", "-c", command)
		c.Dir = dir
		vars := map[string]string{"SF_TARGET_ORG": user, "SF_DISABLE_TELEMETRY": "true", "SF_AUTOUPDATE_DISABLE": "true"}
		c.Env = os.Environ()
		for _, k := range slices.Sorted(maps.Keys(vars)) {
			c.Env = append(c.Env, k+"="+vars[k])
		}
		var out bytes.Buffer
		c.Stdout, c.Stderr = &out, &out
		if err := c.Run(); err != nil {
			return fmt.Errorf("%s: %w\n%s", command, err, tail(out.String()))
		}
	}
	return nil
}

// sameID compares Salesforce IDs, which come in 15- and 18-character forms.
func sameID(a, b string) bool {
	return len(a) >= 15 && len(b) >= 15 && a[:15] == b[:15]
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 4096 {
		return "…" + s[len(s)-4096:]
	}
	return s
}
