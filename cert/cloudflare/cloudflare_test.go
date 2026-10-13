package cloudflare

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/cert"
	"github.com/Tight-Line/hinziel/cert/certtest"
)

// fakeOriginCA serves the Origin CA API for one zone, example.com, signing
// with a certtest.Fake.
type fakeOriginCA struct {
	mu    sync.Mutex
	ca    *certtest.Fake
	certs map[string]*certificate // by ID, revoked ones included
	next  int
	types []string // request_type of every issue
}

func (f *fakeOriginCA) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any, pages int) {
		json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": v,
			"result_info": map[string]int{"total_pages": pages}})
	}
	fail := func(status, code int, msg string) {
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": code, "message": msg}}})
	}
	if r.Header.Get("Authorization") != "Bearer cf-token" {
		fail(http.StatusForbidden, 9109, "Invalid access token")
		return
	}
	switch {
	case r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			ok([]map[string]string{{"id": "z1"}}, 1)
			return
		}
		ok([]any{}, 1)
	case r.URL.Path == "/certificates" && r.Method == "GET":
		if r.URL.Query().Get("zone_id") != "z1" {
			fail(http.StatusBadRequest, 1001, "zone_id required")
			return
		}
		// Two per page, to exercise paging.
		var all []*certificate
		for n := 1; n <= f.next; n++ {
			if c, found := f.certs[strconv.Itoa(n)]; found {
				all = append(all, c)
			}
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		page = max(page, 1)
		from, to := min((page-1)*2, len(all)), min(page*2, len(all))
		ok(all[from:to], max((len(all)+1)/2, 1))
	case r.URL.Path == "/certificates" && r.Method == "POST":
		var body struct {
			CSR       string   `json:"csr"`
			Hostnames []string `json:"hostnames"`
			Type      string   `json:"request_type"`
			Validity  int      `json:"requested_validity"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.ca.Validity = time.Duration(body.Validity) * 24 * time.Hour
		pem, err := f.ca.Issue(r.Context(), body.Hostnames, []byte(body.CSR))
		if err != nil {
			fail(http.StatusBadRequest, 1010, err.Error())
			return
		}
		f.next++
		c := &certificate{ID: strconv.Itoa(f.next), Certificate: string(pem), Hostnames: body.Hostnames}
		f.certs[c.ID] = c
		f.types = append(f.types, body.Type)
		ok(c, 1)
	case strings.HasPrefix(r.URL.Path, "/certificates/") && r.Method == "DELETE":
		c, found := f.certs[strings.TrimPrefix(r.URL.Path, "/certificates/")]
		if !found || c.RevokedAt != "" {
			fail(http.StatusNotFound, 1003, "certificate not found")
			return
		}
		c.RevokedAt = time.Now().Format(time.RFC3339)
		ok(map[string]string{"id": c.ID, "revoked_at": c.RevokedAt}, 1)
	default:
		http.NotFound(w, r)
	}
}

// current returns the unrevoked certificates.
func (f *fakeOriginCA) current() []certificate {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []certificate
	for _, c := range f.certs {
		if c.RevokedAt == "" {
			out = append(out, *c)
		}
	}
	return out
}

func newTestIssuer(t *testing.T, opts Options) (*Issuer, *fakeOriginCA) {
	f := &fakeOriginCA{ca: certtest.NewFake(), certs: map[string]*certificate{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Token, opts.Zone, opts.BaseURL = "cf-token", "example.com", srv.URL
	return New(opts), f
}

func issue(t *testing.T, i *Issuer, names []string) []byte {
	t.Helper()
	_, csr, err := cert.NewRequest(names)
	if err != nil {
		t.Fatal(err)
	}
	crt, err := i.Issue(context.Background(), names, csr)
	if err != nil {
		t.Fatal(err)
	}
	return crt
}

func TestConformance(t *testing.T) {
	i, _ := newTestIssuer(t, Options{})
	certtest.Run(t, i, "example.com")
}

func TestIssueReplacesTheEnvironmentsCertificateOnly(t *testing.T) {
	i, f := newTestIssuer(t, Options{})
	env := []string{"hz-abc-api.example.com", "hz-abc-nova.example.com"}
	other := []string{"hz-def-api.example.com"}
	issue(t, i, other)
	issue(t, i, env)
	issue(t, i, other) // three certificates, so the list has two pages
	// The same hostnames in another order and case are the same environment.
	issue(t, i, []string{"HZ-abc-nova.example.com", "hz-abc-api.example.com."})

	cur := f.current()
	if len(cur) != 2 {
		t.Fatalf("want one certificate per environment, have %+v", cur)
	}
	if f.types[0] != "origin-ecc" {
		t.Errorf("an ECDSA CSR is request type %q, want origin-ecc", f.types[0])
	}

	if err := i.Revoke(context.Background(), env); err != nil {
		t.Fatal(err)
	}
	if cur := f.current(); len(cur) != 1 || !cert.SameHostnames(cur[0].Hostnames, other) {
		t.Errorf("revoking one environment's certificate left %+v", cur)
	}
}

func TestValidity(t *testing.T) {
	i, _ := newTestIssuer(t, Options{Validity: 7})
	names := []string{"hz-abc-api.example.com"}
	k, csr, _ := cert.NewRequest(names)
	crt, err := i.Issue(context.Background(), names, csr)
	if err != nil {
		t.Fatal(err)
	}
	if !cert.Current(crt, k, names, time.Now().Add(4*24*time.Hour)) || cert.Current(crt, k, names, time.Now().Add(5*24*time.Hour)) {
		t.Error("a 7-day certificate must be current for its first two thirds only")
	}
}

func TestAllowedNamesProtectOtherCertificates(t *testing.T) {
	i, f := newTestIssuer(t, Options{AllowedNames: "hz-*"})
	ctx := context.Background()
	f.certs["prod"] = &certificate{ID: "prod", Hostnames: []string{"*.example.com", "example.com"}}

	for _, names := range [][]string{
		{"*.example.com", "example.com"},
		{"hz-x-api.example.com", "api.example.com"},
		{"hz-x.other.test"},
		{"hz-x.example.com.evil.test"},
		{},
	} {
		_, csr, _ := cert.NewRequest([]string{"x.example.com"})
		if _, err := i.Issue(ctx, names, csr); err == nil {
			t.Errorf("Issue(%v) must be refused", names)
		}
		if err := i.Revoke(ctx, names); err == nil {
			t.Errorf("Revoke(%v) must be refused", names)
		}
	}
	if f.certs["prod"].RevokedAt != "" {
		t.Error("the production certificate was revoked")
	}
}

func TestCheck(t *testing.T) {
	i, _ := newTestIssuer(t, Options{AllowedNames: "hz-*"})
	if err := i.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	base := i.api.BaseURL
	for name, opts := range map[string]Options{
		"bad token":     {Token: "wrong", Zone: "example.com"},
		"missing zone":  {Token: "cf-token", Zone: "nope.test"},
		"bad glob":      {Token: "cf-token", Zone: "example.com", AllowedNames: "["},
		"bad validity":  {Token: "cf-token", Zone: "example.com", Validity: 60},
		"missing token": {Zone: "example.com"},
	} {
		opts.BaseURL = base
		if err := New(opts).Check(context.Background()); err == nil {
			t.Errorf("%s must fail the check", name)
		}
	}
}

// TestIntegration issues, replaces and revokes a certificate for
// "skct-<random>.<zone>".
func TestIntegration(t *testing.T) {
	if os.Getenv("HINZIEL_IT_CLOUDFLARE") == "" {
		t.Skip("set HINZIEL_IT_CLOUDFLARE=1, CLOUDFLARE_CERT_TOKEN and CLOUDFLARE_TEST_ZONE")
	}
	zone := os.Getenv("CLOUDFLARE_TEST_ZONE")
	i := New(Options{Token: os.Getenv("CLOUDFLARE_CERT_TOKEN"), Zone: zone, AllowedNames: "skct-*", Validity: 7})
	if err := i.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	certtest.Run(t, i, zone)
}
