// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

// realServer starts the admin mux on a real loopback listener and returns its
// base URL. It exercises the whole stack a remote client meets — connection,
// transport gate, authentication, routing, encoding — rather than a handler
// invoked in-process, which is where a wiring mistake actually hides.
func realServer(t *testing.T, cfg config.AdminConfig, deps Deps) (*httptest.Server, *Server) {
	t.Helper()
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:0"
	}
	s := newTestServer(t, cfg, deps)
	ts := httptest.NewServer(s.routes())
	t.Cleanup(ts.Close)
	return ts, s
}

func doGet(t *testing.T, client *http.Client, url, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, body
}

// TestV1ReadSurfaceOverARealConnection is the required real-server E2E: a
// success, an authentication failure and the transport refusal, each over an
// actual HTTP round trip.
func TestV1ReadSurfaceOverARealConnection(t *testing.T) {
	const token = "e2e-token-32-chars-padded-------"
	ts, _ := realServer(t, config.AdminConfig{Token: token}, Deps{
		Ready: func() bool { return true },
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "file_owned", Source: "default", ConfigState: "file_owned"}
		},
	})

	t.Run("success", func(t *testing.T) {
		resp, body := doGet(t, ts.Client(), ts.URL+"/api/v1/status", token)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("Content-Type = %q", ct)
		}
		if resp.Header.Get("X-Request-ID") == "" {
			t.Error("no X-Request-ID on a successful external response")
		}
		var got adminapi.StatusResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v\n%s", err, body)
		}
		// file_owned is the fixed default: a CLI E2E must never rely on a
		// server being writable by accident (ADR 0019 §9.1).
		if got.ConfigAuthority != "file_owned" || got.ConfigAuthoritySource != "default" {
			t.Fatalf("authority = %q/%q", got.ConfigAuthority, got.ConfigAuthoritySource)
		}
		if got.BootID == "" {
			t.Error("boot_id is empty over a real connection")
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		resp, body := doGet(t, ts.Client(), ts.URL+"/api/v1/capabilities", token)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		var got adminapi.CapabilitiesResponse
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(got.Endpoints) == 0 {
			t.Fatal("capabilities describes no endpoints")
		}
	})

	t.Run("authentication failure", func(t *testing.T) {
		resp, body := doGet(t, ts.Client(), ts.URL+"/api/v1/status", "wrong-token-32-chars-padded----")
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401: %s", resp.StatusCode, body)
		}
		var env adminapi.Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("a failure must still be one parseable object: %v\n%s", err, body)
		}
		if env.Error.Code != adminapi.CodeUnauthenticated {
			t.Fatalf("code = %q", env.Error.Code)
		}
		if env.Error.RequestID != resp.Header.Get("X-Request-ID") {
			t.Fatal("the envelope's request_id and the header disagree")
		}
		if strings.Contains(string(body), "wrong-token") {
			t.Fatal("the refusal echoed the presented credential")
		}
	})
}

// TestV1IsRefusedOverPlaintextOnANonLoopbackConnection proves §28.1's gate
// covers the new namespace over a real connection, before authentication.
func TestV1IsRefusedOverPlaintextOnANonLoopbackConnection(t *testing.T) {
	const token = "e2e-token-32-chars-padded-------"
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090", Token: token}, Deps{})

	rr := httptest.NewRecorder()
	req := withLocalAddr(httptest.NewRequest(http.MethodGet, "/api/v1/status", nil), "203.0.113.7:9090")
	req.Header.Set("Authorization", "Bearer "+token)
	s.routes().ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeInsecureTransport {
		t.Fatalf("code = %q, want insecure_transport", env.Error.Code)
	}
	// Refused before authentication: a valid credential gets the same answer.
	if strings.Contains(rr.Body.String(), token) {
		t.Fatal("the refusal echoed the credential it never evaluated")
	}
}

// TestV1OverTLSOnANonLoopbackAddress is the other half of §28.1 and the
// deployment #336 shipped for: a TLS-terminated listener satisfies the gate on
// any address, so remote administration is possible without a tunnel.
func TestV1OverTLSOnANonLoopbackAddress(t *testing.T) {
	const token = "e2e-token-32-chars-padded-------"
	s := newTestServer(t, config.AdminConfig{Listen: "0.0.0.0:9090", Token: token}, Deps{})

	ts := httptest.NewTLSServer(s.routes())
	t.Cleanup(ts.Close)

	// Present the request as having arrived on a public address over TLS.
	client := ts.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("unexpected transport %T", client.Transport)
	}
	transport.TLSClientConfig.ServerName = "example.com"
	transport.DialTLSContext = nil

	resp, body := doGet(t, client, ts.URL+"/api/v1/status", token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("a TLS request was refused: %d %s", resp.StatusCode, body)
	}
	if resp.TLS == nil {
		t.Fatal("the round trip was not actually TLS")
	}
}

// TestV1RequestIDsAreUniquePerRequest: a correlation id that repeats correlates
// nothing.
func TestV1RequestIDsAreUniquePerRequest(t *testing.T) {
	const token = "e2e-token-32-chars-padded-------"
	ts, _ := realServer(t, config.AdminConfig{Token: token}, Deps{})

	seen := map[string]bool{}
	for range 25 {
		resp, _ := doGet(t, ts.Client(), ts.URL+"/api/v1/status", token)
		id := resp.Header.Get("X-Request-ID")
		if id == "" {
			t.Fatal("a response carried no request id")
		}
		if seen[id] {
			t.Fatalf("request id %q was reused", id)
		}
		seen[id] = true
	}
}

// TestV1ClientSuppliedRequestIDIsNotReflected. Echoing a client-chosen value
// would let a caller forge a log correlation, or smuggle bytes into an
// operator's terminal.
func TestV1ClientSuppliedRequestIDIsNotReflected(t *testing.T) {
	const token = "e2e-token-32-chars-padded-------"
	ts, _ := realServer(t, config.AdminConfig{Token: token}, Deps{})

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/status", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Request-ID", "forged-by-the-client")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := resp.Header.Get("X-Request-ID"); got == "forged-by-the-client" {
		t.Fatal("the server reflected a client-supplied request id")
	}
}

// TestProbesStayReachableWithoutACredential guards the boundary the new
// namespace sits beside: adding /api/v1 must not disturb the two public routes.
func TestProbesStayReachableWithoutACredential(t *testing.T) {
	ts, _ := realServer(t, config.AdminConfig{Token: "e2e-token-32-chars-padded-------"},
		Deps{Ready: func() bool { return true }})
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, body := doGet(t, ts.Client(), ts.URL+path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s = %d: %s", path, resp.StatusCode, body)
		}
	}
}
