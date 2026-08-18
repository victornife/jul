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

func TestValidateMaxConnectionsPerBackend(t *testing.T) {
	t.Run("accepted at pool level", func(t *testing.T) {
		if err := Validate(resilienceConfig(t, &ResilienceConfig{MaxConnectionsPerBackend: 256})); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("rejected above the ceiling", func(t *testing.T) {
		err := Validate(resilienceConfig(t, &ResilienceConfig{MaxConnectionsPerBackend: 100_001}))
		if err == nil || !strings.Contains(err.Error(), "max_connections_per_backend must be between") {
			t.Fatalf("err = %v, want a range error", err)
		}
	})
	t.Run("accepted at location level", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].Resilience = &LocationResilienceConfig{MaxConnectionsPerBackend: 32}
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("rejected above the ceiling at location level", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].Resilience = &LocationResilienceConfig{MaxConnectionsPerBackend: -1}
		if err := Validate(cfg); err == nil {
			t.Fatal("Validate accepted a negative location-level bound")
		}
	})
}

// TestParseRejectsStatefulControlsInLocation pins the scope rule at the type
// level: the location block is a different, smaller surface, so a stateful key
// written there is rejected by strict decoding rather than by a rule that could
// drift from the scope decision it implements.
func TestParseRejectsStatefulControlsInLocation(t *testing.T) {
	for _, key := range []string{"max_active_requests = 10", "max_pending_requests = 5", `pending_timeout = "1s"`, "max_active_per_backend = 2"} {
		t.Run(key, func(t *testing.T) {
			doc := `
[global]
[[servers]]
listen = "127.0.0.1:8080"
[[servers.locations]]
path = "/"
proxy_pass = "http://api"
[servers.locations.resilience]
` + key + `

[[upstreams]]
name = "api"
servers = [{ address = "127.0.0.1:3000", weight = 1 }]
`
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatalf("parser accepted a stateful control in a location: %s", key)
			}
		})
	}
}

// TestLintWarnsConnectionBoundOnMultiplexedRoute pins that a socket bound set
// where one HTTP/2 connection carries every stream is reported. Silence would
// leave an operator believing a limit is in force when it bounds nothing.
func TestLintWarnsConnectionBoundOnMultiplexedRoute(t *testing.T) {
	t.Run("pool used by a native gRPC route", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].ProxyPass = "http://api"
		cfg.Servers[0].Locations[0].GRPC = true
		cfg.Upstreams = []UpstreamConfig{{
			Name:       "api",
			Strategy:   "round_robin",
			Servers:    []UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
			Resilience: &ResilienceConfig{MaxConnectionsPerBackend: 64},
		}}
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "max_connections_per_backend", "does not limit concurrency")
	})

	t.Run("location-level on a transcoding route", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].ProxyPass = ""
		cfg.Servers[0].Locations[0].GRPCTranscode = &GRPCTranscodeConfig{Target: "api"}
		cfg.Servers[0].Locations[0].Resilience = &LocationResilienceConfig{MaxConnectionsPerBackend: 16}
		requireDiagnostic(t, Lint(cfg), SeverityWarning, "max_connections_per_backend", "does not limit its concurrency")
	})

	t.Run("plain HTTP route is silent", func(t *testing.T) {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].Resilience = &LocationResilienceConfig{MaxConnectionsPerBackend: 16}
		for _, d := range Lint(cfg) {
			if strings.Contains(d.Field, "max_connections_per_backend") {
				t.Fatalf("unexpected diagnostic on an HTTP/1.1 route: %+v", d)
			}
		}
	})
}

