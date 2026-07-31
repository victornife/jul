// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the global-config, plugin, and stream diff functions.
// Server/location/upstream diff functions and utility helpers live in
// diff_helpers.go.

import (
	"fmt"
	"strings"
	"time"

	"jul/internal/config"
	"jul/internal/rbac"
)

// diffGlobalSettings compares the [global] block. All fields are hot-reloadable
// except log_format which is restart-required (gated before diff is shown).
func diffGlobalSettings(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Global, after.Global
	if b.LogLevel != a.LogLevel {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: orNone(b.LogLevel), After: orNone(a.LogLevel), Detail: "Change log level (hot-reloadable)"}, "global log_level")
	}
	d.cover("global.log_level")
	if b.LogFormat != a.LogFormat {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: orNone(b.LogFormat), After: orNone(a.LogFormat), Detail: "Change log format — restart required to apply"}, "global log_format")
	}
	d.cover("global.log_format")
	if b.WorkerThreads != a.WorkerThreads {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: orNone(b.WorkerThreads), After: orNone(a.WorkerThreads), Detail: "Change worker threads / GOMAXPROCS (hot-reloadable)"}, "global worker_threads")
	}
	d.cover("global.worker_threads")
	if b.ShutdownTimeout != a.ShutdownTimeout {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: durStr(b.ShutdownTimeout), After: durStr(a.ShutdownTimeout), Detail: "Change graceful shutdown timeout"}, "global shutdown_timeout")
	}
	d.cover("global.shutdown_timeout")
	if b.ReloadTimeout != a.ReloadTimeout {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: durStr(b.ReloadTimeout), After: durStr(a.ReloadTimeout), Detail: "Change reload timeout threshold"}, "global reload_timeout")
	}
	if b.RedactMinSecretLength != a.RedactMinSecretLength {
		d.mod(DiffEntry{Kind: "global", Name: "global", Before: fmt.Sprintf("%d", b.RedactMinSecretLength), After: fmt.Sprintf("%d", a.RedactMinSecretLength), Detail: "Change secret redaction minimum length"}, "global redact_min")
	}
	d.cover("global.redact_min_secret_length")
}

func diffGlobalCache(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Cache, after.Cache
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cache", Name: "global", Detail: action + " the response cache"}, "cache")
		d.cover("cache.enabled")
		d.cover("cache.memory_max_size")
		d.cover("cache.disk_path")
		d.cover("cache.disk_max_size")
		return
	}
	if !a.Enabled {
		d.cover("cache.enabled")
		return
	}
	if b.MemoryMaxSize != a.MemoryMaxSize {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: sizeStr(b.MemoryMaxSize), After: sizeStr(a.MemoryMaxSize), Detail: "Change cache memory size"}, "cache memory")
	}
	if b.DiskPath != a.DiskPath {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: orNone(b.DiskPath), After: orNone(a.DiskPath), Detail: "Change cache disk path"}, "cache disk")
	}
	if b.DiskMaxSize != a.DiskMaxSize {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: sizeStr(b.DiskMaxSize), After: sizeStr(a.DiskMaxSize), Detail: "Change cache disk size"}, "cache disk size")
	}
	if b.DefaultTTL != a.DefaultTTL {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.DefaultTTL), After: durStr(a.DefaultTTL), Detail: "Change cache default TTL"}, "cache ttl")
	}
	if b.StaleWhileRevalidate != a.StaleWhileRevalidate {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.StaleWhileRevalidate), After: durStr(a.StaleWhileRevalidate), Detail: "Change cache stale-while-revalidate window"}, "cache swr")
	}
	if b.StaleIfError != a.StaleIfError {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: durStr(b.StaleIfError), After: durStr(a.StaleIfError), Detail: "Change cache stale-if-error window"}, "cache sif")
	}
	d.cover("cache.enabled")
	d.cover("cache.memory_max_size")
	d.cover("cache.disk_path")
	d.cover("cache.disk_max_size")
}

