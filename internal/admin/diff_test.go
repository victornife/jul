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
