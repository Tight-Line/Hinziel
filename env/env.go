// Package env defines provisioning of the place a branch runs (a DigitalOcean
// droplet, a folder + vhost on a shared host, …). Implementations live in
// subpackages; one implementation may also satisfy deploy.Deployer.
package env

import (
	"context"
)

// Provisioner creates (and destroys) the place a branch will run.
type Provisioner interface {
	// Ensure returns the existing environment named spec.Name or creates it.
	// It blocks until the environment is reachable (e.g. SSH answers).
	Ensure(ctx context.Context, spec Spec) (*Environment, error)
	// Destroy removes everything Ensure created. Destroying a missing env is not an error.
	Destroy(ctx context.Context, e *Environment) error
}

// Lister is optional: lets `heinzel env list` find orphans not tracked in the run store.
type Lister interface {
	List(ctx context.Context) ([]Environment, error)
}

type Spec struct {
	Name   string            // DNS-label-safe slug (≤63 chars), unique per factory+ticket
	Labels map[string]string // factory, ticket, run id; for tagging cloud resources
}

// Environment is the hand-off from Provisioner to the Deployers, and what
// teardown needs later. It is persisted with the run.
type Environment struct {
	Name     string `json:"name"`
	Provider string `json:"provider"` // plugin instance name
	ID       string `json:"id"`       // provider-native id (droplet id, folder path)
	// Endpoints are the environment's public URLs by name ("api", "web"). They
	// are all decided in provision_environment, before any deploy, so every
	// deployer can use every URL (an SPA build needs the API URL, the API needs
	// the SPA URL for CORS). Set by the Provisioner and/or by the factory's Endpoints.
	Endpoints map[string]string `json:"endpoints,omitempty"`
	Hosts     []Host            `json:"hosts,omitempty"`
	State     map[string]string `json:"state,omitempty"` // opaque provider data (port, vhost path, dns record id)
}

type Host struct {
	Name    string     `json:"name"`
	Address string     `json:"address"` // IP or hostname
	SSH     *SSHAccess `json:"ssh,omitempty"`
}

type SSHAccess struct {
	User           string `json:"user"`
	Port           int    `json:"port"`
	KeyPath        string `json:"key_path"`
	KnownHostsPath string `json:"known_hosts_path,omitempty"` // empty = trust on first use, pinned in State
}