// diffGlobalCompression compares the [compression] block.
func diffGlobalCompression(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Compression, after.Compression
	if b.IsEnabled() != a.IsEnabled() {
		action := "Enable"
		if !a.IsEnabled() {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "compression", Name: "global", Detail: action + " response compression"}, "compression")
		d.cover("compression.enabled")
		d.cover("compression.types")
		d.cover("compression.min_length")
		return
	}
	if !a.IsEnabled() {
		d.cover("compression.enabled")
		return
	}
	if strings.Join(b.Encoders, ",") != strings.Join(a.Encoders, ",") {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: orNone(strings.Join(b.Encoders, ", ")), After: orNone(strings.Join(a.Encoders, ", ")), Detail: "Change compression encoders"}, "compression encoders")
	}
	if b.Level != a.Level {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: fmt.Sprintf("%d", b.Level), After: fmt.Sprintf("%d", a.Level), Detail: "Change compression level"}, "compression level")
	}
	if b.MinSize != a.MinSize {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Before: sizeStr(b.MinSize), After: sizeStr(a.MinSize), Detail: "Change compression minimum size"}, "compression min size")
	}
	if strings.Join(b.Types, ",") != strings.Join(a.Types, ",") {
		d.mod(DiffEntry{Kind: "compression", Name: "global", Detail: "Change compression content types"}, "compression types")
	}
	if b.Precompressed != a.Precompressed {
		action := "Enable"
		if !a.Precompressed {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "compression", Name: "global", Detail: action + " precompressed sidecar serving"}, "compression precompressed")
	}
	d.cover("compression.enabled")
	d.cover("compression.types")
	d.cover("compression.min_length")
}

// diffGlobalRateLimit compares the global [rate_limit] block.
func diffGlobalRateLimit(before, after *config.Config, d *ConfigDiff) {
	b, a := before.RateLimit, after.RateLimit
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Detail: action + " global rate limiting"}, "rate limit")
		if !a.Enabled {
			d.warn("Disabling global rate limiting removes protection against request floods.")
		}
		d.cover("rate_limit.enabled")
		d.cover("rate_limit.rate")
		d.cover("rate_limit.burst")
		return
	}
	if !a.Enabled {
		d.cover("rate_limit.enabled")
		return
	}
	if b.Key != a.Key || b.Rate != a.Rate || b.Burst != a.Burst {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: rlStr(&b), After: rlStr(&a), Detail: "Change global rate-limit policy"}, "rate limit policy")
	}
	if b.MaxConns != a.MaxConns {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: fmt.Sprintf("%d", b.MaxConns), After: fmt.Sprintf("%d", a.MaxConns), Detail: "Change max concurrent connections"}, "rate limit max conns")
	}
	d.cover("rate_limit.enabled")
	d.cover("rate_limit.rate")
	d.cover("rate_limit.burst")
}

// diffGlobalWAF compares the global [waf] block.
func diffGlobalWAF(before, after *config.Config, d *ConfigDiff) {
	b, a := before.WAF, after.WAF
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: action + " global WAF"}, "waf global")
		if a.Enabled {
			d.warn("Enabling the WAF inspects requests against the configured rules; it only enforces in binaries built with the waf tag, and the apply preflight rejects an enabled WAF otherwise.")
		} else {
			d.warn("Disabling the global WAF removes rule inspection from routes that do not have a per-location override.")
		}
		d.cover("waf.enabled")
		d.cover("waf.mode")
		d.cover("waf.crs_enabled")
		return
	}
	if !a.Enabled {
		d.cover("waf.enabled")
		return
	}
	if b.Mode != a.Mode {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: b.Mode, After: a.Mode, Detail: "Change global WAF mode"}, "waf global mode")
		if b.Mode == "block" && a.Mode == "detect" {
			d.warn("Switching global WAF to detect mode stops blocking threats.")
		}
	}
	if b.BlockStatus != a.BlockStatus {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: fmt.Sprintf("%d", b.BlockStatus), After: fmt.Sprintf("%d", a.BlockStatus), Detail: "Change global WAF block status"}, "waf global block_status")
	}
	if b.Paranoia != a.Paranoia {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: fmt.Sprintf("%d", b.Paranoia), After: fmt.Sprintf("%d", a.Paranoia), Detail: "Change global WAF paranoia level"}, "waf global paranoia")
		if a.Paranoia < b.Paranoia {
			d.warn("Lowering global WAF paranoia reduces rule coverage.")
		}
	}
	if b.CRSEnabled != a.CRSEnabled {
		action := "Enable"
		if !a.CRSEnabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: action + " global CRS"}, "waf global crs")
		if !a.CRSEnabled {
			d.warn("Disabling the global CRS removes the core rule set.")
		}
	}
	if b.RequestBodyLimit != a.RequestBodyLimit {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Before: sizeStr(b.RequestBodyLimit), After: sizeStr(a.RequestBodyLimit), Detail: "Change global WAF request body limit on global"}, "waf global body_limit")
		if a.RequestBodyLimit.Bytes() == 0 && b.RequestBodyLimit.Bytes() != 0 {
			d.warn("Removing the global WAF request body limit allows arbitrarily large uploads to be inspected.")
		}
	}
	if b.ResponseBodyCheck != a.ResponseBodyCheck {
		action := "Enable"
		if !a.ResponseBodyCheck {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: action + " global WAF response body inspection"}, "waf global response_body_check")
	}
	if strings.Join(b.DirectivesFiles, ",") != strings.Join(a.DirectivesFiles, ",") {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: "Change global WAF directives files"}, "waf global directives_files")
	}
	if b.InlineRules != a.InlineRules {
		d.mod(DiffEntry{Kind: "waf", Name: "global", Detail: "Change global WAF inline rules"}, "waf global inline_rules")
	}
	d.cover("waf.enabled")
	d.cover("waf.mode")
	d.cover("waf.crs_enabled")
}

