// Package cfoauthrelay gives every environment Google (or any OAuth) login
// through one redirect URI, so nobody registers a new one per environment.
//
// OAuth providers accept only redirect URIs registered in advance, one by
// one: no wildcards, and for Google, no API. With previews named per ticket,
// that's a human action per preview. The relay removes it:
//
//  1. Register the relay's callback URLs with the provider once, e.g.
//     https://hz-auth.example.com/auth/google/callback.
//  2. Every environment uses the relay as its OAuth redirect host
//     (GOOGLE_REDIRECT_HOST or similar in its configuration).
//  3. When a login starts on an environment, a short-lived cookie on the
//     parent domain, oauth_return, names the host it started on. The app can
//     set it (a few lines in its login-start route), or the environment's
//     proxy can (dockercompose.TLS.ReturnCookie).
//  4. The provider sends the browser to the relay, which redirects it, path
//     and query unchanged, to the host the cookie names. The app completes
//     the login there, with the same redirect URI it sent.
//
// Nothing in that contract is Hinziel's: environments made by hand use the
// same relay, as long as they set the cookie and the redirect host.
//
// It works with apps whose OAuth flow keeps no state in a session cookie
// of their own host between the start and the callback (Laravel Socialite's
// stateless(), for example), or whose state survives the extra redirect.
//
// The relay is a Cloudflare Worker on Host, with a route and a proxied DNS
// record. This Deployer keeps all three in place on every deploy, so it
// needs no setup of its own; list it among the factory's Deployers. A team
// may as well deploy the same Worker with its own tooling (Script returns
// it). It redirects only to hosts under AllowedSuffix, never to itself.
//
// # Credentials
//
//   - CLOUDFLARE_ACCOUNT_ID and CLOUDFLARE_WORKERS_TOKEN, as for
//     deploy/cfworkers: the "Edit Cloudflare Workers" template includes
//     Workers Routes:Edit on the zone, which the route needs.
//   - CLOUDFLARE_DNS_TOKEN, as for dns/cloudflare (Zone:Read and DNS:Edit
//     on the zone): it looks the zone up and keeps the relay's DNS record.
package cfoauthrelay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/deploy"
	"github.com/Tight-Line/hinziel/env"
	"github.com/Tight-Line/hinziel/internal/apiclient"
)

type Options struct {
	AccountID    string
	WorkersToken string
	DNSToken     string
	Zone         string // e.g. "example.com"
	Host         string // the relay's hostname in Zone, e.g. "hz-auth.example.com"
	// AllowedSuffix limits where the relay sends browsers: hosts ending in
	// it, such as ".example.com". "" = "." + Zone.
	AllowedSuffix string
	// Cookie names the host to return to; "" = "oauth_return".
	Cookie string
	// Worker is the relay Worker's name; "" = "hinziel-oauth-relay".
	Worker string
	// BaseURL of the API; "" = https://api.cloudflare.com/client/v4.
	BaseURL string
}

// Deployer keeps the relay in place.
type Deployer struct {
	opts      Options
	dns, work *apiclient.Client
}

var (
	_ deploy.Deployer = (*Deployer)(nil)
	_ core.Checker    = (*Deployer)(nil)
)

// DefaultCookie is the cookie both ends use unless told otherwise.
const DefaultCookie = "oauth_return"

func New(opts Options) *Deployer {
	if opts.Cookie == "" {
		opts.Cookie = DefaultCookie
	}
	if opts.Worker == "" {
		opts.Worker = "hinziel-oauth-relay"
	}
	if opts.AllowedSuffix == "" && opts.Zone != "" {
		opts.AllowedSuffix = "." + opts.Zone
	}
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	bearer := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}
	return &Deployer{
		opts: opts,
		dns:  &apiclient.Client{Name: "cloudflare", BaseURL: base, Auth: bearer(opts.DNSToken)},
		work: &apiclient.Client{Name: "cloudflare", BaseURL: base, Auth: bearer(opts.WorkersToken)},
	}
}

