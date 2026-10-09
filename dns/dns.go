// Package dns defines DNS record management for environment endpoints
// (Cloudflare, DigitalOcean DNS, …). Implementations live in subpackages.
//
// The engine calls it in provision_environment, after the Provisioner, so every
// endpoint resolves before anything is deployed. Provider-specific options
// (e.g. Cloudflare proxying, TTL) are plugin config, not part of Record.
package dns

import (
	"context"
)

// Provider manages DNS records. Records are identified by name, so nothing
// needs to be persisted to update or delete them later.
type Provider interface {
	Ensure(ctx context.Context, r Record) error    // create or update
	Delete(ctx context.Context, name string) error // a missing record is not an error
}

type Record struct {
	Name  string `json:"name"`  // FQDN, e.g. "sk-abc123-api.example.com"
	Type  string `json:"type"`  // "A", "AAAA", "CNAME"
	Value string `json:"value"` // IP or target hostname
}