func diffSecretRefs(before, after *config.Config, d *ConfigDiff) {
	bN := config.CountSecretRefs(before)
	aN := config.CountSecretRefs(after)
	if bN != aN {
		d.mod(DiffEntry{Kind: "secrets", Name: "global", Before: fmt.Sprintf("%d", bN), After: fmt.Sprintf("%d", aN), Detail: "Change secret reference count"}, "secret refs")
	}
}

// diffGlobalTracing compares the [observability.tracing] block. Tracing changes
// govern what telemetry leaves the process and where it is sent, so each is
// surfaced explicitly; enabling exports spans and is only active in binaries
// built with the otel tag.
func diffGlobalTracing(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Observability.Tracing, after.Observability.Tracing
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Detail: action + " distributed tracing"}, "tracing")
		if a.Enabled {
			d.warn("Enabling tracing exports spans to the collector; it is only active in binaries built with the otel tag.")
		}
		d.cover("observability.tracing.enabled")
		d.cover("observability.tracing.endpoint")
		d.cover("observability.tracing.sample_ratio")
		return
	}
	if !a.Enabled {
		d.cover("observability.tracing.enabled")
		return
	}
	if b.Exporter != a.Exporter {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.Exporter), After: orNone(a.Exporter), Detail: "Change tracing exporter"}, "tracing exporter")
	}
	if b.Endpoint != a.Endpoint {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.Endpoint), After: orNone(a.Endpoint), Detail: "Change tracing collector endpoint"}, "tracing endpoint")
	}
	if b.SampleRatio != a.SampleRatio {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: fmt.Sprintf("%g", b.SampleRatio), After: fmt.Sprintf("%g", a.SampleRatio), Detail: "Change tracing sample ratio"}, "tracing sample_ratio")
	}
	if b.ServiceName != a.ServiceName {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Before: orNone(b.ServiceName), After: orNone(a.ServiceName), Detail: "Change tracing service name"}, "tracing service_name")
	}
	if b.Insecure != a.Insecure {
		d.mod(DiffEntry{Kind: "tracing", Name: "global", Detail: "Change tracing transport security"}, "tracing insecure")
		if a.Insecure {
			d.warn("Tracing now sends spans over plaintext (insecure); only use this for a local collector on a trusted network.")
		}
	}
	d.cover("observability.tracing.enabled")
	d.cover("observability.tracing.endpoint")
	d.cover("observability.tracing.sample_ratio")
}

// diffGlobalPlugins compares the declared WASM plugin set ([plugins.NAME]),
// reporting added/removed declarations and per-plugin changes to the module
// source, type, granted host capabilities, and limits. Attachment (which routes
// run a plugin) is diffed per-location in diffLocationFields.
func diffGlobalPlugins(before, after *config.Config, d *ConfigDiff) {
	for _, name := range sortedKeys(after.Plugins) {
		a := after.Plugins[name]
		b, ok := before.Plugins[name]
		if !ok {
			d.add(DiffEntry{Kind: "plugin", Name: name, After: pluginSummary(a), Detail: "Add plugin " + name}, "plugin "+name)
			d.warn("Plugin %s runs guest WASM; it only loads in binaries built with the wasmplugins tag, and the apply preflight rejects it otherwise.", name)
			continue
		}
		diffPluginFields(name, b, a, d)
	}
	for _, name := range sortedKeys(before.Plugins) {
		if _, ok := after.Plugins[name]; !ok {
			b := before.Plugins[name]
			d.del(DiffEntry{Kind: "plugin", Name: name, Before: pluginSummary(b), Detail: "Remove plugin " + name}, "plugin "+name)
		}
	}
}

