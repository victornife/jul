// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

func validKnownValueConfig() *Config {
	return &Config{
		Global: GlobalConfig{
			LogLevel:        "info",
			LogFormat:       "text",
			WorkerThreads:   "auto",
			ShutdownTimeout: Duration(30 * time.Second),
			ReloadTimeout:   Duration(10 * time.Second),
		},
		Servers: []ServerConfig{{
			Listen:            "127.0.0.1:8080",
			ReadHeaderTimeout: Duration(10 * time.Second),
			IdleTimeout:       Duration(time.Minute),
			ClientMaxBodySize: Size(1 << 20),
			MaxHeaderBytes:    Size(1 << 20),
			Locations: []LocationConfig{{
				Match:     MatchConfig{Type: "prefix", Path: "/"},
				ProxyPass: "http://127.0.0.1:3000",
			}},
		}},
	}
}

func requireValidationError(t *testing.T, cfg *Config, want string) {
	t.Helper()
	err := Validate(cfg)
	if err == nil {
		t.Fatalf("Validate() succeeded, want error containing %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("Validate() error = %q, want containing %q", err, want)
	}
}

func TestValidateRejectsInvalidGlobalKnownValues(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"log level", func(c *Config) { c.Global.LogLevel = "verbose" }, `[global].log_level`},
		{"log level casing", func(c *Config) { c.Global.LogLevel = "INFO" }, `[global].log_level`},
		{"log format", func(c *Config) { c.Global.LogFormat = "yaml" }, `[global].log_format`},
		{"log format casing", func(c *Config) { c.Global.LogFormat = "JSON" }, `[global].log_format`},
		{"worker zero", func(c *Config) { c.Global.WorkerThreads = "0" }, `[global].worker_threads`},
		{"worker negative", func(c *Config) { c.Global.WorkerThreads = "-1" }, `[global].worker_threads`},
		{"worker fractional", func(c *Config) { c.Global.WorkerThreads = "1.5" }, `[global].worker_threads`},
		{"worker whitespace", func(c *Config) { c.Global.WorkerThreads = " 4" }, `[global].worker_threads`},
		{"worker casing", func(c *Config) { c.Global.WorkerThreads = "AUTO" }, `[global].worker_threads`},
		{"worker overflow", func(c *Config) { c.Global.WorkerThreads = "999999999999999999999999" }, `[global].worker_threads`},
		{"shutdown timeout", func(c *Config) { c.Global.ShutdownTimeout = -1 }, `[global].shutdown_timeout`},
		{"reload timeout", func(c *Config) { c.Global.ReloadTimeout = -1 }, `[global].reload_timeout`},
		{"redaction floor", func(c *Config) { c.Global.RedactMinSecretLength = -1 }, `[global].redact_min_secret_length`},
		{"config authority reserved value", func(c *Config) { c.Global.ConfigAuthority = "controller_owned" }, `reserved for a future release`},
		{"config authority invalid value", func(c *Config) { c.Global.ConfigAuthority = "sometimes" }, `[global].config_authority`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validKnownValueConfig()
			tc.set(cfg)
			requireValidationError(t, cfg, tc.want)
		})
	}
}

func TestValidateRejectsInvalidHTTPAndBackendScalars(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"server body", func(c *Config) { c.Servers[0].ClientMaxBodySize = -1 }, `servers[0].client_max_body_size`},
		{"server headers", func(c *Config) { c.Servers[0].MaxHeaderBytes = -1 }, `servers[0].max_header_bytes`},
		{"read header", func(c *Config) { c.Servers[0].ReadHeaderTimeout = -1 }, `servers[0].read_header_timeout`},
		{"read", func(c *Config) { c.Servers[0].ReadTimeout = -1 }, `servers[0].read_timeout`},
		{"write", func(c *Config) { c.Servers[0].WriteTimeout = -1 }, `servers[0].write_timeout`},
		{"idle", func(c *Config) { c.Servers[0].IdleTimeout = -1 }, `servers[0].idle_timeout`},
		{"redirect status", func(c *Config) { c.Servers[0].RedirectHTTPS = 302 }, `servers[0].redirect_https`},
		{"proxy connect", func(c *Config) { c.Servers[0].Locations[0].ProxyConnectTimeout = -1 }, `proxy_connect_timeout`},
		{"proxy read", func(c *Config) { c.Servers[0].Locations[0].ProxyReadTimeout = -1 }, `proxy_read_timeout`},
		{"proxy send", func(c *Config) { c.Servers[0].Locations[0].ProxySendTimeout = -1 }, `proxy_send_timeout`},
		{"proxy retries", func(c *Config) { c.Servers[0].Locations[0].ProxyRetries = -1 }, `proxy_retries`},
		{"location body", func(c *Config) { c.Servers[0].Locations[0].ClientMaxBodySize = -1 }, `locations[0].client_max_body_size`},
		{"return too low", func(c *Config) { c.Servers[0].Locations[0].ProxyPass = ""; c.Servers[0].Locations[0].Return = 99 }, `locations[0].return`},
		{"return too high", func(c *Config) { c.Servers[0].Locations[0].ProxyPass = ""; c.Servers[0].Locations[0].Return = 600 }, `locations[0].return`},
		{"upstream failures", func(c *Config) {
			c.Upstreams = []UpstreamConfig{{Name: "api", Servers: []UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}}, MaxFails: -1}}
		}, `upstreams[0].max_fails`},
		{"upstream timeout", func(c *Config) {
			c.Upstreams = []UpstreamConfig{{Name: "api", Servers: []UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}}, FailTimeout: -1}}
		}, `upstreams[0].fail_timeout`},
		{"upstream weight", func(c *Config) {
			c.Upstreams = []UpstreamConfig{{Name: "api", Servers: []UpstreamServer{{Address: "127.0.0.1:1", Weight: -1}}}}
		}, `upstreams[0].servers[0].weight`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validKnownValueConfig()
			tc.set(cfg)
			requireValidationError(t, cfg, tc.want)
		})
	}
}

