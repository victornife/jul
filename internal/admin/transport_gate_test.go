// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// withLocalAddr attaches the local address net/http sets on every connection it
// serves, so a test can present a request as having arrived on a particular
// listener without opening a socket.
func withLocalAddr(r *http.Request, addr string) *http.Request {
	a, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		panic(err)
	}
	return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, a))
}

func decodeEnvelope(t *testing.T, rr *httptest.ResponseRecorder) adminapi.Envelope {
	t.Helper()
	var env adminapi.Envelope
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an error envelope: %v\nbody: %s", err, rr.Body.String())
	}
	return env
}

// TestTransportGateRefusesPlaintextOnNonLoopback is §28.1's core assertion, and
// it deliberately covers a *read* as well as a write, and /metrics as well as
// the configuration surface. The earlier, narrower scoping permitted
// authenticated plaintext reads and exempted the legacy routes — which was
// ineffective, because the legacy single-token identity is a wildcard principal
// holding read and write, so a token disclosed by a permitted read could simply
// be replayed against an exempt mutation.
func TestTransportGateRefusesPlaintextOnNonLoopback(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090", Token: "secret-token"}, Deps{})
	h := s.routes()

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{"a v1 mutation", http.MethodPost, "/api/v1/config/apply"},
		{"a v1 read", http.MethodGet, "/api/v1/status"},
		{"a legacy read", http.MethodGet, "/api/status"},
		{"a legacy mutation", http.MethodPost, "/api/config/apply"},
		{"metrics", http.MethodGet, "/metrics"},
		{"a route that does not exist", http.MethodGet, "/api/nope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := withLocalAddr(httptest.NewRequest(tc.method, tc.path, nil), "203.0.113.7:9090")
			req.Header.Set("Authorization", "Bearer secret-token")
			h.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403", rr.Code)
			}
			env := decodeEnvelope(t, rr)
			if env.Error.Code != adminapi.CodeInsecureTransport {
				t.Fatalf("code = %q, want insecure_transport", env.Error.Code)
			}
			if env.Error.Details.Required != "tls_or_loopback" {
				t.Fatalf("details.required = %q, want tls_or_loopback", env.Error.Details.Required)
			}
			if env.Error.RequestID == "" {
				t.Fatal("the refusal carries no request_id, so an operator cannot correlate it with the server log")
			}
			if got := rr.Header().Get("X-Request-ID"); got != env.Error.RequestID {
				t.Fatalf("X-Request-ID = %q, envelope request_id = %q; they must agree", got, env.Error.RequestID)
			}
		})
	}
}

// TestTransportGateRunsBeforeAuthentication proves the ordering property by
// proving the *credential was never evaluated*: a request carrying a token the
// server would reject, and a request carrying no token at all, produce the same
// 403 insecure_transport rather than a 401.
//
// This is also the documented exception to §28's existence-disclosure rule,
// where an unauthenticated caller would otherwise receive a 401. It discloses
// nothing: the verdict is identical for every request and every principal.
func TestTransportGateRunsBeforeAuthentication(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090", Token: "secret-token"}, Deps{})
	h := s.routes()

	for _, auth := range []string{"", "Bearer secret-token", "Bearer wrong-token", "Bearer "} {
		rr := httptest.NewRecorder()
		req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "198.51.100.4:9090")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		h.ServeHTTP(rr, req)

		if rr.Code != http.StatusForbidden {
			t.Fatalf("Authorization=%q produced %d; every credential state must produce the same 403, "+
				"otherwise the response is an oracle for whether the credential was evaluated", auth, rr.Code)
		}
		if env := decodeEnvelope(t, rr); env.Error.Code != adminapi.CodeInsecureTransport {
			t.Fatalf("Authorization=%q produced code %q, want insecure_transport", auth, env.Error.Code)
		}
	}
}

// TestTransportGateAllowsLoopbackAndTLS covers the two ways to satisfy it.
func TestTransportGateAllowsLoopbackAndTLS(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090"}, Deps{})
	h := s.routes()

	t.Run("loopback connection on a wildcard bind", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "127.0.0.1:9090")
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("a loopback connection was refused: %s", rr.Body.String())
		}
	})

	t.Run("IPv6 loopback", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "[::1]:9090")
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("an IPv6 loopback connection was refused: %s", rr.Body.String())
		}
	})

	t.Run("TLS on a public address", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "203.0.113.7:9443")
		req.TLS = &tls.ConnectionState{HandshakeComplete: true, Version: tls.VersionTLS13}
		h.ServeHTTP(rr, req)
		if rr.Code == http.StatusForbidden {
			t.Fatalf("a TLS connection was refused: %s", rr.Body.String())
		}
	})
}

// TestTransportGateExemptsOnlyTheProbes: /healthz and /readyz are genuinely
// credential-free, so refusing them would break liveness checking for no
// security gain. Nothing else is exempt.
func TestTransportGateExemptsOnlyTheProbes(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090"}, Deps{Ready: func() bool { return true }})
	h := s.routes()

	for _, path := range []string{"/healthz", "/readyz"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, withLocalAddr(httptest.NewRequest(http.MethodGet, path, nil), "203.0.113.7:9090"))
		if rr.Code == http.StatusForbidden {
			t.Fatalf("%s was refused; the probes are credential-free and stay reachable", path)
		}
	}

	// A path that merely starts with a probe name must not inherit the
	// exemption. This is the fail-closed direction of the exact-match rule.
	for _, path := range []string{"/healthz/../api/config", "/healthz/x", "/readyz/", "/healthzz"} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, withLocalAddr(httptest.NewRequest(http.MethodGet, path, nil), "203.0.113.7:9090"))
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s produced %d; only the exact probe paths are exempt", path, rr.Code)
		}
	}
}

// TestTransportGateFallsBackToTheConfiguredListener covers a request with no
// connection to inspect. The fallback is the static property of the listener
// the server was configured to bind, and it never widens the gate: a wildcard
// or public bind is refused exactly as a wildcard or public connection is.
func TestTransportGateFallsBackToTheConfiguredListener(t *testing.T) {
	cases := []struct {
		listen  string
		refused bool
	}{
		{"127.0.0.1:9090", false},
		{"localhost:9090", false},
		{"[::1]:9090", false},
		{"0.0.0.0:9090", true},
		{":9090", true},
		{"203.0.113.7:9090", true},
	}
	for _, tc := range cases {
		t.Run(tc.listen, func(t *testing.T) {
			s := newTestServer(t, config.AdminConfig{Listen: tc.listen}, Deps{})
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))
			refused := rr.Code == http.StatusForbidden
			if refused != tc.refused {
				t.Fatalf("listen %q: refused = %v, want %v (status %d)", tc.listen, refused, tc.refused, rr.Code)
			}
		})
	}
}

// TestTransportGateRefusalCarriesNoConfigurationValue is §26 rule 3's named
// test case. The refusal is returned *before authentication*, so anything it
// discloses is disclosed to an anonymous caller. An earlier draft returned the
// listen address here.
func TestTransportGateRefusalCarriesNoConfigurationValue(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Listen: "203.0.113.7:9090", Token: "secret-token"}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/status", nil), "203.0.113.7:9090"))

	body := rr.Body.String()
	for _, leak := range []string{"203.0.113.7", "9090", "secret-token"} {
		if strings.Contains(body, leak) {
			t.Fatalf("the pre-authentication refusal disclosed %q: %s", leak, body)
		}
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Details.Field != "" || env.Error.Details.Kind != "" || env.Error.Details.ID != "" {
		t.Fatalf("insecure_transport details carry more than `required`: %+v", env.Error.Details)
	}
}