// diffPluginFields reports per-plugin declaration changes between matched
// [plugins.NAME] blocks, warning when a host capability (kv/fetch) is newly
// granted.
func diffPluginFields(name string, b, a config.PluginConfig, d *ConfigDiff) {
	if pluginSource(b) != pluginSource(a) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: pluginSource(b), After: pluginSource(a), Detail: "Change plugin module source for " + name}, "plugin "+name+" source")
	}
	if pluginTypeOrDefault(b) != pluginTypeOrDefault(a) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: pluginTypeOrDefault(b), After: pluginTypeOrDefault(a), Detail: "Change plugin type for " + name}, "plugin "+name+" type")
	}
	if b.KV != a.KV {
		action := "Grant"
		if !a.KV {
			action = "Revoke"
		}
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: fmt.Sprintf("%s KV store access for plugin %s", action, name)}, "plugin "+name+" kv")
		if a.KV {
			d.warn("Plugin %s now has KV store access; it can read and write shared key-value state.", name)
		}
	}
	if b.Fetch != a.Fetch {
		action := "Grant"
		if !a.Fetch {
			action = "Revoke"
		}
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: fmt.Sprintf("%s outbound fetch for plugin %s", action, name)}, "plugin "+name+" fetch")
		if a.Fetch {
			d.warn("Plugin %s can now make outbound HTTP requests; it is restricted to the allowed_hosts allowlist.", name)
		}
	}
	if bf, af := strings.Join(b.AllowedHosts, ","), strings.Join(a.AllowedHosts, ","); bf != af {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: orNone(bf), After: orNone(af), Detail: "Change plugin fetch allowlist for " + name}, "plugin "+name+" allowed_hosts")
	}
	if !stringMapEqual(b.Config, a.Config) {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Detail: "Change plugin config for " + name}, "plugin "+name+" config")
	}
	if b.MemoryLimit != a.MemoryLimit {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.MemoryLimit), After: sizeStr(a.MemoryLimit), Detail: "Change plugin memory limit for " + name}, "plugin "+name+" memory_limit")
	}
	if b.Timeout != a.Timeout {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: durStr(b.Timeout), After: durStr(a.Timeout), Detail: "Change plugin timeout for " + name}, "plugin "+name+" timeout")
	}
	if b.MaxRequestBody != a.MaxRequestBody {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.MaxRequestBody), After: sizeStr(a.MaxRequestBody), Detail: "Change plugin max request body for " + name}, "plugin "+name+" max_request_body")
	}
	if b.MaxResponseBody != a.MaxResponseBody {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.MaxResponseBody), After: sizeStr(a.MaxResponseBody), Detail: "Change plugin max response body for " + name}, "plugin "+name+" max_response_body")
	}
	if b.FetchTimeout != a.FetchTimeout {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: durStr(b.FetchTimeout), After: durStr(a.FetchTimeout), Detail: "Change plugin fetch timeout for " + name}, "plugin "+name+" fetch_timeout")
	}
	if b.MaxFetchResponse != a.MaxFetchResponse {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.MaxFetchResponse), After: sizeStr(a.MaxFetchResponse), Detail: "Change plugin max fetch response for " + name}, "plugin "+name+" max_fetch_response")
	}
	if b.KVMaxEntries != a.KVMaxEntries {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: fmt.Sprintf("%d", b.KVMaxEntries), After: fmt.Sprintf("%d", a.KVMaxEntries), Detail: "Change plugin KV max entries for " + name}, "plugin "+name+" kv_max_entries")
	}
	if b.KVMaxBytes != a.KVMaxBytes {
		d.mod(DiffEntry{Kind: "plugin", Name: name, Before: sizeStr(b.KVMaxBytes), After: sizeStr(a.KVMaxBytes), Detail: "Change plugin KV max bytes for " + name}, "plugin "+name+" kv_max_bytes")
	}
}

// pluginSource renders a plugin's module source for a diff: "inline" for an
// embedded module, or "path <file>" for a file-backed one.
func pluginSource(p config.PluginConfig) string {
	if strings.TrimSpace(p.Inline) != "" {
		return "inline"
	}
	return "path " + p.Path
}

// stringSetDiff returns the elements added to and removed from a string slice
// (set semantics, ignoring order and duplicates), used to diff a location's
// plugin middleware chain.
func stringSetDiff(before, after []string) (added, removed []string) {
	bset := make(map[string]bool, len(before))
	for _, s := range before {
		bset[s] = true
	}
	aset := make(map[string]bool, len(after))
	for _, s := range after {
		aset[s] = true
	}
	for _, s := range after {
		if !bset[s] {
			added = append(added, s)
			bset[s] = true // dedupe
		}
	}
	for _, s := range before {
		if !aset[s] {
			removed = append(removed, s)
			aset[s] = true // dedupe
		}
	}
	return added, removed
}

// stringMapEqual reports whether two string maps have identical keys and values.
func stringMapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

