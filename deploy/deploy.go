// Package deploy defines how a branch is put onto an environment (docker
// compose over SSH, rsync + nginx, …). Implementations live in subpackages.
package deploy

import (
	"context"
	"time"

	"github.com/Tight-Line/hinzele/env"
	"github.com/Tight-Line/hinzele/tracker"
)

// Deployer puts Source onto Environment. Redeploying the same commit must be safe.
type Deployer interface {
	Deploy(ctx context.Context, e *env.Environment, src *Source) (*Deployment, error)
}

// Retriever is optional: the inverse of Deploy. It brings changes made directly
// in the environment (e.g. clicks in a Salesforce scratch org) into the working
// tree as files, so they are committed and reviewed like code. It is called by
// `hinzele retrieve`, by `hinzele deploy` as a guard (deploy is refused if
// retrieve leaves uncommitted changes), and by the retrieve step after implement.
type Retriever interface {
	Retrieve(ctx context.Context, e *env.Environment, workdir string) error
}

// Source is always a pushed commit: Hinzele never deploys uncommitted or
// unpushed work, so an environment only ever runs something in the forge.
// Each deployer is bound to one of the project's repos in config.
type Source struct {
	Repo    string // name of the factory repo this deployer is bound to
	Workdir string // clean checkout of Repo at Commit; Deployer may build here
	Branch  string
	Commit  string          // required; pushed to the forge
	Ticket  *tracker.Ticket // for templating (.env values, labels)
}

type Deployment struct {
	URL    string            `json:"url"` // the URL people should click
	Commit string            `json:"commit"`
	At     time.Time         `json:"at"`
	Info   map[string]string `json:"info,omitempty"`
}
