// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
)

// okHandler is a next-handler that records whether it ran and echoes any claims
// found in the request context.
type okHandler struct {
	mu     sync.Mutex
	ran    bool
	claims map[string]any
	header http.Header
}

func (h *okHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.ran = true
	h.claims = middleware.ClaimsFrom(r.Context())
	h.header = r.Header.Clone()
	h.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func newAuth(t *testing.T, cfg config.AuthConfig, onDecision func(string, string)) *Authenticator {
	t.Helper()
	a, err := New(cfg, Options{OnDecision: onDecision})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func TestWrapCIDR(t *testing.T) {
	var decisions [][2]string
	a := newAuth(t, config.AuthConfig{
		Allow: []string{"10.0.0.0/8"},
		Deny:  []string{"10.9.0.0/16"},
	}, func(m, r string) { decisions = append(decisions, [2]string{m, r}) })

	next := &okHandler{}
	h := a.Wrap(next)

	t.Run("allowed", func(t *testing.T) {
		next.ran = false
		decisions = nil
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.1.2.3:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if !next.ran || rec.Code != http.StatusOK {
			t.Errorf("ran=%v code=%d, want true/200", next.ran, rec.Code)
		}
		if len(decisions) != 1 || decisions[0] != [2]string{"cidr", "allow"} {
			t.Errorf("decisions = %v, want one cidr/allow", decisions)
		}
	})

	t.Run("denied by deny list", func(t *testing.T) {
		next.ran = false
		decisions = nil
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.9.1.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusForbidden {
			t.Errorf("ran=%v code=%d, want false/403", next.ran, rec.Code)
		}
		if len(decisions) != 1 || decisions[0] != [2]string{"cidr", "deny"} {
			t.Errorf("decisions = %v, want one cidr/deny", decisions)
		}
	})

	t.Run("denied outside allow list", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "192.168.1.1:5555"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusForbidden {
			t.Errorf("ran=%v code=%d, want false/403", next.ran, rec.Code)
		}
	})
}

func TestWrapBasic(t *testing.T) {
	path := writeHtpasswd(t, map[string]string{"alice": "s3cret"})
	a := newAuth(t, config.AuthConfig{
		Basic: &config.BasicAuthConfig{File: path, Realm: "Restricted"},
	}, nil)
	next := &okHandler{}
	h := a.Wrap(next)

	t.Run("no credentials challenges", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusUnauthorized {
			t.Errorf("ran=%v code=%d, want false/401", next.ran, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Error("expected WWW-Authenticate header")
		}
	})

	t.Run("valid credentials pass", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.SetBasicAuth("alice", "s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if !next.ran || rec.Code != http.StatusOK {
			t.Errorf("ran=%v code=%d, want true/200", next.ran, rec.Code)
		}
	})

	t.Run("cidr gate applies before basic", func(t *testing.T) {
		a := newAuth(t, config.AuthConfig{
			Deny:  []string{"10.0.0.0/8"},
			Basic: &config.BasicAuthConfig{File: path, Realm: "Restricted"},
		}, nil)
		next := &okHandler{}
		h := a.Wrap(next)
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.1.1.1:80"
		r.SetBasicAuth("alice", "s3cret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusForbidden {
			t.Errorf("ran=%v code=%d, want false/403 (CIDR denies before basic)", next.ran, rec.Code)
		}
	})
}

func TestWrapJWTClaimsInContext(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	srv := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
	a := newAuth(t, config.AuthConfig{
		JWT: &config.JWTAuthConfig{
			JWKSURL:    srv.URL,
			Issuer:     "https://issuer.example",
			Audience:   "my-api",
			Algorithms: defaultAlgs(),
		},
	}, nil)
	// Point the validator's HTTP client at the test server.
	a.jwt.jwks.client = srv.Client()

	next := &okHandler{}
	h := a.Wrap(next)

	t.Run("valid token attaches claims", func(t *testing.T) {
		next.ran = false
		r := bearerReq(signRS256(t, key, "rsa-1", validClaims()))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if !next.ran || rec.Code != http.StatusOK {
			t.Fatalf("ran=%v code=%d, want true/200", next.ran, rec.Code)
		}
		if next.claims["sub"] != "user-123" {
			t.Errorf("claims sub = %v, want user-123", next.claims["sub"])
		}
	})

	t.Run("invalid token is rejected", func(t *testing.T) {
		next.ran = false
		r := bearerReq("not-a-jwt")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusUnauthorized {
			t.Errorf("ran=%v code=%d, want false/401", next.ran, rec.Code)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Error("expected WWW-Authenticate header")
		}
	})
}

func TestWrapForwardAuth(t *testing.T) {
	authSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Uri") == "/allow" {
			w.Header().Set("X-Auth-User", "bob")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer authSrv.Close()

	a := newAuth(t, config.AuthConfig{
		ForwardAuth: &config.ForwardAuthConfig{
			URL:                 authSrv.URL,
			AuthResponseHeaders: []string{"X-Auth-User"},
		},
	}, nil)
	a.forward.client = authSrv.Client()

	next := &okHandler{}
	h := a.Wrap(next)

	t.Run("allow merges response headers", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if !next.ran || rec.Code != http.StatusOK {
			t.Fatalf("ran=%v code=%d, want true/200", next.ran, rec.Code)
		}
		if next.header.Get("X-Auth-User") != "bob" {
			t.Errorf("upstream X-Auth-User = %q, want bob", next.header.Get("X-Auth-User"))
		}
	})

	t.Run("deny blocks request", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "http://app.example/deny", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.ran || rec.Code != http.StatusForbidden {
			t.Errorf("ran=%v code=%d, want false/403", next.ran, rec.Code)
		}
	})

	t.Run("client cannot spoof identity header", func(t *testing.T) {
		next.ran = false
		r := httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
		r.Header.Set("X-Auth-User", "attacker")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if next.header.Get("X-Auth-User") != "bob" {
			t.Errorf("X-Auth-User = %q, want bob (auth service value, not client-supplied)", next.header.Get("X-Auth-User"))
		}
	})
}