// locationEffectiveWAF returns the WAF policy that applies to a location,
// taking inheritance from the global policy into account.  If the location
// does not define a WAF block at all, the global policy is returned (when
// enabled).  A nil result means no WAF protection for this location.
func locationEffectiveWAF(loc *config.LocationConfig, global config.WAFConfig) *config.WAFConfig {
	if loc.WAF != nil {
		if loc.WAF.Enabled {
			return loc.WAF
		}
		return nil
	}
	if global.Enabled {
		return &global
	}
	return nil
}

// diffStreams compares the declared [[stream]] L4 listeners, reporting added and
// removed listeners plus per-listener field changes. Streams are a slice keyed
// by their protocol + listen identity (the same key the validator dedups on), so
// a change to that identity surfaces as a remove + add rather than a modify.
func diffStreams(before, after *config.Config, d *ConfigDiff) {
	bs, as := streamIndex(before.Streams), streamIndex(after.Streams)
	for _, key := range sortedKeys(as) {
		a := as[key]
		b, ok := bs[key]
		if !ok {
			d.add(DiffEntry{Kind: "stream", Name: key, After: streamSummary(a), Detail: "Add L4 stream listener " + key}, "stream "+key)
			d.warn("Stream %s opens an L4 (TCP/UDP) listener; it only serves in binaries built with the stream tag, and a lean binary refuses to start with it.", key)
			continue
		}
		diffStreamFields(key, b, a, d)
	}
	for _, key := range sortedKeys(bs) {
		if _, ok := as[key]; !ok {
			b := bs[key]
			d.del(DiffEntry{Kind: "stream", Name: key, Before: streamSummary(b), Detail: "Remove L4 stream listener " + key}, "stream "+key)
			d.warn("Removing stream %s stops L4 proxying on that listener.", key)
		}
	}
}

// streamIndex keys a stream slice by its normalized "proto/listen" identity for
// diffing. A duplicate key (which the validated config rejects) keeps the last
// occurrence, mirroring how the runtime would treat the live set.
func streamIndex(streams []config.StreamServer) map[string]config.StreamServer {
	out := make(map[string]config.StreamServer, len(streams))
	for _, st := range streams {
		out[streamProtoOrDefault(st.Protocol)+"/"+strings.TrimSpace(st.Listen)] = st
	}
	return out
}

// diffStreamFields reports per-listener changes between two [[stream]] blocks
// with the same proto/listen identity: the default target, SNI routes, TLS
// passthrough, the PROXY protocol, and the connect/idle timeouts.
func diffStreamFields(key string, b, a config.StreamServer, d *ConfigDiff) {
	if strings.TrimSpace(b.ProxyPass) != strings.TrimSpace(a.ProxyPass) {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: orNone(b.ProxyPass), After: orNone(a.ProxyPass), Detail: "Change default backend for stream " + key}, "stream "+key+" proxy_pass")
	}
	d.cover("stream.*.proxy_pass")
	if !stringMapEqual(trimSNIRoutes(b.SNIRoutes), trimSNIRoutes(a.SNIRoutes)) {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: fmt.Sprintf("%d route%s", len(b.SNIRoutes), plural(len(b.SNIRoutes))), After: fmt.Sprintf("%d route%s", len(a.SNIRoutes), plural(len(a.SNIRoutes))), Detail: "Change SNI routes for stream " + key}, "stream "+key+" sni_routes")
	}
	d.cover("stream.*.sni_routes")
	if b.TLSPassthrough != a.TLSPassthrough {
		d.mod(DiffEntry{Kind: "stream", Name: key, Detail: "Change TLS passthrough flag for stream " + key}, "stream "+key+" tls_passthrough")
	}
	if bp, ap := strings.ToLower(strings.TrimSpace(b.ProxyProtocol)), strings.ToLower(strings.TrimSpace(a.ProxyProtocol)); bp != ap {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: orNone(bp), After: orNone(ap), Detail: "Change PROXY protocol for stream " + key}, "stream "+key+" proxy_protocol")
	}
	if b.ConnectTimeout != a.ConnectTimeout {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: durStr(b.ConnectTimeout), After: durStr(a.ConnectTimeout), Detail: "Change connect timeout for stream " + key}, "stream "+key+" connect_timeout")
	}
	if b.IdleTimeout != a.IdleTimeout {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: durStr(b.IdleTimeout), After: durStr(a.IdleTimeout), Detail: "Change idle timeout for stream " + key}, "stream "+key+" idle_timeout")
	}
	if b.MaxUDPSessions != a.MaxUDPSessions {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: fmt.Sprintf("%d", b.MaxUDPSessions), After: fmt.Sprintf("%d", a.MaxUDPSessions), Detail: "Change max UDP sessions for stream " + key}, "stream "+key+" max_udp_sessions")
	}
}

