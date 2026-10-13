// Package cloudflare is a cert.Issuer for Cloudflare's Origin CA: the
// certificates Cloudflare trusts on an origin behind its proxy in
// "Full (strict)" mode. Browsers don't trust them, so they only serve
// hostnames proxied through Cloudflare (dns/cloudflare with Proxied).
//
// Cloudflare can't limit a token to some hostnames of a zone, so when the
// zone also serves production, set AllowedNames: certificates for other
// names are never issued or revoked.
//
// # Credentials
//
//   - CLOUDFLARE_CERT_TOKEN: at https://dash.cloudflare.com/profile/api-tokens,
//     "Create Token" → custom token with Zone → SSL and Certificates → Edit
//     and Zone → Zone → Read, with Zone Resources limited to the one zone.
//     The DNS token can carry these permissions too.
package cloudflare

import (
	"context"
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/Tight-Line/hinziel/cert"
	"github.com/Tight-Line/hinziel/core"
	"github.com/Tight-Line/hinziel/internal/apiclient"
)

// Options configure the Origin CA.
type Options struct {
	Token string
	Zone  string // e.g. "example.com"
	// AllowedNames, if set, is a glob (path.Match syntax) that the part of
	// every hostname before the zone must match, e.g. "hz-*". Certificates
	// for other names are never issued or revoked.
	AllowedNames string
	// Validity of a certificate in days: 7, 30, 90, 365, 730, 1095 or 5475;
	// 0 = 90. Deployers replace a certificate when a third of it is left,
	// so this is how long an environment can go without a deploy.
	Validity int
	// BaseURL of the API; "" = https://api.cloudflare.com/client/v4.
	BaseURL string
}

var validities = []int{7, 30, 90, 365, 730, 1095, 5475}

// Issuer issues Origin CA certificates.
type Issuer struct {
	opts Options
	api  *apiclient.Client

	mu     sync.Mutex
	zoneID string
}

var (
	_ cert.Issuer  = (*Issuer)(nil)
	_ core.Checker = (*Issuer)(nil)
)

// New returns an Origin CA issuer.
func New(opts Options) *Issuer {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.cloudflare.com/client/v4"
	}
	opts.Zone = strings.TrimSuffix(strings.ToLower(opts.Zone), ".")
	if opts.Validity == 0 {
		opts.Validity = 90
	}
	return &Issuer{opts: opts, api: &apiclient.Client{
		Name:    "cloudflare",
		BaseURL: base,
		Auth:    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+opts.Token) },
	}}
}

// certificate is an Origin CA certificate as the API returns it.
type certificate struct {
	ID          string   `json:"id"`
	Certificate string   `json:"certificate"`
	Hostnames   []string `json:"hostnames"`
	RevokedAt   string   `json:"revoked_at,omitempty"`
}

// envelope is the shape of every Cloudflare API response.
type envelope[T any] struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result     T `json:"result"`
	ResultInfo struct {
		TotalPages int `json:"total_pages"`
	} `json:"result_info"`
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

func call[T any](ctx context.Context, i *Issuer, method, path string, query url.Values, body any) (*envelope[T], error) {
	var env envelope[T]
	if err := i.api.Do(ctx, method, path, query, body, &env); err != nil {
		return &env, err
	}
	return &env, env.err()
}

// allowed checks that every hostname is in the zone and matches
// AllowedNames, and returns them normalized.
func (i *Issuer) allowed(hostnames []string) ([]string, error) {
	if len(hostnames) == 0 {
		return nil, errors.New("a certificate needs at least one hostname")
	}
	out := make([]string, len(hostnames))
	for n, name := range hostnames {
		name = strings.TrimSuffix(strings.ToLower(name), ".")
		if name != i.opts.Zone && !strings.HasSuffix(name, "."+i.opts.Zone) {
			return nil, fmt.Errorf("hostname %s is not in zone %s", name, i.opts.Zone)
		}
		if i.opts.AllowedNames != "" {
			label := strings.TrimSuffix(strings.TrimSuffix(name, i.opts.Zone), ".")
			if ok, err := path.Match(i.opts.AllowedNames, label); err != nil || !ok {
				return nil, fmt.Errorf("hostname %s is outside AllowedNames %q; refusing to touch its certificates", name, i.opts.AllowedNames)
			}
		}
		out[n] = name
	}
	return out, nil
}

