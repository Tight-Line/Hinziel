// Package dnstest provides an in-memory DNS provider for tests and a
// conformance suite for dns.Provider implementations.
package dnstest

import (
	"context"
	"maps"
	"sync"
	"testing"

	"github.com/Tight-Line/hinzele/dns"
	"github.com/Tight-Line/hinzele/internal/testutil"
)

// Fake is an in-memory dns.Provider.
type Fake struct {
	mu      sync.Mutex
	records map[string]dns.Record

	EnsureErrs []error // returned by the next Ensure calls, in order
}

var _ dns.Provider = (*Fake)(nil)

func NewFake() *Fake { return &Fake{records: map[string]dns.Record{}} }

func (f *Fake) Ensure(_ context.Context, r dns.Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := testutil.PopErr(&f.EnsureErrs); err != nil {
		return err
	}
	f.records[r.Name] = r
	return nil
}

func (f *Fake) Delete(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.records, name)
	return nil
}

// Records returns the current records, by name.
func (f *Fake) Records() map[string]dns.Record {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.records)
}

// Run checks p against the DNS provider contract by creating, updating and
// deleting a record "skct-<random>.<zone>".
func Run(t *testing.T, p dns.Provider, zone string) {
	ctx := context.Background()
	name := "skct-" + testutil.Suffix() + "." + zone
	t.Cleanup(func() { p.Delete(context.Background(), name) })

	if err := p.Ensure(ctx, dns.Record{Name: name, Type: "A", Value: "203.0.113.10"}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	t.Run("EnsureUpdates", func(t *testing.T) {
		if err := p.Ensure(ctx, dns.Record{Name: name, Type: "A", Value: "203.0.113.11"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("DeleteIsIdempotent", func(t *testing.T) {
		if err := p.Delete(ctx, name); err != nil {
			t.Fatal(err)
		}
		if err := p.Delete(ctx, name); err != nil {
			t.Fatalf("deleting a missing record must not fail: %v", err)
		}
	})
}