// diffGlobalEgress compares the [egress] allow-list. Changes are restart-required
// (the dial policy is built once at startup) and are surfaced in the diff so
// the operator can review what changed before the 409 rejection.
func diffGlobalEgress(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Egress, after.Egress
	if b.Enabled == a.Enabled && strings.Join(b.Allow, ",") == strings.Join(a.Allow, ",") {
		d.cover("egress.enabled")
		d.cover("egress.allow")
		return
	}
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "egress", Name: "global", Detail: action + " egress allow-list — restart required to apply"}, "egress")
	}
	if strings.Join(b.Allow, ",") != strings.Join(a.Allow, ",") {
		d.mod(DiffEntry{Kind: "egress", Name: "global",
			Before: fmt.Sprintf("%d entries", len(b.Allow)),
			After:  fmt.Sprintf("%d entries", len(a.Allow)),
			Detail: "Change egress allow-list — restart required to apply",
		}, "egress allow")
		if len(a.Allow) < len(b.Allow) {
			d.warn("Tightening the egress allow-list restricts the server's outbound fetches (JWKS, forward-auth, discovery). The new policy takes effect on restart.")
		}
	}
	d.cover("egress.enabled")
	d.cover("egress.allow")
}

// diffGlobalAdmin compares the [admin] block. All changes are restart-required
// (the admin listener is built once at startup). The token value is never
// included in the diff output.
func diffGlobalAdmin(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Admin, after.Admin
	if b.Enabled == a.Enabled && b.Listen == a.Listen && b.Token == a.Token &&
		boolPtrEqDiff(b.Console, a.Console) &&
		b.HistoryKeep == a.HistoryKeep && b.HistoryDir == a.HistoryDir &&
		b.RateLimitReadPerMin == a.RateLimitReadPerMin &&
		b.RateLimitWritePerMin == a.RateLimitWritePerMin &&
		b.RateLimitApplyPerMin == a.RateLimitApplyPerMin &&
		b.MaxEventConns == a.MaxEventConns &&
		b.AuditLogFile == a.AuditLogFile &&
		b.AuditLogRotateMaxMB == a.AuditLogRotateMaxMB &&
		b.AuditLogRotateKeep == a.AuditLogRotateKeep &&
		b.PluginUploadDir == a.PluginUploadDir &&
		b.PluginUploadMaxSize == a.PluginUploadMaxSize &&
		boolPtrEqDiff(b.PluginUploadEnabled, a.PluginUploadEnabled) {
		d.cover("admin.enabled")
		d.cover("admin.listen")
		d.cover("admin.token")
		return
	}
	if b.Token != a.Token {
		d.mod(DiffEntry{Kind: "admin", Name: "global",
			Detail: "Rotate admin bearer token — restart required to apply (the new token takes effect on restart)",
		}, "admin token")
		d.warn("The admin token change is saved but takes effect only after restart; the old token remains valid until then.")
	}
	if b.Listen != a.Listen {
		d.mod(DiffEntry{Kind: "admin", Name: "global", Before: b.Listen, After: a.Listen, Detail: "Change admin listen address — restart required"}, "admin listen")
	}
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "admin", Name: "global", Detail: action + " admin server — restart required"}, "admin enabled")
	}
	// Summarise remaining admin-block changes without exposing values.
	if !boolPtrEqDiff(b.Console, a.Console) ||
		b.HistoryKeep != a.HistoryKeep || b.HistoryDir != a.HistoryDir ||
		b.RateLimitReadPerMin != a.RateLimitReadPerMin ||
		b.RateLimitWritePerMin != a.RateLimitWritePerMin ||
		b.RateLimitApplyPerMin != a.RateLimitApplyPerMin ||
		b.MaxEventConns != a.MaxEventConns ||
		b.AuditLogFile != a.AuditLogFile ||
		b.AuditLogRotateMaxMB != a.AuditLogRotateMaxMB ||
		b.AuditLogRotateKeep != a.AuditLogRotateKeep ||
		b.PluginUploadDir != a.PluginUploadDir ||
		b.PluginUploadMaxSize != a.PluginUploadMaxSize ||
		!boolPtrEqDiff(b.PluginUploadEnabled, a.PluginUploadEnabled) {
		d.mod(DiffEntry{Kind: "admin", Name: "global", Detail: "Change admin server settings (console, history, rate limits, audit log, plugin upload) — restart required"}, "admin settings")
	}
	d.cover("admin.enabled")
	d.cover("admin.listen")
	d.cover("admin.token")
}

