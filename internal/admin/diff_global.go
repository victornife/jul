// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the global-config, plugin, and stream diff functions.
// Server/location/upstream diff functions and utility helpers live in
// diff_helpers.go.

import (
	"fmt"
	"strings"

	"jul/internal/config"
)

func diffGlobalCache(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Cache, after.Cache
	if b.Enabled != a.Enabled {
		action := "Enable"
		if !a.Enabled {
			action = "Disable"
		}
		d.mod(DiffEntry{Kind: "cache", Name: "global", Detail: action + " the response cache"}, "cache")
		return
	}
	if !a.Enabled {
		return
	}
	if b.MemoryMaxSize != a.MemoryMaxSize {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: sizeStr(b.MemoryMaxSize), After: sizeStr(a.MemoryMaxSize), Detail: "Change cache memory size"}, "cache memory")
	}
	if b.DiskPath != a.DiskPath {
		d.mod(DiffEntry{Kind: "cache", Name: "global", Before: orNone(b.DiskPath), After: orNone(a.DiskPath), Detail: "Change cache disk path"}, "cache disk")
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
		return
	}
	if !a.IsEnabled() {
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
		return
	}
	if !a.Enabled {
		return
	}
	if b.Key != a.Key || b.Rate != a.Rate || b.Burst != a.Burst {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: rlStr(&b), After: rlStr(&a), Detail: "Change global rate-limit policy"}, "rate limit policy")
	}
	if b.MaxConns != a.MaxConns {
		d.mod(DiffEntry{Kind: "rate_limit", Name: "global", Before: fmt.Sprintf("%d", b.MaxConns), After: fmt.Sprintf("%d", a.MaxConns), Detail: "Change max concurrent connections"}, "rate limit max conns")
	}
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
		return
	}
	if !a.Enabled {
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
		return
	}
	if !a.Enabled {
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
	if !stringMapEqual(trimSNIRoutes(b.SNIRoutes), trimSNIRoutes(a.SNIRoutes)) {
		d.mod(DiffEntry{Kind: "stream", Name: key, Before: fmt.Sprintf("%d route%s", len(b.SNIRoutes), plural(len(b.SNIRoutes))), After: fmt.Sprintf("%d route%s", len(a.SNIRoutes), plural(len(a.SNIRoutes))), Detail: "Change SNI routes for stream " + key}, "stream "+key+" sni_routes")
	}
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
}

// diffGlobalEgress compares the [egress] allow-list. Changes are restart-required
// (the dial policy is built once at startup) and are surfaced in the diff so
// the operator can review what changed before the 409 rejection.
func diffGlobalEgress(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Egress, after.Egress
	if b.Enabled == a.Enabled && strings.Join(b.Allow, ",") == strings.Join(a.Allow, ",") {
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
}

// diffGlobalAdmin compares the [admin] block. All changes are restart-required
// (the admin listener is built once at startup). The token value is never
// included in the diff output.
func diffGlobalAdmin(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Admin, after.Admin
	if b.Enabled == a.Enabled && b.Listen == a.Listen && b.Token == a.Token &&
		b.HistoryKeep == a.HistoryKeep && b.HistoryDir == a.HistoryDir &&
		b.RateLimitReadPerMin == a.RateLimitReadPerMin &&
		b.RateLimitWritePerMin == a.RateLimitWritePerMin &&
		b.RateLimitApplyPerMin == a.RateLimitApplyPerMin &&
		b.MaxEventConns == a.MaxEventConns &&
		b.AuditLogFile == a.AuditLogFile &&
		b.PluginUploadDir == a.PluginUploadDir &&
		b.PluginUploadMaxSize == a.PluginUploadMaxSize &&
		boolPtrEqDiff(b.PluginUploadEnabled, a.PluginUploadEnabled) {
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
	if b.HistoryKeep != a.HistoryKeep || b.HistoryDir != a.HistoryDir ||
		b.RateLimitReadPerMin != a.RateLimitReadPerMin ||
		b.RateLimitWritePerMin != a.RateLimitWritePerMin ||
		b.RateLimitApplyPerMin != a.RateLimitApplyPerMin ||
		b.MaxEventConns != a.MaxEventConns ||
		b.AuditLogFile != a.AuditLogFile ||
		b.PluginUploadDir != a.PluginUploadDir ||
		b.PluginUploadMaxSize != a.PluginUploadMaxSize ||
		!boolPtrEqDiff(b.PluginUploadEnabled, a.PluginUploadEnabled) {
		d.mod(DiffEntry{Kind: "admin", Name: "global", Detail: "Change admin server settings (history, rate limits, audit log, plugin upload) — restart required"}, "admin settings")
	}
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

// diffGlobalMetrics compares the [observability.metrics] block. Changes are
// restart-required (the Prometheus registry is built once at startup).
func diffGlobalMetrics(before, after *config.Config, d *ConfigDiff) {
	b, a := before.Observability.Metrics, after.Observability.Metrics
	if b == a {
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
}
