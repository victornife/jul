// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/crypto/acme"
	"golang.org/x/crypto/ocsp"

	"jul/internal/config"
	"jul/internal/egress"
)

// ocspTestCA is a throwaway CA that mints leaf certificates and signs OCSP
// responses for them, giving the stapler tests a self-contained PKI.
type ocspTestCA struct {
	issuer    *x509.Certificate
	issuerDER []byte
	key       *ecdsa.PrivateKey
	serial    int64
}

func newOCSPTestCA(t *testing.T) *ocspTestCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("issuer key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Jul Test Issuer"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("issuer cert: %v", err)
	}
	issuer, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse issuer: %v", err)
	}
	return &ocspTestCA{issuer: issuer, issuerDER: der, key: key, serial: 1}
}

// leaf mints a server certificate chained to the CA. When withResponder is true
// the leaf advertises an OCSP responder URL so the stapler attempts a fetch.
func (ca *ocspTestCA) leaf(t *testing.T, withResponder bool) *tls.Certificate {
	t.Helper()
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	ca.serial++
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(ca.serial),
		Subject:      pkix.Name{CommonName: "example.test"},
		DNSNames:     []string{"example.test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if withResponder {
		tmpl.OCSPServer = []string{"http://ocsp.example.test/"}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.issuer, &leafKey.PublicKey, ca.key)
	if err != nil {
		t.Fatalf("leaf cert: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return &tls.Certificate{
		Certificate: [][]byte{der, ca.issuerDER},
		PrivateKey:  leafKey,
		Leaf:        leaf,
	}
}

// response signs an OCSP response for leaf with the given status and validity.
func (ca *ocspTestCA) response(t *testing.T, leaf *x509.Certificate, status int, nextUpdate time.Time) []byte {
	t.Helper()
	tmpl := ocsp.Response{
		Status:       status,
		SerialNumber: leaf.SerialNumber,
		ThisUpdate:   time.Now().Add(-time.Hour),
		NextUpdate:   nextUpdate,
	}
	if status == ocsp.Revoked {
		tmpl.RevokedAt = time.Now().Add(-time.Hour)
		tmpl.RevocationReason = ocsp.Unspecified
	}
	der, err := ocsp.CreateResponse(ca.issuer, ca.issuer, tmpl, ca.key)
	if err != nil {
		t.Fatalf("create ocsp response: %v", err)
	}
	return der
}

// newTestStapler builds a stapler over a fixed base certificate with a stubbed
// fetch and the default clock.
func newTestStapler(cert *tls.Certificate, fetch func(context.Context, []byte, string) ([]byte, error)) *ocspStapler {
	return &ocspStapler{
		base:  certProviderFunc(func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cert, nil }),
		now:   time.Now,
		fetch: fetch,
		cache: make(map[string]*ocspStaple),
	}
}

func TestOCSPStaplerStaplesFreshResponse(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, true)
	respDER := ca.response(t, cert.Leaf, ocsp.Good, time.Now().Add(7*24*time.Hour))

	var fetched int
	st := newTestStapler(cert, func(_ context.Context, _ []byte, url string) ([]byte, error) {
		fetched++
		if url != "http://ocsp.example.test/" {
			t.Errorf("responder url = %q", url)
		}
		return respDER, nil
	})

	// Populate the cache synchronously, then serve from cache deterministically.
	st.refresh(ocspCacheKey(cert.Leaf), cert.Leaf, cert.Certificate[1])
	if fetched != 1 {
		t.Fatalf("expected exactly one fetch, got %d", fetched)
	}

	got, err := st.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if !bytes.Equal(got.OCSPStaple, respDER) {
		t.Error("served certificate is missing the OCSP staple")
	}
	if fetched != 1 {
		t.Errorf("a fresh cached staple must not trigger another fetch, got %d", fetched)
	}
	// The shared base certificate must never be mutated; only the copy carries
	// the staple.
	if len(cert.OCSPStaple) != 0 {
		t.Error("base certificate was mutated; stapler must return a copy")
	}
}

func TestOCSPStaplerSkipsChallengeHandshake(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, true)
	st := newTestStapler(cert, func(context.Context, []byte, string) ([]byte, error) {
		t.Error("fetch must not run for a TLS-ALPN-01 challenge")
		return nil, nil
	})
	got, err := st.GetCertificate(&tls.ClientHelloInfo{SupportedProtos: []string{acme.ALPNProto}})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if len(got.OCSPStaple) != 0 {
		t.Error("challenge certificate must be served unstapled")
	}
}

func TestOCSPStaplerSkipsWhenNoResponder(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, false) // no OCSPServer URL
	st := newTestStapler(cert, func(context.Context, []byte, string) ([]byte, error) {
		t.Error("fetch must not run without a responder URL")
		return nil, nil
	})
	got, err := st.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if len(got.OCSPStaple) != 0 {
		t.Error("certificate without a responder must be served unstapled")
	}
}

