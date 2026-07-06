// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// diffHas reports whether any entry across additions/removals/modifications
// has a Detail containing substr.
func diffHas(d ConfigDiff, substr string) bool {
	for _, group := range [][]DiffEntry{d.Additions, d.Removals, d.Modifications} {
		for _, e := range group {
			if strings.Contains(e.Detail, substr) {
				return true
			}
		}
	}
	return false
}

func warnHas(d ConfigDiff, substr string) bool {
	for _, w := range d.Warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}

func TestDiffLocationActionAndTarget(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{{
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:3000",
		}},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{{
			Match:     config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass: "http://127.0.0.1:4000",
		}},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change target of route") {
		t.Fatalf("expected target change to be reported, got %+v", d)
	}
	if !warnHas(d, "redirects matching traffic") {
		t.Fatalf("expected target-change warning, got %+v", d.Warnings)
	}
}

func TestDiffRouteAuthCacheRateLimit(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{{
			Match: config.MatchConfig{Type: "prefix", Path: "/api"},
			Root:  "/srv",
		}},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":8080",
		Locations: []config.LocationConfig{{
			Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
			Root:      "/srv",
			Auth:      &config.AuthConfig{},
			Cache:     true,
			RateLimit: &config.RateLimitConfig{Rate: 10, Burst: 20},
		}},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "access control on route") {
		t.Errorf("expected auth toggle, got %+v", d)
	}
	if !diffHas(d, "response cache on route") {
		t.Errorf("expected cache toggle, got %+v", d)
	}
	if !diffHas(d, "rate-limit override on route") {
		t.Errorf("expected rate-limit override, got %+v", d)
	}
	if !warnHas(d, "serving private responses") {
		t.Errorf("expected cache-on-auth warning, got %+v", d.Warnings)
	}
}

// TestDiffLocationWAFAdvanced proves a per-location override's advanced SecLang
// fields — block status, request-body limit, response-body inspection — are
// reported in the diff, so the guided editor's full-field edits surface in the
// Validate → Diff → Apply review instead of landing silently.
func TestDiffLocationWAFAdvanced(t *testing.T) {
	loc := func(status int, limit config.Size, respCheck bool) config.LocationConfig {
		return config.LocationConfig{
			Match: config.MatchConfig{Type: "prefix", Path: "/api"},
			WAF: &config.WAFConfig{
				Enabled:           true,
				Mode:              "block",
				BlockStatus:       status,
				RequestBodyLimit:  limit,
				ResponseBodyCheck: respCheck,
				InlineRules:       `SecRule ARGS "@rx evil" "id:1,deny"`,
			},
		}
	}
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen:    ":8080",
		Locations: []config.LocationConfig{loc(403, config.Size(128<<10), false)},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen:    ":8080",
		Locations: []config.LocationConfig{loc(429, config.Size(64<<10), true)},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change WAF block status on route prefix /api") {
		t.Errorf("expected block-status change, got %+v", d)
	}
	if !diffHas(d, "Change WAF request body limit on route prefix /api") {
		t.Errorf("expected request-body-limit change, got %+v", d)
	}
	if !diffHas(d, "Enable WAF response-body inspection on route prefix /api") {
		t.Errorf("expected response-body inspection toggle, got %+v", d)
	}
}

func TestDiffTLSMinVersionAndACME(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443",
		TLS: &config.TLSConfig{
			Enabled:    true,
			MinVersion: "1.3",
			ACME:       &config.ACMEConfig{Enabled: true, CA: "letsencrypt"},
		},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443",
		TLS: &config.TLSConfig{
			Enabled:    true,
			MinVersion: "1.2",
			ACME:       &config.ACMEConfig{Enabled: true, CA: "letsencrypt-staging"},
		},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change TLS minimum version") {
		t.Errorf("expected min version change, got %+v", d)
	}
	if !warnHas(d, "weakens transport security") {
		t.Errorf("expected min version warning, got %+v", d.Warnings)
	}
	if !diffHas(d, "Change ACME directory (CA)") {
		t.Errorf("expected ACME CA change, got %+v", d)
	}
}

func TestDiffMTLSToggle(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443",
		TLS:    &config.TLSConfig{Enabled: true},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen: ":443",
		TLS: &config.TLSConfig{
			Enabled:    true,
			ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem"},
		},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "mutual TLS") {
		t.Errorf("expected mTLS enable, got %+v", d)
	}
}

func TestDiffMTLSVerifySAN(t *testing.T) {
	mk := func(sans ...string) *config.Config {
		return &config.Config{Servers: []config.ServerConfig{{
			Listen: ":443",
			TLS: &config.TLSConfig{
				Enabled:    true,
				ClientAuth: &config.ClientAuthConfig{Mode: "require", CAFile: "/etc/ca.pem", VerifySAN: sans},
			},
		}}}
	}
	d := diffConfigs(mk("svc-a.internal"), mk("svc-a.internal", "svc-b.internal"))
	if !diffHas(d, "Change mutual TLS SAN allow-list") {
		t.Errorf("expected verify_san change to be diffed, got %+v", d)
	}
	if !warnHas(d, "SAN allow-list") {
		t.Errorf("expected verify_san change warning, got %+v", d.Warnings)
	}
	// No change must not emit a diff entry.
	if d2 := diffConfigs(mk("svc-a.internal"), mk("svc-a.internal")); diffHas(d2, "SAN allow-list") {
		t.Errorf("unexpected verify_san diff for identical lists, got %+v", d2)
	}
}