// boolPtrEqDiff reports whether two *bool values are semantically equal for
// diff purposes (mirrors the restart-check helper without the server-package dep).
func boolPtrEqDiff(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// diffGlobalRBAC compares the [admin.rbac] block. It reports only role,
// principal, and token-ID-level structure: which roles/principals were added,
// removed, or changed, whether RBAC is enabled, and whether a credential was
// rotated. It NEVER emits a plaintext token, a token digest, or any other
// secret — only names, roles, counts, disabled/expiry state, and the fact that
// a credential changed. Enabling/disabling RBAC is restart-required (the auth
// wiring is chosen at startup); the policy contents are hot-reloadable via
// atomic swap on a successful apply.
func diffGlobalRBAC(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Admin.RBAC, after.Admin.RBAC

	// Every RBAC lifecycle registry path is represented here, so mark them all
	// covered up front and let the completeness pass skip them regardless of
	// which specific sub-change (or none) is emitted below.
	d.cover("admin.rbac.enabled")
	d.cover("admin.rbac.default_role")
	d.cover("admin.rbac.principals.*")
	d.cover("admin.rbac.roles.*")

	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "rbac", Name: "admin", Detail: action + " admin RBAC — restart required to change the authentication mode"}, "rbac enabled")
		if a.Enabled {
			d.warn("Enabling RBAC replaces shared-token access with named principals; ensure at least one enabled principal holds admin:manage before applying, or you may lock yourself out of the admin API.")
		}
	}

	if b.DefaultRole != a.DefaultRole {
		d.mod(DiffEntry{Kind: "rbac", Name: "admin", Before: orNone(b.DefaultRole), After: orNone(a.DefaultRole), Detail: "Change RBAC default role for the legacy shared identity"}, "rbac default_role")
	}

	diffRBACRoles(b, a, d)
	diffRBACPrincipals(b, a, d)

	// Whole-config warnings evaluated against the candidate as a whole.
	if a.Enabled && !rbacHasAdminCapable(a, after.Admin.Token) {
		d.warn("This change would leave no enabled, admin-capable principal; applying it removes admin access. Keep at least one enabled principal (or the shared token) with admin:manage.")
	}
	if a.Enabled && strings.TrimSpace(after.Admin.Token) != "" {
		d.warn("A legacy shared admin token is still configured while RBAC is enabled; the shared credential stays valid alongside named principals. Remove admin.token to fully migrate to per-principal credentials.")
	}
}

// diffRBACRoles reports custom-role additions, removals, and permission changes
// between two RBAC configs. Permission strings are non-secret catalog values;
// the diff summarizes them by count so a role change is legible without listing
// every grant.
func diffRBACRoles(b, a config.AdminRBACConfig, d *ConfigDiff) {
	bRoles := rbacRoleMap(b.Roles)
	aRoles := rbacRoleMap(a.Roles)
	for _, name := range sortedKeys(aRoles) {
		ar := aRoles[name]
		br, ok := bRoles[name]
		if !ok {
			d.add(DiffEntry{Kind: "rbac_role", Name: name, After: rbacPermCount(ar.Permissions), Detail: "Add RBAC role " + name}, "rbac role "+name)
			continue
		}
		if !rbacStringSetEqual(br.Permissions, ar.Permissions) {
			d.mod(DiffEntry{Kind: "rbac_role", Name: name, Before: rbacPermCount(br.Permissions), After: rbacPermCount(ar.Permissions), Detail: "Change permissions for RBAC role " + name}, "rbac role "+name+" permissions")
		}
	}
	for _, name := range sortedKeys(bRoles) {
		if _, ok := aRoles[name]; !ok {
			d.del(DiffEntry{Kind: "rbac_role", Name: name, Before: rbacPermCount(bRoles[name].Permissions), Detail: "Remove RBAC role " + name}, "rbac role "+name)
		}
	}
}

