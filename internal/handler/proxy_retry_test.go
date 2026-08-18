// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// TestNoRetryAfterResponseStarted is the regression that matters most in this
// slice, because violating it is not a performance bug but a correctness one:
// once a backend has answered, the request has been processed, and sending it
// somewhere else would execute it twice.
//
// Jul retries transport errors only, so a 5xx is an answer and ends the
// sequence. That also keeps the mechanism from doubling load on a backend that
// is deliberately shedding.
func TestNoRetryAfterResponseStarted(t *testing.T) {
	var firstHits, secondHits atomic.Int64
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		firstHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("shedding"))
	}))
	defer first.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer second.Close()

	ups := map[string]config.UpstreamConfig{
		"pool": {
			Name:     "pool",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: strings.TrimPrefix(first.URL, "http://"), Weight: 1},
				{Address: strings.TrimPrefix(second.URL, "http://"), Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, ups)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the backend's 503 passed through unchanged", rec.Code)
	}
	if got := firstHits.Load(); got != 1 {
		t.Fatalf("first backend served %d requests, want 1", got)
	}
	if got := secondHits.Load(); got != 0 {
		t.Fatalf("second backend served %d requests; a delivered response was retried elsewhere", got)
	}
}

// TestNoRetryOfNonIdempotentMethod pins that a POST is attempted once even
// though its body is replayable. GetBody proves the request can be re-sent, not
// that it is safe to: a connection error does not prove the backend did not
// accept, commit and then die.
func TestNoRetryOfNonIdempotentMethod(t *testing.T) {
	ups := map[string]config.UpstreamConfig{
		"dead": {
			Name:     "dead",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: "127.0.0.1:2", Weight: 1},
				{Address: "127.0.0.1:3", Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	pool, err := upstream.NewPool(ups["dead"], "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	// dialFailure fires once per failed attempt, so it counts attempts without
	// depending on passive health tripping.
	var attempts atomic.Int64
	tr := &balancingTransport{
		pool:        pool,
		base:        http.DefaultTransport.(*http.Transport).Clone(),
		dialFailure: func(string) { attempts.Add(1) },
	}
	req := httptest.NewRequest(http.MethodPost, "http://edge/", strings.NewReader("body"))
	if _, err := tr.RoundTrip(req); err == nil {
		t.Fatal("expected the POST to fail against dead backends")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("%d attempts were made for a POST, want exactly 1", got)
	}
}

// TestNoTLSDowngradeOnRetry pins that failover may never move a request from
// TLS to plaintext. A backend whose scheme does not match the route is refused
// rather than dialled, and the refusal is terminal: retrying into the next
// plaintext backend would be the same downgrade one hop later.
func TestNoTLSDowngradeOnRetry(t *testing.T) {
	var hits atomic.Int64
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()
	addr := strings.TrimPrefix(plain.URL, "http://")

	// The pool is built with the http scheme while the route believes it is
	// https, which is the state the guard exists to refuse.
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "mixed",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: addr, Weight: 1},
			{Address: addr, Weight: 1},
			{Address: addr, Weight: 1},
		},
		MaxFails: 1000,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	tr := &balancingTransport{
		pool:       pool,
		base:       http.DefaultTransport.(*http.Transport).Clone(),
		tlsBackend: true,
	}
	resp, err := tr.RoundTrip(httptest.NewRequest(http.MethodGet, "https://edge/", nil))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an https route was served by a plaintext backend")
	}
	if !strings.Contains(err.Error(), "refusing to downgrade") {
		t.Fatalf("error = %v, want the downgrade refusal", err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("plaintext backend was dialled %d times; the guard must refuse before dialling", got)
	}
}

// TestRetryDeadlineBoundsTheWholeSequence pins that retry_deadline bounds the
// sequence rather than each attempt, so a pool of slow-to-fail backends cannot
// multiply it by the backend count.
func TestRetryDeadlineBoundsTheWholeSequence(t *testing.T) {
	// Backends that accept the connection and then never answer, so each
	// attempt blocks until the deadline rather than failing fast.
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer slow.Close()
	addr := strings.TrimPrefix(slow.URL, "http://")

	servers := make([]config.UpstreamServer, 0, 6)
	for range 6 {
		servers = append(servers, config.UpstreamServer{Address: addr, Weight: 1})
	}
	ups := map[string]config.UpstreamConfig{
		"slow": {Name: "slow", Strategy: "round_robin", Servers: servers, MaxFails: 1000},
	}
	loc := config.LocationConfig{
		ProxyPass:  "http://slow",
		Resilience: &config.LocationResilienceConfig{RetryDeadline: config.Duration(250 * time.Millisecond)},
	}
	h := newProxy(t, loc, ups)

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Fatalf("request took %s; the deadline bounded each attempt instead of the sequence", elapsed)
	}
}

// TestLocationRetryAttemptsOverridesPool pins the override rule, and that a
// zero location value inherits rather than meaning "unlimited" — the same rule
// max_connections_per_backend already follows, because one field reading its
// zero the other way would be a trap.
func TestLocationRetryAttemptsOverridesPool(t *testing.T) {
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:       "p",
		Strategy:   "round_robin",
		Servers:    []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}},
		MaxFails:   1000,
		Resilience: &config.ResilienceConfig{RetryAttempts: 7, RetryDeadline: config.Duration(9 * time.Second)},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	for _, tc := range []struct {
		name         string
		loc          config.LocationConfig
		wantAttempts int
		wantDeadline time.Duration
	}{
		{
			name:         "no location block inherits the pool",
			loc:          config.LocationConfig{},
			wantAttempts: 7,
			wantDeadline: 9 * time.Second,
		},
		{
			name:         "zero location values inherit",
			loc:          config.LocationConfig{Resilience: &config.LocationResilienceConfig{}},
			wantAttempts: 7,
			wantDeadline: 9 * time.Second,
		},
		{
			name: "set location values win",
			loc: config.LocationConfig{Resilience: &config.LocationResilienceConfig{
				RetryAttempts: 2,
				RetryDeadline: config.Duration(time.Second),
			}},
			wantAttempts: 2,
			wantDeadline: time.Second,
		},
		{
			name:         "the deprecated proxy_retries still works",
			loc:          config.LocationConfig{ProxyRetries: 4},
			wantAttempts: 4,
			wantDeadline: 9 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &balancingTransport{pool: pool, retryOverride: newLocationRetry(tc.loc)}
			rr := tr.retryRequest(true)
			if rr.MaxAttempts != tc.wantAttempts {
				t.Errorf("MaxAttempts = %d, want %d", rr.MaxAttempts, tc.wantAttempts)
			}
			if rr.Deadline != tc.wantDeadline {
				t.Errorf("Deadline = %s, want %s", rr.Deadline, tc.wantDeadline)
			}
		})
	}
}
