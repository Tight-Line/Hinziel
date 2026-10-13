// Package cert defines how a deployer gets a TLS certificate for an
// environment's hostnames from a certificate authority (Cloudflare's Origin
// CA, …), so each environment has a key and certificate of its own.
// Implementations live in subpackages.
//
// The deployer generates the key (NewRequest) and keeps it with the
// environment; the issuer only ever sees the CSR. Certificates are identified
// by their hostnames, so nothing needs to be persisted to replace or revoke
// them later.
package cert

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"slices"
	"strings"
	"time"
)

// Issuer issues and revokes certificates.
type Issuer interface {
	// Issue returns a PEM certificate (with any intermediates) for csr, valid
	// for exactly hostnames. It revokes every earlier certificate it issued
	// for the same hostnames, so an environment has one certificate at a
	// time, even when its key was lost.
	Issue(ctx context.Context, hostnames []string, csr []byte) ([]byte, error)
	// Revoke revokes every certificate it issued for exactly hostnames.
	// Revoking what is already gone is not an error.
	Revoke(ctx context.Context, hostnames []string) error
}

// NewRequest generates an ECDSA P-256 key and a CSR for hostnames, both PEM.
func NewRequest(hostnames []string) (key, csr []byte, err error) {
	if len(hostnames) == 0 {
		return nil, nil, errors.New("a certificate needs at least one hostname")
	}
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return nil, nil, err
	}
	req, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{CommonName: hostnames[0]},
		DNSNames: hostnames,
	}, k)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: req}), nil
}

// Current reports whether certPEM and keyPEM are a pair, valid for exactly
// hostnames, and have more than a third of their lifetime left at now.
func Current(certPEM, keyPEM []byte, hostnames []string, now time.Time) bool {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return false
	}
	c, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !SameHostnames(c.DNSNames, hostnames) {
		return false
	}
	return now.After(c.NotBefore) && c.NotAfter.Sub(now) > c.NotAfter.Sub(c.NotBefore)/3
}

// SameHostnames reports whether a and b name the same hostnames, ignoring
// order, case, duplicates and a trailing dot.
func SameHostnames(a, b []string) bool {
	return slices.Equal(normalize(a), normalize(b))
}

func normalize(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.TrimSuffix(strings.ToLower(n), ".")
	}
	slices.Sort(out)
	return slices.Compact(out)
}