func (i *Issuer) zone(ctx context.Context) (string, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.zoneID != "" {
		return i.zoneID, nil
	}
	zones, err := call[[]struct {
		ID string `json:"id"`
	}](ctx, i, http.MethodGet, "/zones", url.Values{"name": {i.opts.Zone}}, nil)
	if err != nil {
		return "", fmt.Errorf("zone %s: %w", i.opts.Zone, err)
	}
	if len(zones.Result) == 0 {
		return "", fmt.Errorf("zone %s not found, or the token can't read it", i.opts.Zone)
	}
	i.zoneID = zones.Result[0].ID
	return i.zoneID, nil
}

// matching returns the unrevoked certificates of the zone for exactly
// hostnames.
func (i *Issuer) matching(ctx context.Context, hostnames []string) ([]certificate, error) {
	zone, err := i.zone(ctx)
	if err != nil {
		return nil, err
	}
	var out []certificate
	for page := 1; ; page++ {
		res, err := call[[]certificate](ctx, i, http.MethodGet, "/certificates",
			url.Values{"zone_id": {zone}, "per_page": {"50"}, "page": {strconv.Itoa(page)}}, nil)
		if err != nil {
			return nil, err
		}
		for _, c := range res.Result {
			if c.RevokedAt == "" && cert.SameHostnames(c.Hostnames, hostnames) {
				out = append(out, c)
			}
		}
		if page >= res.ResultInfo.TotalPages {
			return out, nil
		}
	}
}

func (i *Issuer) revoke(ctx context.Context, certs []certificate) error {
	var errs []error
	for _, c := range certs {
		_, err := call[any](ctx, i, http.MethodDelete, "/certificates/"+c.ID, nil, nil)
		if err != nil && !apiclient.IsStatus(err, http.StatusNotFound) {
			errs = append(errs, fmt.Errorf("revoking certificate %s: %w", c.ID, err))
		}
	}
	return errors.Join(errs...)
}

// Issue revokes the zone's certificates for exactly hostnames, then issues
// a new one for csr.
func (i *Issuer) Issue(ctx context.Context, hostnames []string, csr []byte) ([]byte, error) {
	names, err := i.allowed(hostnames)
	if err != nil {
		return nil, err
	}
	reqType, err := requestType(csr)
	if err != nil {
		return nil, err
	}
	old, err := i.matching(ctx, names)
	if err != nil {
		return nil, err
	}
	if err := i.revoke(ctx, old); err != nil {
		return nil, err
	}
	res, err := call[certificate](ctx, i, http.MethodPost, "/certificates", nil, map[string]any{
		"csr":                string(csr),
		"hostnames":          names,
		"request_type":       reqType,
		"requested_validity": i.opts.Validity,
	})
	if err != nil {
		return nil, fmt.Errorf("issuing a certificate for %s: %w", strings.Join(names, ", "), err)
	}
	return []byte(res.Result.Certificate), nil
}

// requestType is the Origin CA's name for the CSR's key type.
func requestType(csr []byte) (string, error) {
	block, _ := pem.Decode(csr)
	if block == nil {
		return "", errors.New("CSR is not PEM")
	}
	req, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return "", err
	}
	if _, ok := req.PublicKey.(*ecdsa.PublicKey); ok {
		return "origin-ecc", nil
	}
	return "origin-rsa", nil
}

// Revoke revokes the zone's certificates for exactly hostnames.
func (i *Issuer) Revoke(ctx context.Context, hostnames []string) error {
	names, err := i.allowed(hostnames)
	if err != nil {
		return err
	}
	certs, err := i.matching(ctx, names)
	if err != nil {
		return err
	}
	return i.revoke(ctx, certs)
}

// Check verifies the options and that the token can list the zone's
// certificates.
func (i *Issuer) Check(ctx context.Context) error {
	if err := core.NeedSettings(map[string]string{"Token": i.opts.Token, "Zone": i.opts.Zone}); err != nil {
		return err
	}
	if !slices.Contains(validities, i.opts.Validity) {
		return fmt.Errorf("Validity %d: the Origin CA issues certificates for 7, 30, 90, 365, 730, 1095 or 5475 days", i.opts.Validity)
	}
	if i.opts.AllowedNames != "" {
		if _, err := path.Match(i.opts.AllowedNames, ""); err != nil {
			return fmt.Errorf("AllowedNames %q: %w", i.opts.AllowedNames, err)
		}
	}
	zone, err := i.zone(ctx)
	if err != nil {
		return err
	}
	_, err = call[[]certificate](ctx, i, http.MethodGet, "/certificates", url.Values{"zone_id": {zone}, "per_page": {"5"}}, nil)
	if err != nil {
		return fmt.Errorf("listing certificates (the token needs SSL and Certificates: Edit): %w", err)
	}
	return nil
}
