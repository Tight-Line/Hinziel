// Package cloudflare is a dns.Provider for Cloudflare DNS.
//
// The token needs Zone:Read and DNS:Edit on the zone. Cloudflare can't limit
// a token to some records of a zone, so when the zone also holds production
// records, set AllowedNames: every other name is refused before any API call.
package cloudflare

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/Tight-Line/heinzel/core"
	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/internal/apiclient"
)

// Options configure Cloudflare DNS.
type Options struct {
	Token string
	Zone  string // e.g. "example.com"
	// Proxied routes the records through Cloudflare (orange cloud), which
	// then serves HTTPS for them.
	Proxied bool
	TTL     int // seconds; 0 = automatic
	// AllowedNames, if set, is a glob (path.Match syntax) that the part of a
	// record name before the zone must match, e.g. "hz-*". Records with other
	// names are never created, changed or deleted.
	AllowedNames string
	// BaseURL of the API; "" = https://api.cloudflare.com/client/v4.
	BaseURL string
}

// Provider is the Cloudflare DNS provider.
type Provider struct {
	opts Options
	api  *apiclient.Client

	mu     sync.Mutex
	zoneID string
}

var (
	_ dns.Provider = (*Provider)(nil)
	_ core.Checker = (*Provider)(nil)
)

// New returns a Cloudflare DNS provider.
func New(opts Options) *Provider {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	opts.Zone = strings.TrimSuffix(strings.ToLower(opts.Zone), ".")
	return &Provider{opts: opts, api: &apiclient.Client{
		Name:    "cloudflare",
		BaseURL: base,
		Auth:    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+opts.Token) },
	}}
}

type record struct {
	ID      string `json:"id,omitempty"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	Proxied bool   `json:"proxied"`
	TTL     int    `json:"ttl"`
}

// envelope is the shape of every Cloudflare API response.
type envelope[T any] struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result T `json:"result"`
}

func (e *envelope[T]) err() error {
	if e.Success {
		return nil
	}
	var msgs []string
	for _, m := range e.Errors {
		msgs = append(msgs, fmt.Sprintf("%d %s", m.Code, m.Message))
	}
	return fmt.Errorf("cloudflare: %s", strings.Join(msgs, "; "))
}

func call[T any](ctx context.Context, p *Provider, method, path string, query url.Values, body any) (T, error) {
	var env envelope[T]
	if err := p.api.Do(ctx, method, path, query, body, &env); err != nil {
		return env.Result, err
	}
	return env.Result, env.err()
}

// allowed checks that name is in the zone and matches AllowedNames.
func (p *Provider) allowed(name string) (string, error) {
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if name != p.opts.Zone && !strings.HasSuffix(name, "."+p.opts.Zone) {
		return "", fmt.Errorf("record %s is not in zone %s", name, p.opts.Zone)
	}
	if p.opts.AllowedNames != "" {
		label := strings.TrimSuffix(strings.TrimSuffix(name, p.opts.Zone), ".")
		if ok, err := path.Match(p.opts.AllowedNames, label); err != nil || !ok {
			return "", fmt.Errorf("record %s is outside AllowedNames %q; refusing to touch it", name, p.opts.AllowedNames)
		}
	}
	return name, nil
}

func (p *Provider) zone(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.zoneID != "" {
		return p.zoneID, nil
	}
	zones, err := call[[]struct {
		ID string `json:"id"`
	}](ctx, p, http.MethodGet, "/zones", url.Values{"name": {p.opts.Zone}}, nil)
	if err != nil {
		return "", fmt.Errorf("zone %s: %w", p.opts.Zone, err)
	}
	if len(zones) == 0 {
		return "", fmt.Errorf("zone %s not found, or the token can't read it", p.opts.Zone)
	}
	p.zoneID = zones[0].ID
	return p.zoneID, nil
}

func (p *Provider) records(ctx context.Context, zone, name string) ([]record, error) {
	return call[[]record](ctx, p, http.MethodGet, "/zones/"+zone+"/dns_records", url.Values{"name": {name}}, nil)
}

// Ensure makes r the only record at r.Name: it updates an existing record of
// the same type and removes any others (a CNAME can't coexist with them).
func (p *Provider) Ensure(ctx context.Context, r dns.Record) error {
	name, err := p.allowed(r.Name)
	if err != nil {
		return err
	}
	zone, err := p.zone(ctx)
	if err != nil {
		return err
	}
	existing, err := p.records(ctx, zone, name)
	if err != nil {
		return err
	}
	ttl := p.opts.TTL
	if ttl == 0 {
		ttl = 1 // automatic
	}
	want := record{Type: r.Type, Name: name, Content: r.Value, Proxied: p.opts.Proxied, TTL: ttl}
	var keep *record
	for i := range existing {
		if keep == nil && existing[i].Type == r.Type {
			keep = &existing[i]
			continue
		}
		if _, err := call[any](ctx, p, http.MethodDelete, "/zones/"+zone+"/dns_records/"+existing[i].ID, nil, nil); err != nil {
			return err
		}
	}
	if keep == nil {
		_, err = call[record](ctx, p, http.MethodPost, "/zones/"+zone+"/dns_records", nil, want)
		return err
	}
	if keep.Content == want.Content && keep.Proxied == want.Proxied && keep.TTL == want.TTL {
		return nil
	}
	_, err = call[record](ctx, p, http.MethodPut, "/zones/"+zone+"/dns_records/"+keep.ID, nil, want)
	return err
}

// Delete removes every record named name. A missing record is not an error.
func (p *Provider) Delete(ctx context.Context, name string) error {
	name, err := p.allowed(name)
	if err != nil {
		return err
	}
	zone, err := p.zone(ctx)
	if err != nil {
		return err
	}
	existing, err := p.records(ctx, zone, name)
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range existing {
		_, err := call[any](ctx, p, http.MethodDelete, "/zones/"+zone+"/dns_records/"+rec.ID, nil, nil)
		if err != nil && !apiclient.IsStatus(err, http.StatusNotFound) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Check verifies that the token can read the zone and its DNS records.
func (p *Provider) Check(ctx context.Context) error {
	if p.opts.AllowedNames != "" {
		if _, err := path.Match(p.opts.AllowedNames, ""); err != nil {
			return fmt.Errorf("AllowedNames %q: %w", p.opts.AllowedNames, err)
		}
	}
	zone, err := p.zone(ctx)
	if err != nil {
		return err
	}
	_, err = call[[]record](ctx, p, http.MethodGet, "/zones/"+zone+"/dns_records", url.Values{"per_page": {"5"}}, nil)
	return err
}
