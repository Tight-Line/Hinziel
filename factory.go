package heinzel

import (
	"errors"
	"fmt"
	"net"
	"regexp"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/notify"
	"github.com/Tight-Line/heinzel/tracker"
	"github.com/Tight-Line/heinzel/trigger"
)

// Factory is one team's software factory: where tickets come from, which
// repos the agent works in, where the result runs, and who hears about it.
//
// The zero value of every optional field is useful: leave a component out and
// that part of the pipeline is silently a no-op (no Notifiers: nobody is
// notified; no Environment: nothing is provisioned or deployed). Only Name,
// Tracker, Agent and at least one Repo with a Forge are required. Settings
// that only make sense with a missing component, such as an Endpoint DNS
// target without a DNS provider, are reported as errors, not ignored.
type Factory struct {
	Name    string // [a-z0-9-]; used in CLI output, run state and trigger URLs
	Tracker tracker.Tracker
	Repos   []Repo // at least one; checked out side by side on the same branch
	Agent   agent.Agent

	Environment env.Provisioner // nil = no environment, no endpoints, no deploys
	DNS         dns.Provider    // nil = no DNS records
	Endpoints   []Endpoint      // public URLs, decided right after provisioning
	Deployers   []Deployer      // run in order on every deploy

	Triggers  []Trigger
	Notifiers []Notifier

	// Optional behaviour. Zero values give the defaults.

	Instructions        string                         // extra agent instructions, added to the engine's prompt
	BranchName          func(t *tracker.Ticket) string // nil = "heinzel/<key>-<slugged title>"
	EnvName             func(t *tracker.Ticket) string // nil = "hz-<factory>-<key>"; must be a DNS label
	Gates               []Step                         // pause before these steps; empty = fully autonomous
	DisabledSteps       []Step
	DraftChangeRequests bool
}

// Repo is one repository of a factory. Repos may live on different forges.
type Repo struct {
	Name         string // checkout directory name and Deployer.Repo key
	Forge        forge.Forge
	TargetBranch string
}

// Endpoint is a named public URL of the environment ("api", "web"). All
// endpoints are computed right after provisioning, before any deploy, and
// stored in env.Environment.Endpoints, so every deployer can use every URL.
type Endpoint struct {
	Name string
	URL  func(e *env.Environment) string
	DNS  DNSTarget // nil = no DNS record for this endpoint
}

// DNSTarget says what the DNS record for an endpoint's host points at.
type DNSTarget func(e *env.Environment) (recordType, value string)

// HostAddress points the endpoint at the address of e.Hosts[i]: an A or AAAA
// record for an IP address, a CNAME for a hostname.
func HostAddress(i int) DNSTarget {
	return func(e *env.Environment) (string, string) {
		if i < 0 || i >= len(e.Hosts) {
			return "", ""
		}
		addr := e.Hosts[i].Address
		ip := net.ParseIP(addr)
		switch {
		case ip == nil:
			return "CNAME", addr
		case ip.To4() != nil:
			return "A", addr
		default:
			return "AAAA", addr
		}
	}
}

// CNAME points the endpoint at the hostname returned by target.
func CNAME(target func(e *env.Environment) string) DNSTarget {
	return func(e *env.Environment) (string, string) { return "CNAME", target(e) }
}

// Deployer binds a deploy.Deployer to the repo whose commits it deploys.
type Deployer struct {
	Name     string // key in run.Snapshot.Deployments
	Repo     string // a Repo.Name of the same factory
	Deployer deploy.Deployer
}

// Notifier routes events to a notify.Notifier.
type Notifier struct {
	On       []notify.EventType // empty = every event
	Notifier notify.Notifier
}

// Trigger is a named trigger; `serve` mounts its HTTP handlers under
// /triggers/<factory>/<name>/.
type Trigger struct {
	Name    string
	Trigger trigger.Trigger
}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// validate reports every problem with the factory at once.
func (f *Factory) validate() error {
	var errs []error
	bad := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	if !nameRE.MatchString(f.Name) {
		bad("name %q must be lowercase letters, digits and dashes", f.Name)
	}
	if f.Tracker == nil {
		bad("Tracker is required")
	}
	if f.Agent == nil {
		bad("Agent is required")
	}

	if len(f.Repos) == 0 {
		bad("at least one Repo is required")
	}
	repos := map[string]bool{}
	for i, r := range f.Repos {
		switch {
		case !nameRE.MatchString(r.Name):
			bad("Repos[%d]: name %q must be lowercase letters, digits and dashes", i, r.Name)
		case repos[r.Name]:
			bad("Repos[%d]: duplicate name %q", i, r.Name)
		}
		repos[r.Name] = true
		if r.Forge == nil {
			bad("repo %q: Forge is required", r.Name)
		}
		if r.TargetBranch == "" {
			bad("repo %q: TargetBranch is required", r.Name)
		}
	}

	if f.Environment == nil {
		if len(f.Endpoints) > 0 || len(f.Deployers) > 0 || f.DNS != nil {
			bad("Endpoints, Deployers and DNS need an Environment")
		}
	}
	endpoints := map[string]bool{}
	for i, ep := range f.Endpoints {
		switch {
		case ep.Name == "":
			bad("Endpoints[%d]: Name is required", i)
		case endpoints[ep.Name]:
			bad("Endpoints[%d]: duplicate name %q", i, ep.Name)
		}
		endpoints[ep.Name] = true
		if ep.URL == nil {
			bad("endpoint %q: URL is required", ep.Name)
		}
		if ep.DNS != nil && f.DNS == nil {
			bad("endpoint %q has a DNS target but the factory has no DNS provider", ep.Name)
		}
	}
	deployers := map[string]bool{}
	for i, d := range f.Deployers {
		switch {
		case d.Name == "":
			bad("Deployers[%d]: Name is required", i)
		case deployers[d.Name]:
			bad("Deployers[%d]: duplicate name %q", i, d.Name)
		}
		deployers[d.Name] = true
		if !repos[d.Repo] {
			bad("deployer %q: Repo %q is not one of the factory's repos", d.Name, d.Repo)
		}
		if d.Deployer == nil {
			bad("deployer %q: Deployer is required", d.Name)
		}
	}

	triggers := map[string]bool{}
	for i, t := range f.Triggers {
		switch {
		case !nameRE.MatchString(t.Name):
			bad("Triggers[%d]: name %q must be lowercase letters, digits and dashes", i, t.Name)
		case triggers[t.Name]:
			bad("Triggers[%d]: duplicate name %q", i, t.Name)
		}
		triggers[t.Name] = true
		if t.Trigger == nil {
			bad("trigger %q: Trigger is required", t.Name)
		}
	}
	for i, n := range f.Notifiers {
		if n.Notifier == nil {
			bad("Notifiers[%d]: Notifier is required", i)
		}
	}

	for _, s := range f.Gates {
		if !s.valid() {
			bad("Gates: unknown step %q", s)
		}
	}
	for _, s := range f.DisabledSteps {
		if !s.valid() {
			bad("DisabledSteps: unknown step %q", s)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("factory %q:\n%w", f.Name, err)
	}
	return nil
}