func TestValidateRejectsInvalidCacheCompressionAndAdminScalars(t *testing.T) {
	tests := []struct {
		name string
		set  func(*Config)
		want string
	}{
		{"cache memory", func(c *Config) { c.Cache.MemoryMaxSize = -1 }, `[cache].memory_max_size`},
		{"cache disk", func(c *Config) { c.Cache.DiskMaxSize = -1 }, `[cache].disk_max_size`},
		{"cache ttl", func(c *Config) { c.Cache.DefaultTTL = -1 }, `[cache].default_ttl`},
		{"cache swr", func(c *Config) { c.Cache.StaleWhileRevalidate = -1 }, `[cache].stale_while_revalidate`},
		{"cache sie", func(c *Config) { c.Cache.StaleIfError = -1 }, `[cache].stale_if_error`},
		{"compression min size", func(c *Config) { c.Compression = CompressionConfig{Enabled: Bool(false), MinSize: -1} }, `[compression].min_size`},
		{"history keep disabled admin", func(c *Config) { c.Admin.HistoryKeep = -1 }, `[admin].history_keep`},
		{"event connections", func(c *Config) { c.Admin.MaxEventConns = -1 }, `[admin].max_event_conns`},
		{"audit max", func(c *Config) { c.Admin.AuditLogRotateMaxMB = -1 }, `[admin].audit_log_rotate_max_mb`},
		{"audit keep", func(c *Config) { c.Admin.AuditLogRotateKeep = -1 }, `[admin].audit_log_rotate_keep`},
		{"upload size", func(c *Config) { c.Admin.PluginUploadEnabled = Bool(false); c.Admin.PluginUploadMaxSize = -1 }, `[admin].plugin_upload_max_size`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validKnownValueConfig()
			tc.set(cfg)
			requireValidationError(t, cfg, tc.want)
		})
	}
}

func TestValidatePreservesDocumentedZeroAndDisableSemantics(t *testing.T) {
	cfg := validKnownValueConfig()
	cfg.Global = GlobalConfig{}
	cfg.Servers[0].ReadTimeout = 0
	cfg.Servers[0].WriteTimeout = 0
	cfg.Servers[0].Locations[0].ProxyConnectTimeout = 0
	cfg.Servers[0].Locations[0].ProxyReadTimeout = 0
	cfg.Servers[0].Locations[0].ProxySendTimeout = 0
	cfg.Servers[0].Locations[0].ProxyRetries = 0
	cfg.Servers[0].Locations[0].ClientMaxBodySize = 0
	cfg.Cache = CacheConfig{}
	cfg.Admin.RateLimitReadPerMin = -1
	cfg.Admin.RateLimitWritePerMin = -1
	cfg.Admin.RateLimitApplyPerMin = -1
	if err := Validate(cfg); err != nil {
		t.Fatalf("documented zero/disabled semantics rejected: %v", err)
	}
}

func TestKnownValueErrorsAreDeterministicAndActionable(t *testing.T) {
	cfg := validKnownValueConfig()
	cfg.Global.LogLevel = "verbose"
	cfg.Global.LogFormat = "yaml"
	cfg.Global.WorkerThreads = "0"
	first := Validate(cfg)
	second := Validate(cfg)
	if first == nil || second == nil || first.Error() != second.Error() {
		t.Fatalf("validation errors are not deterministic:\nfirst:  %v\nsecond: %v", first, second)
	}
	wantOrder := []string{"[global].log_level", "[global].log_format", "[global].worker_threads"}
	last := -1
	for _, want := range wantOrder {
		idx := strings.Index(first.Error(), want)
		if idx <= last {
			t.Fatalf("error order = %q; want %v in order", first, wantOrder)
		}
		last = idx
	}
}

func TestSizeRejectsOverflowBeforeMultiplication(t *testing.T) {
	for _, raw := range []string{
		"9223372036854775807k",
		"9007199254740992g",
		"9223372036854775808",
	} {
		var size Size
		err := size.UnmarshalText([]byte(raw))
		if err == nil || !strings.Contains(err.Error(), "overflow") && !strings.Contains(err.Error(), "value out of range") {
			t.Fatalf("Size.UnmarshalText(%q) = %v, want overflow error", raw, err)
		}
	}
	var max Size
	if err := max.UnmarshalText([]byte("9223372036854775807")); err != nil {
		t.Fatalf("max int64 bytes rejected: %v", err)
	}
	if max.Bytes() != math.MaxInt64 {
		t.Fatalf("max size = %d, want %d", max.Bytes(), int64(math.MaxInt64))
	}
}
