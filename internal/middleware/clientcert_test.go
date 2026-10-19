// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testLeaf builds a parsed leaf certificate with the given common name and DNS
// SAN for identity-extraction assertions.
func testLeaf(t *testing.T, cn string, dns ...string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(0x4d2),
		Subject:      pkix.Name{CommonName: cn, Organization: []string{"Jul Test"}},
		Issuer:       pkix.Name{CommonName: "Issuer CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     dns,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestClientCertRequireNoCert(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("handler should not run without a required client certificate")
	})
	h := ClientCert(true, ForwardCertNone)(next)

	// No TLS state at all.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
}

func TestClientCertOptionalNoCertPasses(t *testing.T) {
	var ran bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ran = true
		if PeerCertIdentityFrom(r.Context()) != nil {
			t.Error("expected no identity when no certificate is presented")
		}
	})
	h := ClientCert(false, ForwardCertNone)(next)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://x/", nil))
	if !ran {
		t.Error("handler should run when a client certificate is optional and absent")
	}
}

func TestClientCertPopulatesIdentity(t *testing.T) {
	leaf := testLeaf(t, "alice", "alice.example.com")

	var got *PeerCertIdentity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = PeerCertIdentityFrom(r.Context())
	})
	h := ClientCert(true, ForwardCertNone)(next)

	req := httptest.NewRequest(http.MethodGet, "https://x/", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got == nil {
		t.Fatal("expected an identity in context")
	}
	if !got.Verified {
		t.Error("identity should be marked verified")
	}
	if got.CN != "alice" {
		t.Errorf("CN = %q, want alice", got.CN)
	}
	if got.SANs != "alice.example.com" {
		t.Errorf("SANs = %q, want alice.example.com", got.SANs)
	}
	if got.Serial != "1234" {
		t.Errorf("Serial = %q, want 1234", got.Serial)
	}
	if len(got.Fingerprint) != 64 {
		t.Errorf("Fingerprint = %q, want 64 hex chars", got.Fingerprint)
	}
}

func TestClientCertWithVerifierAndForwardModes(t *testing.T) {
	leaf := testLeaf(t, "bob", "bob.example.com")
	inter := testLeaf(t, "inter")

	var gotID *PeerCertIdentity
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotID = PeerCertIdentityFrom(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	// 1. Context verifier returns verified=true -> populates identity and chain.
	hChain := ClientCertWithVerifier(true, ForwardCertChain, nil)(next)
	req1 := httptest.NewRequest(http.MethodGet, "https://x/", nil)
	req1.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf, inter}}
	req1 = req1.WithContext(WithClientCertVerifier(req1.Context(), func(_ *tls.ConnectionState, _ bool) (bool, error) {
		return true, nil
	}))
	rec1 := httptest.NewRecorder()
	hChain.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK || gotID == nil || len(gotID.Raw) == 0 || len(gotID.Chain) != 1 {
		t.Fatalf("expected 200 with chain identity, got code=%d id=%+v", rec1.Code, gotID)
	}

	// 2. Fallback verifier returns verified=false, nil (optional cert absent/unverified) -> proceeds without identity.
	gotID = nil
	hFallbackOpt := ClientCertWithVerifier(false, ForwardCertLeaf, func(_ *tls.ConnectionState, _ bool) (bool, error) {
		return false, nil
	})(next)
	req2 := httptest.NewRequest(http.MethodGet, "https://x/", nil)
	rec2 := httptest.NewRecorder()
	hFallbackOpt.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || gotID != nil {
		t.Fatalf("expected 200 with nil identity on optional unverified cert, got code=%d id=%+v", rec2.Code, gotID)
	}

	// 3. Fallback verifier returns error -> 403 Forbidden.
	hFallbackRej := ClientCertWithVerifier(true, ForwardCertLeaf, func(_ *tls.ConnectionState, _ bool) (bool, error) {
		return false, http.ErrAbortHandler
	})(next)
	req3 := httptest.NewRequest(http.MethodGet, "https://x/", nil)
	rec3 := httptest.NewRecorder()
	hFallbackRej.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusForbidden {
		t.Fatalf("expected 403 when verifier rejects, got %d", rec3.Code)
	}
}
