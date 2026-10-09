// Package engine runs the pipeline: it executes the steps of a run against a
// project's components, persists the run after every step, and handles
// resume, gates, retries and notifications.
package engine

import (
	"time"

	"github.com/Tight-Line/heinzel/agent"
	"github.com/Tight-Line/heinzel/deploy"
	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/env"
	"github.com/Tight-Line/heinzel/forge"
	"github.com/Tight-Line/heinzel/notify"
	"github.com/Tight-Line/heinzel/tracker"
)

// Project is a validated factory, as the engine sees it. The root heinzel
// package converts a heinzel.Factory into a Project.
type Project struct {
	Name        string
	Tracker     tracker.Tracker
	Repos       []Repo
	Agent       agent.Agent
	Provisioner env.Provisioner // nil = no environment
	DNS         dns.Provider    // nil = no DNS records
	Endpoints   []Endpoint
	Deployers   []Deployer
	Notifiers   []Notifier
	Settings    Settings
}

type Repo struct {
	Name         string
	Forge        forge.Forge
	TargetBranch string
}

type Endpoint struct {
	Name string
	URL  func(e *env.Environment) string
	DNS  func(e *env.Environment) (recordType, value string) // nil = no record
}

type Deployer struct {
	Name     string
	Repo     string
	Deployer deploy.Deployer
}

type Notifier struct {
	On       []notify.EventType // empty = every event
	Notifier notify.Notifier
}

type Settings struct {
	Instructions        string
	BranchName          func(t *tracker.Ticket) string // nil = names.Branch
	EnvName             func(t *tracker.Ticket) string // nil = names.Env
	EnvironmentActions  func(e *env.Environment) []env.Action
	Gates               []string
	DisabledSteps       []string
	DraftChangeRequests bool
}

// Step names, in pipeline order.
const (
	StepFetchTicket          = "fetch_ticket"
	StepPrepareWorkspace     = "prepare_workspace"
	StepProvisionEnvironment = "provision_environment"
	StepDeployBase           = "deploy_base"
	StepImplement            = "implement"
	StepRetrieve             = "retrieve"
	StepFinalizeBranch       = "finalize_branch"
	StepOpenChangeRequest    = "open_change_request"
	StepDeploy               = "deploy"
	StepPublish              = "publish"
)

// Pipeline lists the steps in execution order.
var Pipeline = []string{
	StepFetchTicket,
	StepPrepareWorkspace,
	StepProvisionEnvironment,
	StepDeployBase,
	StepImplement,
	StepRetrieve,
	StepFinalizeBranch,
	StepOpenChangeRequest,
	StepDeploy,
	StepPublish,
}

// Default step timeouts.
var stepTimeouts = map[string]time.Duration{
	StepFetchTicket:          2 * time.Minute,
	StepPrepareWorkspace:     15 * time.Minute,
	StepProvisionEnvironment: 15 * time.Minute,
	StepDeployBase:           20 * time.Minute,
	StepImplement:            60 * time.Minute,
	StepRetrieve:             10 * time.Minute,
	StepFinalizeBranch:       5 * time.Minute,
	StepOpenChangeRequest:    5 * time.Minute,
	StepDeploy:               20 * time.Minute,
	StepPublish:              5 * time.Minute,
}
