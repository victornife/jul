// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/router"

	"github.com/quic-go/quic-go/http3"
)

// ADR 0018 §12 is HTTP *semantic* parity: a predicate is a property of the HTTP
// message, so it must produce the same routing decision on every transport that
// carries one. This drives real requests through a real router over HTTP/1.1,
// HTTP/2 and HTTP/3 and asserts they select the same location.
//
// L4 `[[stream]]` routes are deliberately out of scope: there is no HTTP
// message to match on.

// predicateParityConfig routes on a method, a repeated header and a query
// parameter, with an unconstrained route below it so a predicate failure is
// observable as a fallthrough rather than as a 404.
func predicateParityConfig(t *testing.T) *config.Config {
	t.Helper()
	value := func(s string) *string { return &s }
	return &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{
			{
				Match: config.MatchConfig{
					Type:    "prefix",
					Path:    "/api/",
					Methods: []string{"POST"},
					Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: value("public")}},
					Query:   []config.QueryMatch{{Name: "version", Op: "exact", Value: value("v2")}},
				},
				Return: 201,
			},
			{Match: config.MatchConfig{Type: "prefix", Path: "/api/"}, Return: 202},
			{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 204},
		},
	}}}
}

// predicateParityHandler builds the router and reports the selected location
// through a response header, so the assertion does not depend on a body.
func predicateParityHandler(t *testing.T) http.Handler {
	t.Helper()
	cfg := predicateParityConfig(t)
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rt, err := router.New(cfg, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	inner := rt.For("127.0.0.1:0")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Proto", r.Proto)
		inner.ServeHTTP(w, r)
	})
}

func TestRoutePredicateParityAcrossProtocols(t *testing.T) {
	handler := predicateParityHandler(t)

	dir := t.TempDir()
	certPath, keyPath := writeSelfSigned(t, dir, "predicate-parity", "localhost")
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("load certificate: %v", err)
	}
	roots := certificatePool(t, certPath)

	tcp := httptest.NewUnstartedServer(handler)
	tcp.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	tcp.EnableHTTP2 = true
	tcp.StartTLS()
	defer tcp.Close()

	h3, err := newStagedHTTP3WithTLS("127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
	}, handler, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("newStagedHTTP3WithTLS: %v", err)
	}
	if err := h3.Activate(); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	defer func() { _ = h3.Close(context.Background()) }()

	h1Transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}
	h2Transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost", NextProtos: []string{"h2"}}, ForceAttemptHTTP2: true}
	h3Transport := &http3.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "localhost"}}
	defer func() { _ = h3Transport.Close() }()

	protocols := []struct {
		name      string
		url       string
		transport http.RoundTripper
		wantMajor int
	}{
		{"http/1.1", tcp.URL, h1Transport, 1},
		{"http/2", tcp.URL, h2Transport, 2},
		{"http/3", "https://" + h3.(*h3Conn).ln.Addr().String(), h3Transport, 3},
	}

	// Each case is a routing decision, not a status code: 201 is the
	// predicate-bearing route, 202 the unconstrained route below it, and 204 the
	// catch-all.
	cases := []struct {
		name       string
		method     string
		target     string
		headers    [][2]string
		wantStatus int
	}{
		{
			name:       "every predicate satisfied",
			method:     http.MethodPost,
			target:     "/api/users?version=v2",
			headers:    [][2]string{{"X-Tenant", "public"}},
			wantStatus: 201,
		},
		{
			name:       "method mismatch falls through",
			method:     http.MethodGet,
			target:     "/api/users?version=v2",
			headers:    [][2]string{{"X-Tenant", "public"}},
			wantStatus: 202,
		},
		{
			name:       "header mismatch falls through",
			method:     http.MethodPost,
			target:     "/api/users?version=v2",
			headers:    [][2]string{{"X-Tenant", "private"}},
			wantStatus: 202,
		},
		{
			name:       "query mismatch falls through",
			method:     http.MethodPost,
			target:     "/api/users?version=v1",
			headers:    [][2]string{{"X-Tenant", "public"}},
			wantStatus: 202,
		},
		{
			// Header names are canonicalized, so the wire spelling is irrelevant
			// — including on HTTP/2 and HTTP/3, which lowercase every name.
			name:       "header name case is irrelevant on every protocol",
			method:     http.MethodPost,
			target:     "/api/users?version=v2",
			headers:    [][2]string{{"x-TENANT", "public"}},
			wantStatus: 201,
		},
		{
			// Any one field line matching is a match, on every protocol.
			name:       "a repeated field line matches",
			method:     http.MethodPost,
			target:     "/api/users?version=v2",
			headers:    [][2]string{{"X-Tenant", "private"}, {"X-Tenant", "public"}},
			wantStatus: 201,
		},
		{
			// A malformed escape makes only that pair absent; the request is
			// still routed rather than turned into a 400.
			name:       "a malformed escape is not a 400",
			method:     http.MethodPost,
			target:     "/api/users?bad=%zz&version=v2",
			headers:    [][2]string{{"X-Tenant", "public"}},
			wantStatus: 201,
		},
		{
			name:       "a path outside the predicate route reaches the catch-all",
			method:     http.MethodGet,
			target:     "/elsewhere",
			wantStatus: 204,
		},
	}

	for _, proto := range protocols {
		for _, tc := range cases {
			t.Run(proto.name+"/"+tc.name, func(t *testing.T) {
				req, err := http.NewRequest(tc.method, proto.url+tc.target, nil)
				if err != nil {
					t.Fatalf("NewRequest: %v", err)
				}
				for _, h := range tc.headers {
					req.Header.Add(h[0], h[1])
				}
				client := &http.Client{Transport: proto.transport, Timeout: 5 * time.Second}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatalf("%s %s: %v", tc.method, tc.target, err)
				}
				defer func() { _ = resp.Body.Close() }()

				if resp.ProtoMajor != proto.wantMajor {
					t.Fatalf("protocol = %q, want HTTP/%d", resp.Proto, proto.wantMajor)
				}
				if resp.StatusCode != tc.wantStatus {
					t.Errorf("%s %s selected the route returning %d, want %d (server saw %s)",
						tc.method, tc.target, resp.StatusCode, tc.wantStatus, resp.Header.Get("X-Test-Proto"))
				}
			})
		}
	}
}