// Deploy makes sure the relay's Worker, route and DNS record exist and are
// current. It doesn't depend on the environment or the commit.
func (d *Deployer) Deploy(ctx context.Context, _ *env.Environment, src *deploy.Source) (*deploy.Deployment, error) {
	zone, err := d.zoneID(ctx)
	if err != nil {
		return nil, err
	}
	if err := d.uploadScript(ctx); err != nil {
		return nil, err
	}
	if err := d.ensureRoute(ctx, zone); err != nil {
		return nil, err
	}
	if err := d.ensureDNS(ctx, zone); err != nil {
		return nil, err
	}
	dep := &deploy.Deployment{URL: "https://" + d.opts.Host, At: time.Now()}
	if src != nil {
		dep.Commit = src.Commit
	}
	return dep, nil
}

var cookieName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Check verifies the options and that both tokens reach what they need.
func (d *Deployer) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{
		"AccountID": d.opts.AccountID, "WorkersToken": d.opts.WorkersToken, "DNSToken": d.opts.DNSToken,
		"Zone": d.opts.Zone, "Host": d.opts.Host,
	}); err != nil {
		return err
	}
	var errs []error
	if !strings.HasSuffix(d.opts.Host, "."+d.opts.Zone) {
		errs = append(errs, fmt.Errorf("Host %q is not in Zone %q", d.opts.Host, d.opts.Zone))
	}
	if !strings.HasPrefix(d.opts.AllowedSuffix, ".") || !strings.HasSuffix(d.opts.AllowedSuffix, "."+d.opts.Zone) && d.opts.AllowedSuffix != "."+d.opts.Zone {
		errs = append(errs, fmt.Errorf("AllowedSuffix %q must start with a dot and be in Zone %q", d.opts.AllowedSuffix, d.opts.Zone))
	}
	if !cookieName.MatchString(d.opts.Cookie) {
		errs = append(errs, fmt.Errorf("Cookie %q is not a valid cookie name", d.opts.Cookie))
	}
	zone, err := d.zoneID(ctx)
	if err != nil {
		return errors.Join(append(errs, err)...)
	}
	if _, err := d.routes(ctx, zone); err != nil {
		errs = append(errs, fmt.Errorf("Workers routes (the Workers token needs Workers Routes:Edit on the zone): %w", err))
	}
	var sub any
	if err := d.work.Do(ctx, http.MethodGet, "/accounts/"+d.opts.AccountID+"/workers/subdomain", nil, nil, &sub); err != nil {
		errs = append(errs, fmt.Errorf("Workers: %w", err))
	}
	return errors.Join(errs...)
}

// Script is the relay Worker's source.
func (d *Deployer) Script() string {
	cookie, _ := json.Marshal(d.opts.Cookie)
	suffix, _ := json.Marshal(d.opts.AllowedSuffix)
	return `// An OAuth relay: the provider calls back here for every environment that
// uses it, and this sends the browser on to the host the login started on,
// which a cookie names. Kept in place by Hinziel's deploy/cfoauthrelay;
// changes made here are overwritten.
const COOKIE = ` + string(cookie) + `;
const ALLOWED_SUFFIX = ` + string(suffix) + `;
const HOSTNAME = /^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$/;

export default {
  async fetch(request) {
    const url = new URL(request.url);
    let host = "";
    for (const part of (request.headers.get("Cookie") || "").split(";")) {
      const [name, ...rest] = part.trim().split("=");
      if (name === COOKIE) host = rest.join("=").toLowerCase();
    }
    // Only hosts of our own domain, and never the relay itself.
    if (!HOSTNAME.test(host) || !host.endsWith(ALLOWED_SUFFIX) || host === url.hostname) {
      return new Response("This is an OAuth relay. It couldn't tell which environment you started " +
        "the login on: start it there again, within ten minutes.\n",
        { status: 400, headers: { "Content-Type": "text/plain; charset=utf-8" } });
    }
    return Response.redirect("https://" + host + url.pathname + url.search, 302);
  },
};
`
}

