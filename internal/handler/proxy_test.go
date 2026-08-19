// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

func newProxy(t *testing.T, loc config.LocationConfig, ups map[string]config.UpstreamConfig) http.Handler {
	t.Helper()
	h, err := NewProxy(context.Background(), config.ServerConfig{}, loc, ups, nil, nil, nil)
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
	if _, _, _, err := resolvePool(context.Background(), config.LocationConfig{ProxyPass: "not-a-url"}, nil, nil); err == nil {
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
	tr := &balancingTransport{pool: pool, base: newProxyTransport(config.LocationConfig{}, nil, 0, nil)}

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

func TestProxyRetryBoundedByProxyRetries(t *testing.T) {
	// Three backends: one live, two dead. With proxy_retries = 1 the retry
	// loop must stop after the first retry, so if the first pick is dead and
	// the second pick is also dead, the request fails without trying the live
	// backend. We verify the cap is respected by checking that only two
	// distinct address attempts occur.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live")
	}))
	defer live.Close()
	liveAddr := strings.TrimPrefix(live.URL, "http://")

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:     "pool",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: "127.0.0.1:2", Weight: 1},
				{Address: liveAddr, Weight: 1},
			},
			MaxFails: 1,
		},
	}
	loc := config.LocationConfig{ProxyPass: "http://pool", ProxyRetries: 1}
	h := newProxy(t, loc, ups)

	// With round-robin starting at backend 0, the first request tries 127.0.0.1:1
	// (fails), retries to 127.0.0.1:2 (fails), then stops because proxy_retries=1.
	req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

// TestProxyDialFailureLogIsThrottled pins that a broken backend plus ordinary
// request volume cannot flood the log — no attacker required, matching the
// stream proxy's TestDialFailureLogIsThrottled (issue #275). The counter must
// still record every failure so the throttle is not also a blind spot.
func TestProxyDialFailureLogIsThrottled(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	var failures atomic.Int64
	ups := map[string]config.UpstreamConfig{
		"flaky": {
			Name:        "flaky",
			Strategy:    "round_robin",
			MaxFails:    1000,
			FailTimeout: config.Duration(time.Minute),
			Servers:     []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		},
	}
	loc2 := config.LocationConfig{ProxyPass: "http://flaky"}
	h2, err := NewProxy(context.Background(), config.ServerConfig{}, loc2, ups, nil, log, func(string) { failures.Add(1) })
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}

	const reqs = 50
	for range reqs {
		req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502", rec.Code)
		}
	}

	logs := buf.String()
	if got := strings.Count(logs, "dial failed") + strings.Count(logs, "upstream error"); got > 1 {
		t.Errorf("%d requests against a broken backend produced %d log lines, want at most 1", reqs, got)
	}
	if got := failures.Load(); got != reqs {
		t.Errorf("dial-failure counter = %d, want %d (must not undercount while the log is throttled)", got, reqs)
	}
}

// TestProxyDialFailureLogsTransitionUnthrottled pins that a backend's cooldown
// trip is logged unconditionally, since it is rare (bounded by
// max_fails/fail_timeout, not by request volume) and is the signal an operator
// needs even while the per-failure heartbeat is quiet.
func TestProxyDialFailureLogsTransitionUnthrottled(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))

	ups := map[string]config.UpstreamConfig{
		"flaky": {
			Name:        "flaky",
			Strategy:    "round_robin",
			MaxFails:    1,
			FailTimeout: config.Duration(10 * time.Second),
			Servers:     []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		},
	}
	loc2 := config.LocationConfig{ProxyPass: "http://flaky"}
	h2, err := NewProxy(context.Background(), config.ServerConfig{}, loc2, ups, nil, log, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}

	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		rec := httptest.NewRecorder()
		h2.ServeHTTP(rec, req)
		// The first request hits the real dial failure (502); once that trips
		// the cooldown, subsequent requests get ErrNoAvailableBackend (503).
		if rec.Code != http.StatusBadGateway && rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 502 or 503", rec.Code)
		}
	}

	logs := buf.String()
	if got := strings.Count(logs, "backend marked down"); got != 1 {
		t.Errorf("cooldown trip produced %d unthrottled lines, want 1", got)
	}
	if got := strings.Count(logs, "dial failed"); got != 0 {
		t.Errorf("already-down backend produced %d heartbeat lines, want 0 (should be pure repeats of the trip line)", got)
	}
}

func TestProxyRetryStableIdentityAcrossUpdate(t *testing.T) {
	// A backend removed and immediately re-added with a new *Backend pointer
	// must still be excluded from retries because its stable (scheme, address)
	// identity is unchanged.
	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "live")
	}))
	defer live.Close()
	liveAddr := strings.TrimPrefix(live.URL, "http://")

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:     "pool",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: liveAddr, Weight: 1}},
			MaxFails: 1,
		},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	// Prime the pool with one pick so the weighted round-robin advances.
	req1 := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK || rec1.Body.String() != "live" {
		t.Fatalf("first request = %d %q", rec1.Code, rec1.Body.String())
	}

	// Simulate discovery churn: replace the backend with a fresh pointer to the
	// same address. The stable identity must still be excluded after one failed
	// attempt within the same request.
	pool, _, _, _ := resolvePool(context.Background(), config.LocationConfig{ProxyPass: "http://pool"}, ups, nil)
	pool.UpdateBackends([]config.UpstreamServer{{Address: liveAddr, Weight: 1}})

	req2 := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "live" {
		t.Fatalf("second request after churn = %d %q", rec2.Code, rec2.Body.String())
	}
}

func TestExpandProxyVarSSLClient(t *testing.T) {
	id := &middleware.PeerCertIdentity{
		Verified:    true,
		SubjectDN:   "CN=alice,O=Jul",
		IssuerDN:    "CN=Issuer",
		CN:          "alice",
		Serial:      "1234",
		Fingerprint: "abcd",
		SANs:        "alice.example.com",
	}
	req := httptest.NewRequest(http.MethodGet, "https://edge/", nil)
	req = req.WithContext(middleware.WithPeerCertIdentity(req.Context(), id))

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

// TestClientCancellationRecords499 pins the intentional compatibility change.
//
// proxyErrorStatus used to map context.Canceled to 504 alongside
// DeadlineExceeded. The client has already disconnected by then, so nothing is
// transmitted either way and this is purely the *recorded* status — but
// recording 504 inflated "gateway timeout" with requests where nothing timed
// out, corrupting the dashboards the taxonomy exists to make trustworthy.
//
// TestProxyGatewayTimeout above is the control: a real upstream timeout must
// still be 504, or this test would pass for the wrong reason.
func TestClientCancellationRecords499(t *testing.T) {
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	defer close(release)

	h := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://edge/", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, req)
	}()
	cancel()
	<-done

	if rec.Code != 499 {
		t.Fatalf("status = %d, want 499: a client that disconnected did not time out", rec.Code)
	}
}