// TestNoAllowHeaderOnAPredicateOnlyMiss pins ADR 0018 §7 over a real
// connection: when every candidate is rejected by a predicate the answer is the
// ordinary 404, with no 405 and no Allow header anywhere.
func TestNoAllowHeaderOnAPredicateOnlyMiss(t *testing.T) {
	value := "public"
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{{
			Match: config.MatchConfig{
				Type:    "prefix",
				Path:    "/api/",
				Methods: []string{"POST"},
				Headers: []config.HeaderMatch{{Name: "X-Tenant", Op: "exact", Value: &value}},
			},
			Return: 201,
		}},
	}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rt, err := router.New(cfg, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	srv := httptest.NewServer(rt.For("127.0.0.1:0"))
	defer srv.Close()

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodOptions} {
		req, err := http.NewRequest(method, srv.URL+"/api/users", nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", method, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body %q)", method, resp.StatusCode, string(body))
		}
		if allow := resp.Header.Get("Allow"); allow != "" {
			t.Errorf("%s: Allow = %q, want none — a gateway must not assert what an upstream it never consulted implements", method, allow)
		}
	}
}

// TestHEADIsServedByAGETRoute pins the RFC 9110 §9.3.2 compatibility rule over a
// real connection, which is where a route that answers GET and 404s HEAD would
// otherwise be discovered.
func TestHEADIsServedByAGETRoute(t *testing.T) {
	cfg := &config.Config{Servers: []config.ServerConfig{{
		Listen: "127.0.0.1:0",
		Locations: []config.LocationConfig{
			{Match: config.MatchConfig{Type: "prefix", Path: "/get-only", Methods: []string{"GET"}}, Return: 200},
			{Match: config.MatchConfig{Type: "prefix", Path: "/head-only", Methods: []string{"HEAD"}}, Return: 200},
		},
	}}}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	rt, err := router.New(cfg, nil, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("router.New: %v", err)
	}
	srv := httptest.NewServer(rt.For("127.0.0.1:0"))
	defer srv.Close()

	cases := []struct {
		method, path string
		wantStatus   int
	}{
		{http.MethodHead, "/get-only", http.StatusOK},       // GET ⊇ HEAD
		{http.MethodGet, "/get-only", http.StatusOK},        //
		{http.MethodHead, "/head-only", http.StatusOK},      // HEAD alone still matches HEAD
		{http.MethodGet, "/head-only", http.StatusNotFound}, // but not GET
	}
	for _, tc := range cases {
		req, err := http.NewRequest(tc.method, srv.URL+tc.path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != tc.wantStatus {
			t.Errorf("%s %s: status = %d, want %d", tc.method, tc.path, resp.StatusCode, tc.wantStatus)
		}
	}

}
