// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"slices"

	"jul/internal/config"
)

// CacheRestartRequired reports whether moving from old to next changes the
// response-cache configuration. The cache is built once at startup by
// cache.New and persists across reloads (so counter and LRU state survive
// config edits). A changed [cache] block would therefore be persisted to disk
// and appear in Console projections while the old capacity, disk path, and
// policy remain active until a restart. The apply path rejects such a change
// with restart_required rather than accepting it silently.
//
// CacheConfig contains only comparable fields (Size is an int64 alias, Duration
// is comparable), so a direct struct comparison is used.
func CacheRestartRequired(old, next *config.Config) (string, bool) {
	if old.Cache == next.Cache {
		return "", false
	}
	return "cache settings changed; the response cache (capacity, disk path, TTL policy) is built once at startup and takes effect on restart", true
}

// EgressRestartRequired reports whether moving from old to next changes the
// egress allow-list configuration. The outbound dial policy is built once at
// startup by egress.New and captures the allow-list as an immutable set. A
// changed [egress] block — including tightening the allow-list — would be
// persisted while the running process continues to enforce the previous policy,
// which is especially dangerous for security-motivated changes (e.g. restricting
// which JWKS or forward-auth endpoints the server may contact). The apply path
// rejects such a change with restart_required so the operator can trust that a
// saved egress policy is the one being enforced.
func EgressRestartRequired(old, next *config.Config) (string, bool) {
	if egressEqual(old.Egress, next.Egress) {
		return "", false
	}
	return "egress allow-list changed; the outbound dial policy is built once at startup and takes effect on restart", true
}

// egressEqual reports whether two EgressConfig values are identical. Allow is a
// slice, so it is compared element-wise with slices.Equal; the Enabled flag uses
// direct equality.
func egressEqual(a, b config.EgressConfig) bool {
	return a.Enabled == b.Enabled && slices.Equal(a.Allow, b.Allow)
}

// AdminRestartRequired reports whether moving from old to next changes the admin
// server configuration. The admin listener is created once at startup by
// admin.New, which copies the AdminConfig by value. Changes to the listen
// address, bearer token, rate-limit settings, history directory, plugin-upload
// policy, or audit-log path are persisted to disk while the running admin server
// keeps using the startup-time values. Token rotation in particular must not
// silently fail: an operator who rotates the token via the Console should restart
// the process so the new token takes effect, rather than believing the rotation
// is live while the old token still grants access.
func AdminRestartRequired(old, next *config.Config) (string, bool) {
	if adminEqual(old.Admin, next.Admin) {
		return "", false
	}
	return "admin server settings changed (listen address, token, rate limits, history, plugin upload, or audit log); the admin listener is built once at startup and takes effect on restart", true
}

// adminEqual reports whether two AdminConfig values are identical. Pointer
// fields (Console, PluginUploadEnabled) are compared by value, not pointer
// identity, using boolPtrEq.
func adminEqual(a, b config.AdminConfig) bool {
	return a.Enabled == b.Enabled &&
		a.Listen == b.Listen &&
		a.Token == b.Token &&
		boolPtrEq(a.Console, b.Console) &&
		a.HistoryDir == b.HistoryDir &&
		a.HistoryKeep == b.HistoryKeep &&
		a.RateLimitReadPerMin == b.RateLimitReadPerMin &&
		a.RateLimitWritePerMin == b.RateLimitWritePerMin &&
		a.RateLimitApplyPerMin == b.RateLimitApplyPerMin &&
		a.MaxEventConns == b.MaxEventConns &&
		a.AuditLogFile == b.AuditLogFile &&
		a.AuditLogRotateMaxMB == b.AuditLogRotateMaxMB &&
		a.AuditLogRotateKeep == b.AuditLogRotateKeep &&
		a.PluginUploadDir == b.PluginUploadDir &&
		a.PluginUploadMaxSize == b.PluginUploadMaxSize &&
		boolPtrEq(a.PluginUploadEnabled, b.PluginUploadEnabled)
}

// boolPtrEq reports whether two *bool values are semantically equal: both nil,
// or both non-nil and pointing to the same bool value.
func boolPtrEq(a, b *bool) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

// MetricsRestartRequired reports whether moving from old to next changes the
// metrics configuration. The Prometheus metrics registry and its host-label
// setting are built once at startup by observability.NewMetrics. Changing
// [observability.metrics].host_label would persist while the running registry
// continues to apply the startup-time label, so the apply path rejects it with
// restart_required.
func MetricsRestartRequired(old, next *config.Config) (string, bool) {
	if old.Observability.Metrics == next.Observability.Metrics {
		return "", false
	}
	return "metrics settings changed; the Prometheus registry is built once at startup and takes effect on restart", true
}