// diffRBACPrincipals reports principal additions, removals, role changes,
// disabled/expiry transitions, and credential rotation between two RBAC configs.
// It never emits token values or hashes: a rotation is reported as a fact, not a
// value.
func diffRBACPrincipals(b, a config.AdminRBACConfig, d *ConfigDiff) {
	bP := rbacPrincipalMap(b.Principals)
	aP := rbacPrincipalMap(a.Principals)
	for _, name := range sortedKeys(aP) {
		ap := aP[name]
		bp, ok := bP[name]
		if !ok {
			d.add(DiffEntry{Kind: "rbac_principal", Name: name, After: "role " + ap.Role, Detail: "Add RBAC principal " + name}, "rbac principal "+name)
			continue
		}
		if bp.Role != ap.Role {
			d.mod(DiffEntry{Kind: "rbac_principal", Name: name, Before: "role " + bp.Role, After: "role " + ap.Role, Detail: "Change role for RBAC principal " + name}, "rbac principal "+name+" role")
		}
		if bp.Disabled != ap.Disabled {
			action := "Enable"
			if ap.Disabled {
				action = "Disable"
			}
			d.mod(DiffEntry{Kind: "rbac_principal", Name: name, Detail: action + " RBAC principal " + name}, "rbac principal "+name+" disabled")
		}
		if !bp.ExpiresAt.Equal(ap.ExpiresAt) {
			d.mod(DiffEntry{Kind: "rbac_principal", Name: name, Before: rbacExpiryLabel(bp.ExpiresAt), After: rbacExpiryLabel(ap.ExpiresAt), Detail: "Change expiry for RBAC principal " + name}, "rbac principal "+name+" expiry")
		}
		if bp.Token != ap.Token {
			d.mod(DiffEntry{Kind: "rbac_principal", Name: name, Detail: "Rotate credential for RBAC principal " + name + " (token value not shown)"}, "rbac principal "+name+" token")
		}
	}
	for _, name := range sortedKeys(bP) {
		if _, ok := aP[name]; !ok {
			d.del(DiffEntry{Kind: "rbac_principal", Name: name, Before: "role " + bP[name].Role, Detail: "Remove RBAC principal " + name}, "rbac principal "+name)
		}
	}
}

// rbacRoleMap indexes custom roles by name for diffing.
func rbacRoleMap(roles []config.AdminRole) map[string]config.AdminRole {
	out := make(map[string]config.AdminRole, len(roles))
	for _, r := range roles {
		out[r.Name] = r
	}
	return out
}

// rbacPrincipalMap indexes principals by name for diffing.
func rbacPrincipalMap(principals []config.AdminPrincipal) map[string]config.AdminPrincipal {
	out := make(map[string]config.AdminPrincipal, len(principals))
	for _, p := range principals {
		out[p.Name] = p
	}
	return out
}

// rbacPermCount renders a permission-set size for a role diff entry.
func rbacPermCount(perms []string) string {
	if len(perms) == 1 {
		return "1 permission"
	}
	return fmt.Sprintf("%d permissions", len(perms))
}

// rbacExpiryLabel renders a principal expiry for a diff entry without leaking
// anything sensitive: "never" for the zero time, otherwise an RFC3339 UTC stamp.
func rbacExpiryLabel(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return t.UTC().Format(time.RFC3339)
}

// rbacStringSetEqual reports order-independent set equality of two permission
// lists (duplicates collapsed), so reordering a role's permissions is not
// flagged as a change.
func rbacStringSetEqual(x, y []string) bool {
	xs := make(map[string]struct{}, len(x))
	for _, s := range x {
		xs[s] = struct{}{}
	}
	ys := make(map[string]struct{}, len(y))
	for _, s := range y {
		ys[s] = struct{}{}
	}
	if len(xs) != len(ys) {
		return false
	}
	for s := range xs {
		if _, ok := ys[s]; !ok {
			return false
		}
	}
	return true
}

// rbacHasAdminCapable reports whether the candidate RBAC config retains at least
// one enabled, non-expired identity that grants admin:manage — either the
// synthetic legacy shared identity (when a shared token is configured) or a
// named principal. It mirrors the invariant rbac.Build enforces so the diff can
// warn before the operator applies a lockout.
func rbacHasAdminCapable(cfg config.AdminRBACConfig, legacyToken string) bool {
	if strings.TrimSpace(legacyToken) != "" {
		role := cfg.DefaultRole
		if role == "" {
			role = rbac.RoleAdmin
		}
		if roleGrantsAdminManage(cfg, role) {
			return true
		}
	}
	for _, p := range cfg.Principals {
		if p.Disabled || principalExpired(p) {
			continue
		}
		if roleGrantsAdminManage(cfg, p.Role) {
			return true
		}
	}
	return false
}

// diffGlobalMetrics compares the [observability.metrics] block. Changes are
// restart-required (the Prometheus registry is built once at startup).
func diffGlobalMetrics(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Observability.Metrics, after.Observability.Metrics
	if b == a {
		d.cover("observability.metrics.host_label")
		return
	}
	if b.HostLabel != a.HostLabel {
		action := "Enable"
		if !a.HostLabel {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "metrics", Name: "global", Detail: fmt.Sprintf("%s host_label on metrics — restart required to apply", action)}, "metrics host_label")
		if a.HostLabel {
			d.warn("Enabling host_label adds the request Host as a Prometheus label; unbounded host cardinality can exhaust memory — only enable when the host set is bounded.")
		}
	}
	d.cover("observability.metrics.host_label")
}