func TestDiffUpstreamBackendsAndRetries(t *testing.T) {
	before := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:     "app",
		Strategy: "round_robin",
		MaxFails: 3,
		Servers:  []config.UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
	}}}
	after := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:     "app",
		Strategy: "least_conn",
		MaxFails: 0,
		Servers: []config.UpstreamServer{
			{Address: "127.0.0.1:3000", Weight: 2},
			{Address: "127.0.0.1:3001", Weight: 1},
		},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change load-balancing strategy") {
		t.Errorf("expected strategy change, got %+v", d)
	}
	if !diffHas(d, "Add backend 127.0.0.1:3001") {
		t.Errorf("expected backend addition, got %+v", d)
	}
	if !diffHas(d, "Change weight of backend 127.0.0.1:3000") {
		t.Errorf("expected weight change, got %+v", d)
	}
	if !diffHas(d, "Change max_fails") {
		t.Errorf("expected max_fails change, got %+v", d)
	}
	if !warnHas(d, "disables passive health checking") {
		t.Errorf("expected max_fails=0 warning, got %+v", d.Warnings)
	}
}

func TestDiffUpstreamHealthAndDiscovery(t *testing.T) {
	before := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:        "app",
		Servers:     []config.UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/healthz", Interval: config.Duration(5e9), Timeout: config.Duration(2e9)},
		Discovery:   &config.DiscoveryConfig{Type: "dns", Target: "svc:8080", Refresh: config.Duration(30e9)},
	}}}
	after := &config.Config{Upstreams: []config.UpstreamConfig{{
		Name:        "app",
		Servers:     []config.UpstreamServer{{Address: "127.0.0.1:3000", Weight: 1}},
		HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "http", Path: "/ready", Interval: config.Duration(10e9), Timeout: config.Duration(2e9)},
		Discovery:   &config.DiscoveryConfig{Type: "dns", Target: "svc:9090", Refresh: config.Duration(15e9)},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change health-check path") {
		t.Errorf("expected health-check path change, got %+v", d)
	}
	if !diffHas(d, "Change health-check interval") {
		t.Errorf("expected health-check interval change, got %+v", d)
	}
	if !diffHas(d, "Change discovery target") {
		t.Errorf("expected discovery target change, got %+v", d)
	}
	if !diffHas(d, "Change discovery refresh interval") {
		t.Errorf("expected discovery refresh change, got %+v", d)
	}

	// Enable/disable health checks and switch the discovery provider type.
	on := &config.Config{Upstreams: []config.UpstreamConfig{{Name: "app", Servers: before.Upstreams[0].Servers, HealthCheck: &config.HealthCheckConfig{Enabled: true, Type: "tcp", Interval: config.Duration(5e9), Timeout: config.Duration(2e9)}}}}
	off := &config.Config{Upstreams: []config.UpstreamConfig{{Name: "app", Servers: before.Upstreams[0].Servers}}}
	if d := diffConfigs(off, on); !diffHas(d, "Enable active health checks") {
		t.Errorf("expected health-check enable, got %+v", d)
	}
	if d := diffConfigs(on, off); !diffHas(d, "Disable active health checks") {
		t.Errorf("expected health-check disable, got %+v", d)
	}
}

func TestDiffGlobalCacheCompressionRateLimit(t *testing.T) {
	before := &config.Config{
		Cache:       config.CacheConfig{Enabled: true, DefaultTTL: config.Duration(0)},
		Compression: config.CompressionConfig{Enabled: true, Encoders: []string{"gzip"}},
		RateLimit:   config.RateLimitConfig{Enabled: true, Rate: 100, Burst: 100},
	}
	after := &config.Config{
		Cache:       config.CacheConfig{Enabled: true, DiskPath: "/var/cache"},
		Compression: config.CompressionConfig{Enabled: true, Encoders: []string{"gzip", "br"}},
		RateLimit:   config.RateLimitConfig{Enabled: false},
	}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change cache disk path") {
		t.Errorf("expected cache disk change, got %+v", d)
	}
	if !diffHas(d, "Change compression encoders") {
		t.Errorf("expected compression encoders change, got %+v", d)
	}
	if !diffHas(d, "Disable global rate limiting") {
		t.Errorf("expected rate-limit disable, got %+v", d)
	}
	if !warnHas(d, "request floods") {
		t.Errorf("expected rate-limit warning, got %+v", d.Warnings)
	}
}

