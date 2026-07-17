// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

func newProxy(t *testing.T, loc config.LocationConfig, ups map[string]config.UpstreamConfig) http.Handler {
	t.Helper()
	h, err := NewProxy(config.ServerConfig{}, loc, ups, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	return h
}

func TestProxyForwardsRequestAndHeaders(t *testing.T) {
	var gotHost, gotXFF, gotXFP, gotBody string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHost = r.Host
		gotXFF = r.Header.Get("X-Forwarded-For")
		gotXFP = r.Header.Get("X-Forwarded-Proto")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-Backend", "yes")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "hello from backend")
	}))
	defer backend.Close()

	loc := config.LocationConfig{
		ProxyPass: backend.URL,
		Headers:   map[string]string{"Host": "$host", "X-Real-IP": "$remote_addr"},
	}
	h := newProxy(t, loc, nil)

	req := httptest.NewRequest(http.MethodPost, "http://edge.example/api/x", strings.NewReader("payload"))
	req.Host = "edge.example"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("X-Backend") != "yes" {
		t.Error("response headers not forwarded back")
	}
	if !strings.Contains(rec.Body.String(), "hello from backend") {
		t.Errorf("body = %q", rec.Body.String())
	}
	if gotHost != "edge.example" {
		t.Errorf("Host override = %q, want edge.example", gotHost)
	}
	if gotXFF == "" {
		t.Error("X-Forwarded-For not set")
	}
	if gotXFP != "http" {
		t.Errorf("X-Forwarded-Proto = %q, want http", gotXFP)
	}
	if gotBody != "payload" {
		t.Errorf("forwarded body = %q, want payload", gotBody)
	}
}

func TestProxyUpstreamResolution(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "up")
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	ups := map[string]config.UpstreamConfig{
		"backend": {Name: "backend", Servers: []config.UpstreamServer{{Address: addr, Weight: 1}}},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://backend"}, ups)

	req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "up" {
		t.Fatalf("upstream proxy = %d %q", rec.Code, rec.Body.String())
	}
}

func TestProxyBadGateway(t *testing.T) {
	// Port 1 is essentially never listening -> dial error -> 502.
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://127.0.0.1:1"}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestProxyGatewayTimeout(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	loc := config.LocationConfig{
		ProxyPass:        backend.URL,
		ProxyReadTimeout: config.Duration(30 * time.Millisecond),
	}
	h := newProxy(t, loc, nil)

	req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
}

func TestResolveTargetInvalid(t *testing.T) {
	if _, _, _, err := resolvePool(config.LocationConfig{ProxyPass: "not-a-url"}, nil, nil); err == nil {
		t.Fatal("expected error for invalid proxy_pass")
	}
}

func TestProxyZeroBackendDiscoveryNoPanic(t *testing.T) {
	// A discovery-backed upstream resolves its backends live, so at build time
	// the pool can legitimately have zero backends. Building the proxy must not
	// panic (regression: NewProxy previously indexed pool.Backends()[0]).
	loc := config.LocationConfig{ProxyPass: "http://disco"}
	ups := map[string]config.UpstreamConfig{
		"disco": {
			Name:      "disco",
			Discovery: &config.DiscoveryConfig{Type: "dns", Target: "svc.internal:80"},
		},
	}
	h := newProxy(t, loc, ups)

	// With no backends resolved yet, a request degrades to a gateway error
	// instead of panicking.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if rec.Code < 500 {
		t.Fatalf("status = %d, want a 5xx gateway error for an empty pool", rec.Code)
	}
}

func TestProxyFailover(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live")
	}))
	defer live.Close()
	liveAddr := strings.TrimPrefix(live.URL, "http://")

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:     "pool",
			Strategy: "round_robin",
			// First backend is dead (port 1), second is live. An idempotent GET
			// must fail over to the live backend.
			Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: liveAddr, Weight: 1}},
			MaxFails: 1,
		},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	// Try several requests; at least one starts on the dead backend and must
	// still succeed via failover.
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "live" {
			t.Fatalf("request %d: failover = %d %q", i, rec.Code, rec.Body.String())
		}
	}
}

func TestProxyFailoverLeastConn(t *testing.T) {
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live")
	}))
	defer live.Close()
	liveAddr := strings.TrimPrefix(live.URL, "http://")

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:     "pool",
			Strategy: "least_conn",
			Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: liveAddr, Weight: 1}},
			MaxFails: 1,
		},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	for i := 0; i < 4; i++ {
		req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || rec.Body.String() != "live" {
			t.Fatalf("request %d: failover = %d %q", i, rec.Code, rec.Body.String())
		}
	}
}

func TestProxyRetryRewindSurfacesUpstreamError(t *testing.T) {
	// Two dead backends so the first attempt fails (setting lastErr) and the
	// retry then attempts to rewind the request body. GetBody is rigged to fail,
	// exercising the path that must surface the upstream failure rather than the
	// body-rewind error.
	up := config.UpstreamConfig{
		Name:     "dead",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: "127.0.0.1:1", Weight: 1}},
		MaxFails: 5,
	}
	pool, err := upstream.NewPool(up, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	tr := &balancingTransport{pool: pool, base: newProxyTransport(config.LocationConfig{})}

	rewindErr := errors.New("rewind boom")
	req := httptest.NewRequest(http.MethodGet, "http://edge/", strings.NewReader("body"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, rewindErr }

	_, err = tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected an error from a request to two dead backends")
	}
	if errors.Is(err, rewindErr) {
		t.Fatalf("surfaced the body-rewind error instead of the upstream failure: %v", err)
	}
}

func TestExpandProxyVarSSLClient(t *testing.T) {
	id := &middleware.ClientIdentity{
		Verified:    true,
		SubjectDN:   "CN=alice,O=Jul",
		IssuerDN:    "CN=Issuer",
		CN:          "alice",
		Serial:      "1234",
		Fingerprint: "abcd",
		SANs:        "alice.example.com",
	}
	req := httptest.NewRequest(http.MethodGet, "https://edge/", nil)
	req = req.WithContext(middleware.WithClientIdentity(req.Context(), id))

	if got := expandProxyVar("$ssl_client_cn", req); got != "alice" {
		t.Errorf("$ssl_client_cn = %q, want alice", got)
	}
	if got := expandProxyVar("$ssl_client_s_dn", req); got != "CN=alice,O=Jul" {
		t.Errorf("$ssl_client_s_dn = %q", got)
	}
	if got := expandProxyVar("$ssl_client_serial", req); got != "1234" {
		t.Errorf("$ssl_client_serial = %q, want 1234", got)
	}
	if got := expandProxyVar("$ssl_client_verify", req); got != "SUCCESS" {
		t.Errorf("$ssl_client_verify = %q, want SUCCESS", got)
	}
	combined := expandProxyVar("$ssl_client_cn/$ssl_client_san", req)
	if combined != "alice/alice.example.com" {
		t.Errorf("combined = %q, want alice/alice.example.com", combined)
	}
}

func TestExpandProxyVarSSLClientAbsent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://edge/", nil)
	if got := expandProxyVar("$ssl_client_verify", req); got != "NONE" {
		t.Errorf("$ssl_client_verify without a cert = %q, want NONE", got)
	}
	if got := expandProxyVar("$ssl_client_cn", req); got != "" {
		t.Errorf("$ssl_client_cn without a cert = %q, want empty", got)
	}
}
