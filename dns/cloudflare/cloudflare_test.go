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

	"github.com/Tight-Line/heinzel/dns"
	"github.com/Tight-Line/heinzel/dns/dnstest"
)

// fakeCloudflare serves zones and DNS records for one zone, example.com.
type fakeCloudflare struct {
	mu      sync.Mutex
	records map[string]record // by ID
	next    int
	calls   int
}

func (f *fakeCloudflare) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	ok := func(v any) { json.NewEncoder(w).Encode(map[string]any{"success": true, "errors": []any{}, "result": v}) }
	if r.Header.Get("Authorization") != "Bearer cf-token" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 9109, "message": "Invalid access token"}}})
		return
	}
	var body record
	json.NewDecoder(r.Body).Decode(&body)
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	switch {
	case r.URL.Path == "/zones":
		if r.URL.Query().Get("name") == "example.com" {
			ok([]map[string]string{{"id": "z1"}})
			return
		}
		ok([]any{})
	case len(parts) == 3 && parts[2] == "dns_records" && r.Method == "GET":
		out := []record{}
		for _, rec := range f.records {
			if name := r.URL.Query().Get("name"); name == "" || rec.Name == name {
				out = append(out, rec)
			}
		}
		ok(out)
	case len(parts) == 3 && r.Method == "POST":
		f.next++
		body.ID = "r" + strconv.Itoa(f.next)
		f.records[body.ID] = body
		ok(body)
	case len(parts) == 4 && r.Method == "PUT":
		body.ID = parts[3]
		f.records[parts[3]] = body
		ok(body)
	case len(parts) == 4 && r.Method == "DELETE":
		if _, found := f.records[parts[3]]; !found {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 81044, "message": "Record does not exist."}}})
			return
		}
		delete(f.records, parts[3])
		ok(map[string]string{"id": parts[3]})
	default:
		http.NotFound(w, r)
	}
}

func newTestProvider(t *testing.T, opts Options) (*Provider, *fakeCloudflare) {
	f := &fakeCloudflare{records: map[string]record{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	opts.Token, opts.Zone, opts.BaseURL = "cf-token", "example.com", srv.URL
	return New(opts), f
}

func (f *fakeCloudflare) byName(name string) []record {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []record
	for _, r := range f.records {
		if r.Name == name {
			out = append(out, r)
		}
	}
	return out
}

func TestConformance(t *testing.T) {
	p, _ := newTestProvider(t, Options{Proxied: true})
	dnstest.Run(t, p, "example.com")
}

func TestEnsure(t *testing.T) {
	p, f := newTestProvider(t, Options{Proxied: true})
	ctx := context.Background()
	name := "hz-abc-api.example.com"

	if err := p.Ensure(ctx, dns.Record{Name: name, Type: "A", Value: "203.0.113.10"}); err != nil {
		t.Fatal(err)
	}
	got := f.byName(name)
	if len(got) != 1 || got[0].Content != "203.0.113.10" || !got[0].Proxied || got[0].TTL != 1 {
		t.Fatalf("records %+v", got)
	}

	calls := f.calls
	p.Ensure(ctx, dns.Record{Name: name, Type: "A", Value: "203.0.113.10"})
	if f.calls-calls != 1 {
		t.Errorf("an unchanged record must only be read, made %d calls", f.calls-calls)
	}

	// Switching to a CNAME replaces the A record.
	if err := p.Ensure(ctx, dns.Record{Name: name, Type: "CNAME", Value: "x.pages.dev"}); err != nil {
		t.Fatal(err)
	}
	if got := f.byName(name); len(got) != 1 || got[0].Type != "CNAME" {
		t.Fatalf("records after type change %+v", got)
	}
}

func TestAllowedNamesProtectOtherRecords(t *testing.T) {
	p, f := newTestProvider(t, Options{AllowedNames: "hz-*"})
	ctx := context.Background()
	f.records["prod"] = record{ID: "prod", Type: "A", Name: "api.example.com", Content: "198.51.100.1"}

	for _, name := range []string{"api.example.com", "example.com", "hz-x.api.example.com.evil.test", "hz-x.other.test"} {
		if err := p.Ensure(ctx, dns.Record{Name: name, Type: "A", Value: "203.0.113.10"}); err == nil {
			t.Errorf("Ensure(%s) must be refused", name)
		}
		if err := p.Delete(ctx, name); err == nil {
			t.Errorf("Delete(%s) must be refused", name)
		}
	}
	if f.records["prod"].Content != "198.51.100.1" {
		t.Error("the production record changed")
	}
	// DNS names are case-insensitive and may end in a dot.
	if err := p.Ensure(ctx, dns.Record{Name: "HZ-x.Example.com.", Type: "A", Value: "203.0.113.10"}); err != nil {
		t.Errorf("a matching name in another case must pass: %v", err)
	}
}

func TestCheck(t *testing.T) {
	p, _ := newTestProvider(t, Options{AllowedNames: "hz-*"})
	if err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad := New(Options{Token: "wrong", Zone: "example.com", BaseURL: p.api.BaseURL})
	if err := bad.Check(context.Background()); err == nil {
		t.Error("a bad token must fail the check")
	}
	missing := New(Options{Token: "cf-token", Zone: "nope.test", BaseURL: p.api.BaseURL})
	if err := missing.Check(context.Background()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("a missing zone must fail the check: %v", err)
	}
	badGlob := New(Options{Token: "cf-token", Zone: "example.com", AllowedNames: "[", BaseURL: p.api.BaseURL})
	if err := badGlob.Check(context.Background()); err == nil {
		t.Error("a malformed AllowedNames must fail the check")
	}
}

// TestIntegration creates, updates and deletes a record "hzct-<random>.<zone>".
func TestIntegration(t *testing.T) {
	if os.Getenv("HEINZEL_IT_CLOUDFLARE") == "" {
		t.Skip("set HEINZEL_IT_CLOUDFLARE=1, CLOUDFLARE_DNS_TOKEN and CLOUDFLARE_TEST_ZONE")
	}
	zone := os.Getenv("CLOUDFLARE_TEST_ZONE")
	p := New(Options{Token: os.Getenv("CLOUDFLARE_DNS_TOKEN"), Zone: zone, AllowedNames: "hzct-*"})
	if err := p.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	dnstest.Run(t, p, zone)
}
