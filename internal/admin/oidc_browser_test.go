// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"jul/internal/adminoidc/oidctest"
	"jul/internal/config"
)

// TestOIDCBrowserHarness is an opt-in loopback-only fixture for Playwright.
// No test control endpoint is compiled into the shipped gateway.
func TestOIDCBrowserHarness(t *testing.T) {
	if os.Getenv("JUL_OIDC_BROWSER") != "1" {
		t.Skip("dedicated browser fixture")
	}
	if !consoleV2Compiled {
		t.Fatal("browser fixture requires console build tag")
	}
	provider, err := oidctest.New()
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	cfg := config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9296", Token: "rrrrrrrrrrrrrrrrrrrrrrrrrrrrrrrr", RBAC: config.AdminRBACConfig{Enabled: true}, OIDC: config.AdminOIDCConfig{Enabled: true, Issuer: provider.URL(), ClientID: "jul", CallbackURL: "http://127.0.0.1:9296/auth/oidc/callback", SessionIdle: config.Duration(10 * time.Second), SessionAbsolute: config.Duration(15 * time.Second), MaxTransactions: 128, Subjects: []config.AdminOIDCSubject{{Subject: "operator-1", Principal: "alice", Role: "viewer"}}}}
	s := New(cfg, testLogger(t), Deps{OIDCClient: provider.Server.Client()})
	s.UpdateAuth(cfg, oidcPolicy(t, "viewer"))
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	defer server.Close()
	go server.Serve(listener)
	controlListener, err := net.Listen("tcp", "127.0.0.1:9297")
	if err != nil {
		t.Fatal(err)
	}
	var controlMu sync.Mutex
	control := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controlMu.Lock()
		defer controlMu.Unlock()
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/outage":
			provider.Configure("outage", nil)
		case "/recover":
			provider.Configure("", nil)
			// Each browser test starts with a fresh auth generation and cold
			// discovery, so outage tests do not inherit a prior cached provider.
			cfg.OIDC.MaxTransactions++
			s.UpdateAuth(cfg, oidcPolicy(t, "viewer"))
		case "/nonce":
			provider.Configure("", map[string]any{"nonce": "wrong"})
		default:
			w.WriteHeader(404)
			return
		}
		w.WriteHeader(204)
	}), ReadHeaderTimeout: 5 * time.Second}
	defer control.Close()
	go control.Serve(controlListener)
	select {
	case <-t.Context().Done():
	case <-time.After(5 * time.Minute):
	}
}