func (d *Deployer) zoneID(ctx context.Context) (string, error) {
	var resp struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	if err := d.dns.Do(ctx, http.MethodGet, "/zones", url.Values{"name": {d.opts.Zone}}, nil, &resp); err != nil {
		return "", fmt.Errorf("zone %s: %w", d.opts.Zone, err)
	}
	if len(resp.Result) == 0 {
		return "", fmt.Errorf("zone %s: not found, or the DNS token can't read it", d.opts.Zone)
	}
	return resp.Result[0].ID, nil
}

func (d *Deployer) uploadScript(ctx context.Context) error {
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	meta, err := w.CreateFormField("metadata")
	if err != nil {
		return err
	}
	json.NewEncoder(meta).Encode(map[string]string{"main_module": "relay.js", "compatibility_date": "2024-09-01"})
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="relay.js"; filename="relay.js"`)
	h.Set("Content-Type", "application/javascript+module")
	part, err := w.CreatePart(h)
	if err != nil {
		return err
	}
	part.Write([]byte(d.Script()))
	if err := w.Close(); err != nil {
		return err
	}
	path := "/accounts/" + d.opts.AccountID + "/workers/scripts/" + url.PathEscape(d.opts.Worker)
	if err := d.work.Do(ctx, http.MethodPut, path, nil, apiclient.RawBody{ContentType: w.FormDataContentType(), Data: body.Bytes()}, nil); err != nil {
		return fmt.Errorf("uploading Worker %s: %w", d.opts.Worker, err)
	}
	return nil
}

type route struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

func (d *Deployer) routes(ctx context.Context, zone string) ([]route, error) {
	var resp struct {
		Result []route `json:"result"`
	}
	err := d.work.Do(ctx, http.MethodGet, "/zones/"+zone+"/workers/routes", nil, nil, &resp)
	return resp.Result, err
}

func (d *Deployer) ensureRoute(ctx context.Context, zone string) error {
	pattern := d.opts.Host + "/*"
	routes, err := d.routes(ctx, zone)
	if err != nil {
		return fmt.Errorf("Worker routes: %w", err)
	}
	body := map[string]string{"pattern": pattern, "script": d.opts.Worker}
	for _, r := range routes {
		if r.Pattern != pattern {
			continue
		}
		if r.Script == d.opts.Worker {
			return nil
		}
		return d.work.Do(ctx, http.MethodPut, "/zones/"+zone+"/workers/routes/"+r.ID, nil, body, nil)
	}
	return d.work.Do(ctx, http.MethodPost, "/zones/"+zone+"/workers/routes", nil, body, nil)
}

// ensureDNS keeps a proxied record for Host. A Worker on a route answers
// before the origin, so the record points nowhere (100::, the discard
// prefix), as Cloudflare recommends for Worker-only hostnames.
func (d *Deployer) ensureDNS(ctx context.Context, zone string) error {
	var resp struct {
		Result []struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Proxied bool   `json:"proxied"`
		} `json:"result"`
	}
	if err := d.dns.Do(ctx, http.MethodGet, "/zones/"+zone+"/dns_records", url.Values{"name": {d.opts.Host}}, nil, &resp); err != nil {
		return fmt.Errorf("DNS record %s: %w", d.opts.Host, err)
	}
	for _, r := range resp.Result {
		if r.Proxied {
			return nil // any proxied record lets the route answer
		}
		return fmt.Errorf("DNS record %s exists but isn't proxied; the relay can only answer on a proxied name", d.opts.Host)
	}
	body := map[string]any{"type": "AAAA", "name": d.opts.Host, "content": "100::", "proxied": true, "ttl": 1,
		"comment": "Hinziel OAuth relay (deploy/cfoauthrelay)"}
	if err := d.dns.Do(ctx, http.MethodPost, "/zones/"+zone+"/dns_records", nil, body, nil); err != nil {
		return fmt.Errorf("DNS record %s: %w", d.opts.Host, err)
	}
	return nil
}
