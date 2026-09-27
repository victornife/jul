// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// WAFEffectivePolicy is the WAF policy the serving handler generation
// actually enforces (#440), built from the compiled engines when that
// generation was prepared — never reconstructed from the on-disk config, so
// a staged, failed or not-yet-reloaded candidate cannot appear active. It
// carries no rule file path, rule text or rule message.
type WAFEffectivePolicy struct {
	// Compiled reports whether this binary links the WAF engine at all.
	Compiled bool `json:"compiled"`
	// EmbeddedCRSVersion is the OWASP CRS version compiled into the binary,
	// read from the embedded rule set; "" without the waf build tag.
	EmbeddedCRSVersion string `json:"embedded_crs_version,omitempty"`
	// EngineVersion is the linked Coraza version from build info, when known.
	EngineVersion string `json:"engine_version,omitempty"`
	// Generation is the serving handler generation; CompiledAt is when its
	// policies were compiled (RFC 3339, UTC).
	Generation uint64 `json:"generation"`
	CompiledAt string `json:"compiled_at,omitempty"`
	// Route coverage of the serving generation.
	InheritingRoutes       int `json:"inheriting_routes"`
	OverrideRoutes         int `json:"override_routes"`
	DisabledOverrideRoutes int `json:"disabled_override_routes"`
	UnprotectedRoutes      int `json:"unprotected_routes"`
	// GlobalEnabled is the serving generation's [waf].enabled. Global is the
	// compiled global policy, present only when at least one route serves it.
	GlobalEnabled bool              `json:"global_enabled"`
	Global        *WAFPolicySummary `json:"global,omitempty"`
	// Overrides lists routes whose own [waf] block replaces the global policy.
	Overrides []WAFRouteOverride `json:"overrides,omitempty"`
}

// WAFPolicySummary is one compiled policy's bounded effective settings.
type WAFPolicySummary struct {
	Mode                   string `json:"mode"`
	BlockStatus            int    `json:"block_status"`
	CRSEnabled             bool   `json:"crs_enabled"`
	CRSVersion             string `json:"crs_version,omitempty"`
	Paranoia               int    `json:"paranoia,omitempty"`
	ParanoiaDefault        bool   `json:"paranoia_default,omitempty"`
	RequestBodyLimitBytes  int64  `json:"request_body_limit_bytes,omitempty"`
	ResponseBodyInspection bool   `json:"response_body_inspection"`
	RuleFilesConfigured    int    `json:"rule_files_configured"`
	// ExternalFiles/ExternalDigest identify the exact bytes read from disk at
	// compile time: same path with changed content yields a new digest.
	ExternalFiles  int           `json:"external_files"`
	ExternalDigest string        `json:"external_digest,omitempty"`
	InlineRules    bool          `json:"inline_rules"`
	Rules          WAFRuleCounts `json:"rules"`
}

// WAFRuleCounts classifies compiled rules by bounded source class.
type WAFRuleCounts struct {
	Total     int `json:"total"`
	Embedded  int `json:"embedded"`
	External  int `json:"external"`
	Inline    int `json:"inline"`
	Generated int `json:"generated"`
}

// WAFRouteOverride identifies a route by its configured listener, server
// names and match — the same identity the Security panel already shows —
// and its compiled override policy (nil when the override disables the WAF).
type WAFRouteOverride struct {
	Listen      string            `json:"listen"`
	ServerNames []string          `json:"server_names,omitempty"`
	MatchType   string            `json:"match_type,omitempty"`
	Path        string            `json:"path,omitempty"`
	Enabled     bool              `json:"enabled"`
	Policy      *WAFPolicySummary `json:"policy,omitempty"`
}
