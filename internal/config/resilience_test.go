// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func resilienceConfig(t *testing.T, r *ResilienceConfig) *Config {
	t.Helper()
	cfg := validKnownValueConfig()
	cfg.Upstreams = []UpstreamConfig{{
		Name:       "api",
		Strategy:   "round_robin",
		Servers:    []UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
		Resilience: r,
	}}
	return cfg
}

func TestValidateAcceptsResilience(t *testing.T) {
	cases := []struct {
		name   string
		policy *ResilienceConfig
	}{
		{name: "absent", policy: nil},
		{name: "all zero reproduces current behaviour", policy: &ResilienceConfig{}},
		{name: "admission limit alone", policy: &ResilienceConfig{MaxActiveRequests: 1000}},
		{name: "per-backend filter alone", policy: &ResilienceConfig{MaxActivePerBackend: 50}},
		{
			name: "bounded queue with a timeout",
			policy: &ResilienceConfig{
				MaxActiveRequests:  1000,
				MaxPendingRequests: 100,
				PendingTimeout:     Duration(2 * time.Second),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(resilienceConfig(t, tc.policy)); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestValidateRejectsResilience(t *testing.T) {
	cases := []struct {
		name   string
		policy *ResilienceConfig
		want   string
	}{
		{
			// The failure this control exists to prevent is an unbounded queue,
			// so the one combination that produces a queue nothing ever drains
			// must not be representable.
			name:   "queue without an admission limit",
			policy: &ResilienceConfig{MaxPendingRequests: 10},
			want:   "max_pending_requests requires max_active_requests",
		},
		{
			name:   "negative admission limit",
			policy: &ResilienceConfig{MaxActiveRequests: -1},
			want:   "max_active_requests must be between",
		},
		{
			name:   "admission limit above the ceiling",
			policy: &ResilienceConfig{MaxActiveRequests: 10_000_001},
			want:   "max_active_requests must be between",
		},
		{
			name:   "queue above the ceiling",
			policy: &ResilienceConfig{MaxActiveRequests: 10, MaxPendingRequests: 100_001},
			want:   "max_pending_requests must be between",
		},
		{
			name:   "pending timeout above the ceiling",
			policy: &ResilienceConfig{MaxActiveRequests: 10, MaxPendingRequests: 1, PendingTimeout: Duration(61 * time.Second)},
			want:   "pending_timeout must be between",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(resilienceConfig(t, tc.policy))
			if err == nil {
				t.Fatalf("Validate accepted %+v", tc.policy)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

// TestValidateRejectsPendingTimeoutBeyondShutdownGrace pins the coupling between
// the queue and generation retirement. The retirement grace is the shutdown
// timeout; a request allowed to wait longer than that outlives the transport it
// was queued for.
func TestValidateRejectsPendingTimeoutBeyondShutdownGrace(t *testing.T) {
	cfg := resilienceConfig(t, &ResilienceConfig{
		MaxActiveRequests:  10,
		MaxPendingRequests: 5,
		PendingTimeout:     Duration(30 * time.Second),
	})
	cfg.Global.ShutdownTimeout = Duration(5 * time.Second)

	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate accepted a pending_timeout longer than the retirement grace")
	}
	if !strings.Contains(err.Error(), "shutdown_timeout") {
		t.Fatalf("error = %v, want it to name global.shutdown_timeout", err)
	}

	cfg.Global.ShutdownTimeout = Duration(60 * time.Second)
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate with a sufficient grace: %v", err)
	}
}

// TestParseRejectsLocationResilience pins the scope rule: a control is
// location-overridable if and only if it owns no shared state, and the
// admission counters are owned by the pool. Strict decoding rejects the block
// outright, so the mistake can never be a silent ignore.
func TestParseRejectsLocationResilience(t *testing.T) {
	const doc = `
[global]
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
path = "/"
proxy_pass = "http://api"
[servers.locations.resilience]
max_active_requests = 10

[[upstreams]]
name = "api"
servers = [{ address = "127.0.0.1:3000", weight = 1 }]
`
	if _, err := Parse([]byte(doc)); err == nil {
		t.Fatal("parser accepted a location-scoped resilience block")
	}
}

func TestLintResilienceSizingWarnings(t *testing.T) {
	t.Run("per-backend capacity below the pool limit", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Upstreams = []UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers: []UpstreamServer{
				{Address: "127.0.0.1:3000", Weight: 1},
				{Address: "127.0.0.1:3001", Weight: 1},
			},
			Resilience: &ResilienceConfig{
				MaxActiveRequests:   1000,
				MaxActivePerBackend: 100,
			},
		}}
		// 2 x 100 = 200 < 1000, so the pool limit can never be reached and
		// requests are rejected while the queue sits empty.
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "max_active_per_backend", "unreachable")
	})

	t.Run("queue without a timeout", func(t *testing.T) {
		cfg := resilienceConfig(t, &ResilienceConfig{
			MaxActiveRequests:  100,
			MaxPendingRequests: 50,
		})
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "pending_timeout", "bounded only by")
	})

	t.Run("coherent sizing is silent", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Upstreams = []UpstreamConfig{{
			Name:     "api",
			Strategy: "round_robin",
			Servers: []UpstreamServer{
				{Address: "127.0.0.1:3000", Weight: 1},
				{Address: "127.0.0.1:3001", Weight: 1},
			},
			Resilience: &ResilienceConfig{
				MaxActiveRequests:   100,
				MaxActivePerBackend: 60,
				MaxPendingRequests:  20,
				PendingTimeout:      Duration(time.Second),
			},
		}}
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Field, "resilience") {
				t.Fatalf("unexpected diagnostic on a coherent policy: %+v", d)
			}
		}
	})
}

func requireDiagnostic(t *testing.T, diags []Diagnostic, sev Severity, field, message string) {
	t.Helper()
	for _, d := range diags {
		if d.Severity == sev && strings.Contains(d.Field, field) && strings.Contains(d.Message, message) {
			if d.Hint == "" {
				t.Fatalf("diagnostic %q has no hint", d.Field)
			}
			return
		}
	}
	t.Fatalf("no %v diagnostic on %q mentioning %q; got %+v", sev, field, message, diags)
}