// TestValidateCGIPass pins the latent bug this slice fixes: a bare name that
// matches no upstream used to be accepted and then dialled as the TCP host
// "name", failing at runtime with no configuration error anywhere.
func TestValidateCGIPass(t *testing.T) {
	withPass := func(fastcgi, uwsgi string, ups []UpstreamConfig) *Config {
		cfg := validKnownValueConfig()
		cfg.Servers[0].Locations[0].ProxyPass = ""
		cfg.Servers[0].Locations[0].FastCGIPass = fastcgi
		cfg.Servers[0].Locations[0].UWSGIPass = uwsgi
		cfg.Upstreams = ups
		return cfg
	}
	phpPool := []UpstreamConfig{{
		Name:     "php",
		Strategy: "round_robin",
		Servers:  []UpstreamServer{{Address: "127.0.0.1:9000", Weight: 1}},
	}}

	t.Run("named upstream", func(t *testing.T) {
		if err := Validate(withPass("php", "", phpPool)); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("host:port literal", func(t *testing.T) {
		if err := Validate(withPass("127.0.0.1:9000", "", nil)); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("tcp:// literal", func(t *testing.T) {
		if err := Validate(withPass("tcp://127.0.0.1:9000", "", nil)); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("unix socket", func(t *testing.T) {
		if err := Validate(withPass("unix:/run/php-fpm.sock", "", nil)); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})
	t.Run("bare name matching no upstream is rejected", func(t *testing.T) {
		err := Validate(withPass("php", "", nil))
		if err == nil {
			t.Fatal("Validate accepted a fastcgi_pass naming no upstream; it would be dialled as the TCP host \"php\"")
		}
		if !strings.Contains(err.Error(), "neither a configured upstream name") {
			t.Fatalf("error = %v, want it to explain the target is unresolvable", err)
		}
	})
	t.Run("empty unix path is rejected", func(t *testing.T) {
		if err := Validate(withPass("unix:", "", nil)); err == nil {
			t.Fatal("Validate accepted an empty unix socket path")
		}
	})
	t.Run("uwsgi_pass is checked the same way", func(t *testing.T) {
		if err := Validate(withPass("", "wsgiapp", nil)); err == nil {
			t.Fatal("Validate accepted a uwsgi_pass naming no upstream")
		}
	})
}

// TestValidateRejectsHTTPHealthCheckOnUnixBackend pins that a probe which could
// never succeed is a configuration error rather than a pool that is silently
// always unhealthy.
func TestValidateRejectsHTTPHealthCheckOnUnixBackend(t *testing.T) {
	cfg := validKnownValueConfig()
	cfg.Upstreams = []UpstreamConfig{{
		Name:     "php",
		Strategy: "round_robin",
		Servers:  []UpstreamServer{{Address: "unix:/run/php-fpm.sock", Weight: 1}},
		HealthCheck: &HealthCheckConfig{
			Enabled: true, Type: "http", Path: "/ping",
			Interval: Duration(2 * time.Second), Timeout: Duration(time.Second),
			HealthyThreshold: 1, UnhealthyThreshold: 1,
		},
	}}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("Validate accepted an http health check against a unix-socket backend")
	}
	if !strings.Contains(err.Error(), "unix socket") {
		t.Fatalf("error = %v, want it to name the unix socket", err)
	}

	cfg.Upstreams[0].HealthCheck.Type = "tcp"
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate with a tcp probe: %v", err)
	}
}

// TestValidateRejectsMaxActiveRequestsOnUDPRoute pins the deliberate asymmetry:
// the UDP cap is per listener with idle eviction, the TCP cap is per pool.
// Layering a pool-scoped concurrency limit on UDP would be the overlapping
// mechanism this programme rejects, and the two would disagree about what a
// session is.
func TestValidateRejectsMaxActiveRequestsOnUDPRoute(t *testing.T) {
	base := func(proto string) *Config {
		cfg := validKnownValueConfig()
		cfg.Upstreams = []UpstreamConfig{{
			Name:       "l4",
			Strategy:   "round_robin",
			Servers:    []UpstreamServer{{Address: "127.0.0.1:5432", Weight: 1}},
			Resilience: &ResilienceConfig{MaxActiveRequests: 100},
		}}
		cfg.Streams = []StreamServer{{
			Listen:    "127.0.0.1:15432",
			Protocol:  proto,
			ProxyPass: "l4",
		}}
		return cfg
	}

	t.Run("udp route is rejected", func(t *testing.T) {
		err := Validate(base("udp"))
		if err == nil {
			t.Fatal("Validate accepted max_active_requests on a UDP stream route")
		}
		if !strings.Contains(err.Error(), "max_udp_sessions") {
			t.Fatalf("error = %v, want it to point at max_udp_sessions", err)
		}
	})

	t.Run("tcp route is accepted", func(t *testing.T) {
		if err := Validate(base("tcp")); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("max_udp_sessions is untouched", func(t *testing.T) {
		cfg := base("udp")
		cfg.Upstreams[0].Resilience = nil
		cfg.Streams[0].MaxUDPSessions = 2048
		if err := Validate(cfg); err != nil {
			t.Fatalf("Validate: %v", err)
		}
	})

	t.Run("udp route via sni_routes is rejected", func(t *testing.T) {
		cfg := base("udp")
		cfg.Streams[0].ProxyPass = "127.0.0.1:5432"
		cfg.Streams[0].SNIRoutes = map[string]string{"db.example.com": "l4"}
		if err := Validate(cfg); err == nil {
			t.Fatal("Validate accepted a bounded upstream reached through sni_routes on a UDP route")
		}
	})
}
