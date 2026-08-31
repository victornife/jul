// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// ExportGlobal is the process-wide settings block of an export.
//
// It is an **allow-list**: a field appears here because it was reviewed and
// published, not because it exists in the configuration. The inverse — marshal
// everything and strip what looks sensitive — fails open, because every field
// added to the schema afterwards is exported until someone remembers to strip
// it, and the failure is silent.
type ExportGlobal struct {
	LogLevel  string `json:"log_level,omitempty"`
	LogFormat string `json:"log_format,omitempty"`
	// ConfigAuthority is who owns the configuration file: managed or file.
	ConfigAuthority     string `json:"config_authority,omitempty"`
	ShutdownTimeoutMS   int64  `json:"shutdown_timeout_ms"`
	ReloadTimeoutMS     int64  `json:"reload_timeout_ms"`
	CompressionEnabled  bool   `json:"compression_enabled"`
	CacheEnabled        bool   `json:"cache_enabled"`
	RateLimitEnabled    bool   `json:"rate_limit_enabled"`
	WAFEnabled          bool   `json:"waf_enabled"`
	TracingEnabled      bool   `json:"tracing_enabled"`
	AccessLogConfigured bool   `json:"access_log_configured"`
	ErrorLogConfigured  bool   `json:"error_log_configured"`
}

// ConfigExportResponse is GET /api/v1/config/export: the whole configuration as
// a redacted structured projection, read at one revision.
//
// It exists because reading the four collections separately can straddle a
// reload and produce a document that never existed. Everything here is captured
// from a single read, so `base_version` describes all of it.
//
// Its safety is **structural, not editorial**: every field is either one of the
// DTOs already published under its own operation, or a reviewed scalar in
// ExportGlobal. Nothing is reflected from the configuration wholesale, so a new
// configuration field cannot appear here by default — it stays absent until
// someone publishes it deliberately.
//
// This is the redacted projection and the only export in the external contract.
// Exact bytes stay on the internal route: raw export is local-only in v1
// (ADR 0019 §24), because a configuration file may hold literal secret values.
type ConfigExportResponse struct {
	APIVersion  string `json:"api_version"`
	BaseVersion string `json:"base_version"`
	// Redacted is always true. It is stated rather than implied so a client
	// cannot mistake this document for the file, and so the field is already
	// there if a raw export is ever added.
	Redacted bool `json:"redacted"`
	// SecretRefCount is how many secret references the configuration resolves.
	// The count is published; no reference and no resolved value ever is. A
	// change in the count is visible without the values being readable.
	SecretRefCount int `json:"secret_ref_count"`

	Global    ExportGlobal `json:"global"`
	Listeners []Listener   `json:"listeners"`
	Routes    []Route      `json:"routes"`
	Upstreams []Upstream   `json:"upstreams"`
	Streams   []Stream     `json:"streams"`
}

// DiffEntry is one change in a configuration diff.
//
// Before and After hold the changed values, so a comparator that put a
// credential in either would put it on the wire. The comparators report a
// credential change as a change — "rotate", with no values — and a test asserts
// that property rather than trusting it.
type DiffEntry struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Detail string `json:"detail,omitempty"`
	// LifecycleClass is hot_reload, restart_required or new_listener_only when
	// known: whether applying this change needs a restart.
	LifecycleClass string `json:"lifecycle_class,omitempty"`
}

// HistoryDiffResponse is GET /api/v1/config/history/{id}/diff: what rolling
// back to a stored revision would change.
//
// The baseline is the **persisted** configuration, not the live runtime. The
// two are identical except while a staged restart or external divergence is
// pending, and a hot rollback is already refused in that state.
type HistoryDiffResponse struct {
	APIVersion string `json:"api_version"`
	// BaseVersion is the revision the diff was computed against, derived
	// identically to the rollback's own concurrency check. A client passes it
	// back so the rollback is bound to the configuration the operator reviewed,
	// and is refused if the persisted configuration moved underneath them.
	BaseVersion string `json:"base_version"`
	// HistoryID is the stored revision being compared against.
	HistoryID string `json:"history_id"`
	Summary   string `json:"summary"`
	// Affected names the areas that change, for a client that wants a headline
	// without walking every entry.
	Affected []string `json:"affected,omitempty"`
	// Warnings are consequences of applying the change that the entries alone
	// do not convey.
	Warnings      []string    `json:"warnings,omitempty"`
	Additions     []DiffEntry `json:"additions,omitempty"`
	Removals      []DiffEntry `json:"removals,omitempty"`
	Modifications []DiffEntry `json:"modifications,omitempty"`
}
