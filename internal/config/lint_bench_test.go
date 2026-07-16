// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"testing"
	"time"
)

// BenchmarkLintCleanConfig measures the happy path: a well-formed config that
// produces zero diagnostics.
func BenchmarkLintCleanConfig(b *testing.B) {
	c := &Config{
		Servers: []ServerConfig{{
			Listen:    ":443",
			TLS:       &TLSConfig{Enabled: true, Cert: "c", Key: "k", MinVersion: "1.3"},
			Locations: []LocationConfig{{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"}},
		}},
		Compression: CompressionConfig{Enabled: Bool(true)},
		Admin:       AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "${env:T}"},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Lint(c)
	}
}

// BenchmarkLintDirtyConfig measures the worst case: a config that triggers
// every lint rule.
func BenchmarkLintDirtyConfig(b *testing.B) {
	c := &Config{
		Servers: []ServerConfig{{
			Listen: ":80",
			Locations: []LocationConfig{
				{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv", DirectoryListing: true},
				{Match: MatchConfig{Type: "prefix", Path: "/"}, Root: "/srv"},
			},
			TLS: &TLSConfig{Enabled: true, Cert: "c", Key: "k"},
		}},
		Admin: AdminConfig{Enabled: true, Listen: "0.0.0.0:9090", Token: "literal"},
		Upstreams: []UpstreamConfig{{
			Discovery: &DiscoveryConfig{Type: "consul", Consul: &ConsulDiscovery{Token: "literal-consul"}},
		}},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Lint(c)
	}
}

// BenchmarkParseAndValidate measures the combined cost of parsing a modest
// config and running both validation + lint — the path `jul lint` exercises.
func BenchmarkParseAndValidate(b *testing.B) {
	data := []byte(`
[global]
log_level = "info"

[[servers]]
listen = "0.0.0.0:8080"
server_names = ["example.com"]

  [[servers.locations]]
  match = { type = "prefix", path = "/" }
  root = "/srv"

[admin]
enabled = true
listen = "127.0.0.1:9090"
token = "${env:T}"

[compression]
enabled = true
`)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cfg, _ := Parse(data)
		_ = Validate(cfg)
		_ = Lint(cfg)
	}
}

// BenchmarkServeDir measures zero-config synthesiser: how fast is it to spin
// up a ServeDir config that is immediately runnable.
func BenchmarkServeDir(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ServeDir("/var/www", ":8080")
	}
}

// BenchmarkProxyTarget measures zero-config synthesiser for the proxy case.
func BenchmarkProxyTarget(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ProxyTarget("127.0.0.1:3000", ":8080")
	}
}

func init() {
	// quiet the default logger during fuzz/bench runs
	_ = time.Now()
}
