// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/egress"
)

// These tests prove the egress seam: an Options.DialContext is installed on the
// default JWKS and forward-auth clients, so a guarded dial that refuses the
// destination fails the fetch (SEQ-07 / #33). A passthrough dial is the control
// showing the guard does not otherwise change behaviour.

func blockDial(context.Context, string, string) (net.Conn, error) {
	return nil, errors.New("egress: blocked in test")
}

func passDial(ctx context.Context, network, addr string) (net.Conn, error) {
	return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, addr)
}

func newAuthWithDial(t *testing.T, cfg config.AuthConfig, dial DialFunc) *Authenticator {
	t.Helper()
	a, err := New(context.Background(), cfg, Options{DialContext: dial})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func serveStatus(a *Authenticator, r *http.Request) int {
	rec := httptest.NewRecorder()
	a.Wrap(&okHandler{}).ServeHTTP(rec, r)
	return rec.Code
}

func TestEgressDialContextGuardsJWKS(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	srv := newJWKSServer(t, rsaJWK("rsa-1", &key.PublicKey))
	cfg := config.AuthConfig{JWT: &config.JWTAuthConfig{
		JWKSURL:    srv.URL,
		Issuer:     "https://issuer.example",
		Audience:   "my-api",
		Algorithms: defaultAlgs(),
	}}
	token := signRS256(t, key, "rsa-1", validClaims())

	// Blocked egress: the JWKS fetch cannot connect, so no key is available and
	// the token is rejected.
	if code := serveStatus(newAuthWithDial(t, cfg, blockDial), bearerReq(token)); code != http.StatusUnauthorized {
		t.Errorf("blocked egress: code = %d, want 401", code)
	}
	// Passthrough egress: the JWKS fetch succeeds and the token validates.
	if code := serveStatus(newAuthWithDial(t, cfg, passDial), bearerReq(token)); code != http.StatusOK {
		t.Errorf("open egress: code = %d, want 200", code)
	}
}

func TestEgressDialContextGuardsForward(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Forwarded-Uri") == "/allow" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	cfg := config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{URL: srv.URL}}

	newReq := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
	}

	// Blocked egress: the forward-auth subrequest cannot connect, surfaced as a
	// 503 (auth service unreachable) rather than a silent allow.
	if code := serveStatus(newAuthWithDial(t, cfg, blockDial), newReq()); code != http.StatusServiceUnavailable {
		t.Errorf("blocked egress: code = %d, want 503", code)
	}
	// Passthrough egress: the subrequest reaches the auth service and allows.
	if code := serveStatus(newAuthWithDial(t, cfg, passDial), newReq()); code != http.StatusOK {
		t.Errorf("open egress: code = %d, want 200", code)
	}
}

// An environment HTTP proxy may itself be allow-listed for other auxiliary
// traffic. Forward-auth must still check its configured destination, not only
// the proxy's dial address, when the egress policy is enabled.
func TestEgressForwardDoesNotUseAllowedEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	policy, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	guard := policy.For(egress.SubsystemAuth).DialContext(nil)
	a := newAuthWithDial(t, config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{
		URL: "http://blocked.example.invalid/auth",
	}}, guard)
	if transport := a.forward.dep.client.Transport.(*http.Transport); transport.Proxy != nil {
		t.Fatal("guarded auth transport may route a blocked target through an environment proxy")
	}
	request := httptest.NewRequest(http.MethodGet, "http://app.example/allow", nil)
	if code := serveStatus(a, request); code != http.StatusServiceUnavailable {
		t.Errorf("forward-auth status = %d, want 503 for blocked target", code)
	}
	if got := proxyCalls.Load(); got != 0 {
		t.Errorf("proxy received %d requests for a blocked target", got)
	}
	// A disabled policy does not supply a guarded dial, so the historical
	// environment-proxy behaviour remains available to this dependency.
	open := newAuthWithDial(t, config.AuthConfig{ForwardAuth: &config.ForwardAuthConfig{
		URL: "http://blocked.example.invalid/auth",
	}}, nil)
	if open.forward.dep.client.Transport != nil {
		t.Error("disabled egress should retain the default HTTP transport and proxy behavior")
	}
}
