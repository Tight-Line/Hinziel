// Package certtest provides an in-memory certificate authority for tests and
// a conformance suite for cert.Issuer implementations.
package certtest

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tight-Line/hinziel/cert"
	"github.com/Tight-Line/hinziel/internal/testutil"
)

// Fake is an in-memory cert.Issuer: a CA of its own signs every CSR.
type Fake struct {
	// Validity of issued certificates; 0 = 90 days.
	Validity time.Duration

	mu      sync.Mutex
	ca      *x509.Certificate
	caKey   *ecdsa.PrivateKey
	serial  int64
	current map[string]int // unrevoked certificates, by key(hostnames)
	revoked int

	IssueErrs  []error // returned by the next Issue calls, in order
	RevokeErrs []error // returned by the next Revoke calls, in order
}

var _ cert.Issuer = (*Fake)(nil)

func NewFake() *Fake {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		panic(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "certtest CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * 365 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		panic(err)
	}
	ca, _ := x509.ParseCertificate(der)
	return &Fake{ca: ca, caKey: k, serial: 1, current: map[string]int{}}
}

// key identifies a set of hostnames, as cert.SameHostnames compares them.
func key(hostnames []string) string {
	names := make([]string, len(hostnames))
	for i, n := range hostnames {
		names[i] = strings.TrimSuffix(strings.ToLower(n), ".")
	}
	slices.Sort(names)
	return strings.Join(slices.Compact(names), " ")
}

func (f *Fake) Issue(_ context.Context, hostnames []string, csrPEM []byte) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := testutil.PopErr(&f.IssueErrs); err != nil {
		return nil, err
	}
	block, _ := pem.Decode(csrPEM)
	if block == nil {
		return nil, errors.New("certtest: CSR is not PEM")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, err
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, err
	}
	validity := f.Validity
	if validity == 0 {
		validity = 90 * 24 * time.Hour
	}
	f.serial++
	now := time.Now()
	der, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(f.serial),
		Subject:      pkix.Name{CommonName: hostnames[0]},
		DNSNames:     hostnames,
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, f.ca, csr.PublicKey, f.caKey)
	if err != nil {
		return nil, err
	}
	k := key(hostnames)
	f.revoked += f.current[k]
	f.current[k] = 1
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func (f *Fake) Revoke(_ context.Context, hostnames []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := testutil.PopErr(&f.RevokeErrs); err != nil {
		return err
	}
	f.revoked += f.current[key(hostnames)]
	delete(f.current, key(hostnames))
	return nil
}

// Current returns the number of unrevoked certificates for hostnames.
func (f *Fake) Current(hostnames []string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current[key(hostnames)]
}

// Revoked returns the number of certificates revoked so far.
func (f *Fake) Revoked() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.revoked
}

// Run checks i against the cert.Issuer contract by issuing, replacing and
// revoking a certificate for "skct-<random>.<zone>".
func Run(t *testing.T, i cert.Issuer, zone string) {
	ctx := context.Background()
	names := []string{"skct-" + testutil.Suffix() + "." + zone}
	t.Cleanup(func() { i.Revoke(context.Background(), names) })

	issue := func(t *testing.T) {
		t.Helper()
		k, csr, err := cert.NewRequest(names)
		if err != nil {
			t.Fatal(err)
		}
		crt, err := i.Issue(ctx, names, csr)
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if !cert.Current(crt, k, names, time.Now()) {
			t.Fatalf("the issued certificate doesn't match the key and hostnames %v:\n%s", names, crt)
		}
	}
	issue(t)
	t.Run("IssueAgain", issue)
	t.Run("RevokeIsIdempotent", func(t *testing.T) {
		if err := i.Revoke(ctx, names); err != nil {
			t.Fatal(err)
		}
		if err := i.Revoke(ctx, names); err != nil {
			t.Fatalf("revoking what is gone must not fail: %v", err)
		}
	})
}