// TestDiffGlobalWAFBuildTag verifies that enabling the global WAF reports the
// toggle and warns it only enforces in a waf-tagged build, mirroring the
// plugins/streams/tracing build-tag disclosures.
func TestDiffGlobalWAFBuildTag(t *testing.T) {
	mk := func(enabled bool) *config.Config {
		return &config.Config{WAF: config.WAFConfig{Enabled: enabled, Mode: "block"}}
	}
	// Enable from disabled: the toggle plus the build-tag warning.
	d := diffConfigs(mk(false), mk(true))
	if !diffHas(d, "Enable global WAF") {
		t.Errorf("expected WAF enable entry, got %+v", d)
	}
	if !warnHas(d, "waf tag") {
		t.Errorf("expected waf build-tag warning, got %+v", d.Warnings)
	}
	// Disable: the build-tag warning must not appear (only protection-loss).
	d = diffConfigs(mk(true), mk(false))
	if warnHas(d, "waf tag") {
		t.Errorf("did not expect build-tag warning on disable, got %+v", d.Warnings)
	}
}

func TestDiffGlobalTracing(t *testing.T) {
	tracing := func(tc config.TracingConfig) *config.Config {
		return &config.Config{Observability: config.ObservabilityConfig{Tracing: tc}}
	}

	// Enable from disabled: reports the toggle and warns about the otel build tag.
	d := diffConfigs(tracing(config.TracingConfig{}), tracing(config.TracingConfig{
		Enabled: true, Exporter: "otlp-grpc", Endpoint: "localhost:4317", SampleRatio: 1,
	}))
	if !diffHas(d, "Enable distributed tracing") {
		t.Errorf("expected tracing enable, got %+v", d)
	}
	if !warnHas(d, "otel") {
		t.Errorf("expected otel build-tag warning, got %+v", d.Warnings)
	}

	// Field changes on an already-enabled block, including a switch to insecure.
	before := tracing(config.TracingConfig{
		Enabled: true, Exporter: "otlp-grpc", Endpoint: "localhost:4317", SampleRatio: 1, ServiceName: "jul",
	})
	after := tracing(config.TracingConfig{
		Enabled: true, Exporter: "otlp-http", Endpoint: "http://collector:4318", SampleRatio: 0.1, ServiceName: "edge", Insecure: true,
	})
	d = diffConfigs(before, after)
	for _, want := range []string{
		"Change tracing exporter",
		"Change tracing collector endpoint",
		"Change tracing sample ratio",
		"Change tracing service name",
		"Change tracing transport security",
	} {
		if !diffHas(d, want) {
			t.Errorf("expected %q, got %+v", want, d)
		}
	}
	if !warnHas(d, "plaintext") {
		t.Errorf("expected insecure-transport warning, got %+v", d.Warnings)
	}

	// Disable from enabled.
	d = diffConfigs(before, tracing(config.TracingConfig{}))
	if !diffHas(d, "Disable distributed tracing") {
		t.Errorf("expected tracing disable, got %+v", d)
	}
}

func TestDiffServerTimeouts(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":8080",
		ReadTimeout: config.Duration(0),
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":8080",
		ReadTimeout: config.Duration(0),
	}}}
	// No change → no timeout entries.
	if d := diffConfigs(before, after); diffHas(d, "read timeout") {
		t.Errorf("did not expect timeout change, got %+v", d)
	}

	after2 := &config.Config{Servers: []config.ServerConfig{{Listen: ":8080"}}}
	before2 := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":8080",
		ReadTimeout: config.Duration(30 * 1e9),
	}}}
	d := diffConfigs(before2, after2)
	if !diffHas(d, "read timeout") {
		t.Errorf("expected read timeout change, got %+v", d)
	}
	if !warnHas(d, "removes a safety bound") {
		t.Errorf("expected timeout-cleared warning, got %+v", d.Warnings)
	}
}

// TestDiffServerHostNames proves a route_rename that keeps the first host name
// (so the block still indexes to the same key) is reported as a clean host-name
// change with a routing warning, rather than being silently dropped.
func TestDiffServerHostNames(t *testing.T) {
	before := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"a.example", "old.example"},
	}}}
	after := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"a.example", "new.example"},
	}}}
	d := diffConfigs(before, after)
	if !diffHas(d, "Change host names") {
		t.Fatalf("expected a host-name change entry, got %+v", d)
	}
	if !warnHas(d, "Host/SNI") {
		t.Fatalf("expected a routing warning, got %+v", d.Warnings)
	}

	// Reordering the trailing names (with the first name unchanged, so the block
	// still indexes to the same key) is not reported as a change.
	base := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"a.example", "old.example", "z.example"},
	}}}
	reordered := &config.Config{Servers: []config.ServerConfig{{
		Listen:      ":443",
		ServerNames: []string{"a.example", "z.example", "old.example"},
	}}}
	if d := diffConfigs(base, reordered); diffHas(d, "Change host names") {
		t.Errorf("did not expect a change for reordered names, got %+v", d)
	}
}