func TestOCSPStaplerGracefulOnFetchError(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, true)
	st := newTestStapler(cert, func(context.Context, []byte, string) ([]byte, error) {
		return nil, errors.New("responder unreachable")
	})

	st.refresh(ocspCacheKey(cert.Leaf), cert.Leaf, cert.Certificate[1])

	got, err := st.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"})
	if err != nil {
		t.Fatalf("a failed OCSP fetch must not break the handshake: %v", err)
	}
	if len(got.OCSPStaple) != 0 {
		t.Error("expected an unstapled certificate after a fetch failure")
	}
	st.mu.Lock()
	cached := st.cache[ocspCacheKey(cert.Leaf)]
	st.mu.Unlock()
	if cached == nil || cached.nextAttempt.IsZero() {
		t.Error("a failed fetch must schedule a backoff before the next attempt")
	}
}

func TestOCSPStaplerServesUnstapledOnRevoked(t *testing.T) {
	ca := newOCSPTestCA(t)
	cert := ca.leaf(t, true)
	respDER := ca.response(t, cert.Leaf, ocsp.Revoked, time.Now().Add(7*24*time.Hour))
	st := newTestStapler(cert, func(context.Context, []byte, string) ([]byte, error) {
		return respDER, nil
	})

	st.refresh(ocspCacheKey(cert.Leaf), cert.Leaf, cert.Certificate[1])

	got, err := st.GetCertificate(&tls.ClientHelloInfo{ServerName: "example.test"})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	if len(got.OCSPStaple) != 0 {
		t.Error("a non-good OCSP status must not be stapled")
	}
}

func TestProviderOCSPWrapping(t *testing.T) {
	on, off := true, false

	cfgOn := acmeServerCfg()
	cfgOn.Servers[0].TLS.ACME.OCSPStapling = &on
	mOn, err := NewACMEManager(cfgOn.Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager (ocsp on): %v", err)
	}
	if _, ok := mOn.Provider(nil).(*ocspStapler); !ok {
		t.Errorf("expected *ocspStapler when ocsp stapling is enabled, got %T", mOn.Provider(nil))
	}

	cfgOff := acmeServerCfg()
	cfgOff.Servers[0].TLS.ACME.OCSPStapling = &off
	mOff, err := NewACMEManager(cfgOff.Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager (ocsp off): %v", err)
	}
	if _, ok := mOff.Provider(nil).(*acmeProvider); !ok {
		t.Errorf("expected *acmeProvider when ocsp stapling is disabled, got %T", mOff.Provider(nil))
	}
}

// TestOCSPFetchRespectsEgressGuard proves the OCSP responder fetch is enforced
// by the injected guarded client: a responder outside the egress allow-list is
// refused at dial time, while one inside it is reached.
func TestOCSPFetchRespectsEgressGuard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ocsp-response-bytes"))
	}))
	defer srv.Close()

	ctx := context.Background()
	reqDER := []byte{0x30, 0x00} // opaque; the stub responder does not parse it

	// Blocked: the loopback responder is not inside 10.0.0.0/8.
	blocked, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	if _, err := ocspFetchWith(blocked.For(egress.SubsystemOCSP).Client(0))(ctx, reqDER, srv.URL); !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("blocked responder: err = %v, want ErrBlocked", err)
	}

	// Allowed: the loopback responder is inside 127.0.0.0/8.
	allowed, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	body, err := ocspFetchWith(allowed.For(egress.SubsystemOCSP).Client(0))(ctx, reqDER, srv.URL)
	if err != nil {
		t.Fatalf("allowed responder: %v", err)
	}
	if string(body) != "ocsp-response-bytes" {
		t.Errorf("body = %q, want the responder payload", body)
	}
}

// TestACMEDirectoryFetchRespectsEgressGuard proves the guarded HTTP client that
// NewACMEManager assigns to acme.Client.HTTPClient enforces the allow-list on
// the ACME directory fetch: a directory outside the allow-list is refused at
// dial time, while one inside it is reached and parsed. It uses a local test
// server so no external ACME endpoint is contacted.
func TestACMEDirectoryFetchRespectsEgressGuard(t *testing.T) {
	var base string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"newNonce":"` + base + `/nonce","newAccount":"` + base + `/acct","newOrder":"` + base + `/order"}`))
	}))
	defer srv.Close()
	base = srv.URL

	ctx := context.Background()

	// Blocked: the loopback directory is not inside 10.0.0.0/8.
	blocked, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"10.0.0.0/8"}})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	blockedClient := &acme.Client{HTTPClient: blocked.For(egress.SubsystemACME).Client(0), DirectoryURL: srv.URL}
	if _, err := blockedClient.Discover(ctx); !errors.Is(err, egress.ErrBlocked) {
		t.Fatalf("blocked directory: err = %v, want ErrBlocked", err)
	}

	// Allowed: the loopback directory is inside 127.0.0.0/8.
	allowed, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	allowedClient := &acme.Client{HTTPClient: allowed.For(egress.SubsystemACME).Client(0), DirectoryURL: srv.URL}
	dir, err := allowedClient.Discover(ctx)
	if err != nil {
		t.Fatalf("allowed directory: %v", err)
	}
	if dir.OrderURL != base+"/order" {
		t.Errorf("directory OrderURL = %q, want %q", dir.OrderURL, base+"/order")
	}
}
