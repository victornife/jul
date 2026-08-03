// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"golang.org/x/crypto/acme"

	"jul/internal/config"
)

func TestNewACMEManagerNilWhenNoACME(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{Listen: ":80"}}}
	mgr, err := NewACMEManager(cfg.Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if mgr != nil {
		t.Error("expected nil manager when no block enables acme")
	}
}

func TestNewACMEManagerBuildsManager(t *testing.T) {
	mgr, err := NewACMEManager(acmeServerCfg().Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	if mgr == nil {
		t.Fatal("expected a manager when a block enables acme")
	}
	if !ACMECompiled {
		t.Error("ACMECompiled must be true with the acme tag")
	}
	if got := mgr.(*acmeManager).challenge; got != "http-01" {
		t.Fatalf("manager challenge = %q, want http-01", got)
	}
}

func TestNewACMEManagerWiresGuardedClient(t *testing.T) {
	guarded := &http.Client{}
	m, err := NewACMEManager(acmeServerCfg().Servers, nil, guarded, nil)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	if got := m.(*acmeManager).mgr.Client.HTTPClient; got != guarded {
		t.Errorf("acme.Client.HTTPClient = %p, want the guarded client %p", got, guarded)
	}

	// A nil client preserves the default (nil) acme HTTP client so an
	// egress-disabled build is unchanged.
	m2, err := NewACMEManager(acmeServerCfg().Servers, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewACMEManager: %v", err)
	}
	if got := m2.(*acmeManager).mgr.Client.HTTPClient; got != nil {
		t.Errorf("nil client must leave acme.Client.HTTPClient nil, got %p", got)
	}
}

func TestDirectoryURL(t *testing.T) {
	cases := map[string]string{
		"letsencrypt":         acme.LetsEncryptURL,
		"letsencrypt-staging": letsEncryptStagingURL,
		"":                    letsEncryptStagingURL,
		"https://ca.test/dir": "https://ca.test/dir",
	}
	for in, want := range cases {
		if got := directoryURL(in); got != want {
			t.Errorf("directoryURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestACMEChallengeHandlerRouting(t *testing.T) {
	mgr, err := NewACMEManager(acmeServerCfg().Servers, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	fallbackHit := false
	h := mgr.ChallengeHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		fallbackHit = true
	}))

	// A challenge path is handled by autocert itself (unknown token -> 404),
	// never reaching the fallback.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/missing", nil))
	if fallbackHit {
		t.Error("challenge request must not reach the fallback")
	}

	// A normal request is delegated to the fallback.
	fallbackHit = false
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if !fallbackHit {
		t.Error("non-challenge request must reach the fallback")
	}
}

func TestACMETLSALPNChallengeDoesNotInstallHTTPHandler(t *testing.T) {
	cfg := acmeServerCfg()
	cfg.Servers[0].TLS.ACME.Challenge = "tls-alpn-01"
	mgr, err := NewACMEManager(cfg.Servers, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := mgr.(*acmeManager).challenge; got != "tls-alpn-01" {
		t.Fatalf("manager challenge = %q, want tls-alpn-01", got)
	}

	fallbackHit := false
	h := mgr.ChallengeHandler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fallbackHit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/.well-known/acme-challenge/not-installed", nil))
	if !fallbackHit {
		t.Fatal("TLS-ALPN-01 must leave the plain HTTP challenge path on the normal handler")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}
