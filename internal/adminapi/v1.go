// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"time"

	"jul/internal/buildcaps"
)

// APIVersion is the external namespace these DTOs belong to. It is reported in
// every response that describes the server itself, so a client can detect a
// namespace it does not understand without parsing the URL it called.
const APIVersion = "v1"

// Timestamp renders t as RFC 3339 with a Z offset (ADR 0019 §24a). The zero
// time renders as the empty string so an absent timestamp is omitted rather
// than published as year 1.
func Timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// LedgerRetention publishes the terminal ledger's bounds as a client contract
// (ADR 0019 §24a, §30). A client that polls an apply result or relies on
// idempotent replay must know both the bound and the boundary.
//
// These are **minimum guarantees, not caps**. A terminal record is evicted only
// when it is both past the age bound and over the count bound, so naming the
// fields max/ttl — as an earlier draft did — would advertise a ceiling and an
// expiry the implementation does not enforce, and a client could wrongly
// conclude a record was gone.
type LedgerRetention struct {
	MinTerminalRecords int `json:"min_terminal_records"`
	// MinAgeSeconds is in seconds rather than the `_ms` suffix §24a uses for
	// measured durations: this is a published policy bound, not a measurement,
	// and ADR 0019 §30 names the field explicitly.
	MinAgeSeconds int `json:"min_age_seconds"`
	// Policy is the eviction rule. "evict_after_both" means a record is
	// discarded only once it exceeds both bounds.
	Policy string `json:"policy"`
}

// AuthorityState is the configuration-authority projection every state-reporting
// response carries (ADR 0019 §9, §16).
type AuthorityState struct {
	// ConfigAuthority is "managed" or "file_owned".
	ConfigAuthority string `json:"config_authority"`
	// ConfigAuthoritySource is "explicit", "default" or "no_config_file".
	ConfigAuthoritySource string `json:"config_authority_source"`
	// ConfigState is §16's closed enum. It is computed once server-side,
	// emitted and never accepted as input.
	ConfigState string `json:"config_state,omitempty"`
	// ConfigInconsistentReason is set only when ConfigState is
	// managed_inconsistent, which is not actionable without it.
	ConfigInconsistentReason string `json:"config_inconsistent_reason,omitempty"`
}

// DriftState reports an unresolved external edit in managed authority
// (ADR 0019 §12, §13). It carries versions and a bounded digest — never the
// bytes, never a path.
type DriftState struct {
	Detected        bool   `json:"detected"`
	DetectedAt      string `json:"detected_at,omitempty"`
	BaselineVersion string `json:"baseline_version,omitempty"`
	DiskVersion     string `json:"disk_version,omitempty"`
	// DiskRawDigest is the on-disk raw sha256 truncated to the same width
	// CanonicalVersion uses: bounded evidence that the file is or is not a
	// byte-for-byte match for the baseline.
	DiskRawDigest string `json:"disk_raw_digest,omitempty"`
	// DiskParseError is a bounded error class, set only when the on-disk
	// content does not parse. It never carries configuration content.
	DiskParseError string `json:"disk_parse_error,omitempty"`
}

// PendingRestartState reports a staged planned restart.
type PendingRestartState struct {
	Pending bool `json:"pending"`
	// State is the authoritative enum: "none", "managed_staged",
	// "external_divergence" or "inconsistent".
	State            string   `json:"state,omitempty"`
	StagedAt         string   `json:"staged_at,omitempty"`
	StagedVersion    string   `json:"staged_version,omitempty"`
	PersistedVersion string   `json:"persisted_version,omitempty"`
	ServingVersion   string   `json:"serving_version,omitempty"`
	Subsystems       []string `json:"subsystems,omitempty"`
	DiscardAvailable bool     `json:"discard_available"`
}

// StatusResponse is GET /api/v1/status: the control-plane state of one server
// (ADR 0019 §24). It reports what is serving, what is desired, what is
// persisted, who owns the configuration, and where the last transaction got to.
type StatusResponse struct {
	APIVersion string `json:"api_version"`

	// Ready is the data-plane readiness answer — the same one /readyz gives.
	// It is reported alongside the control-plane fields precisely so the two
	// are not confused: drift and a pending restart are control-plane
	// conditions and never make a serving data plane unready (ADR 0019's
	// observability rule).
	Ready bool `json:"ready"`

	// ServingVersion is the canonical version of the live runtime.
	ServingVersion string `json:"serving_version,omitempty"`
	// PersistedVersion is the canonical version of the configuration on disk.
	PersistedVersion string `json:"persisted_version,omitempty"`

	AuthorityState
	Drift          DriftState          `json:"drift"`
	PendingRestart PendingRestartState `json:"pending_restart"`

	// LastApply summarises the most recent managed apply transaction, or is
	// null when none has finalized since this boot.
	LastApply *ApplySummary `json:"last_apply"`

	// BootID is this process's apply-instance identity. A changed BootID means
	// the terminal ledger and every idempotency binding were discarded, so a
	// client's replay window is gone (ADR 0019 §27.2).
	BootID          string          `json:"boot_id"`
	LedgerRetention LedgerRetention `json:"ledger_retention"`
}

// ApplySummary is the bounded projection of a managed apply transaction. It
// carries no actor, no source address and no configuration content; the audit
// API remains the only surface for attribution.
type ApplySummary struct {
	ApplyID     string `json:"apply_id"`
	State       string `json:"state"`
	Operation   string `json:"operation,omitempty"`
	Outcome     string `json:"outcome,omitempty"`
	Mode        string `json:"mode,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	// Degraded is present and empty on a clean success, so a script can test it
	// unconditionally rather than checking whether the key exists
	// (ADR 0019 §33.2).
	Degraded []Degradation `json:"degraded"`
}

// Degradation is one member of ADR 0019 §33.2's closed set. A degradation never
// upgrades or downgrades a terminal outcome: "did the change take effect" and
// "is anything about this operation unhealthy" are independent questions.
type Degradation struct {
	// Kind is one of baseline_error, staging_error, staging_incomplete,
	// drift_after_adopt, drift_unknown, history_error, finalization_error.
	Kind string `json:"kind"`
	// Message is a bounded human string carrying an error class — never a path,
	// a digest or configuration content.
	Message string `json:"message,omitempty"`
}

// EndpointAvailability describes one external operation this build serves, so a
// client never has to infer capability from an error (ADR 0019 §30).
type EndpointAvailability struct {
	Path    string   `json:"path"`
	Methods []string `json:"methods"`
	// Available is false when the operation is absent from this build. An
	// absent operation answers 501 not_implemented naming RequiredCapability,
	// not 404.
	Available bool `json:"available"`
	// RequiredCapability names the build flag an unavailable operation needs.
	RequiredCapability string `json:"required_capability,omitempty"`
	// Stability is "external", "public" or "deprecated". Internal routes are
	// never listed.
	Stability string `json:"stability"`
	// Permissions are the permissions that admit a caller; holding any one of
	// them suffices. Empty for a public operation.
	Permissions []string `json:"permissions,omitempty"`
	// SunsetOn is the RFC 3339 date after which a deprecated operation may be
	// removed.
	SunsetOn string `json:"sunset_on,omitempty"`
}

// CapabilitiesResponse is GET /api/v1/capabilities (ADR 0019 §30).
//
// The configuration schema version is build-independent on purpose: a lean
// binary reports the same schema as a fully tagged one, because a field
// belonging to an uncompiled feature is present and annotated rather than
// missing. API surface availability is a different question, answered by
// Endpoints.
type CapabilitiesResponse struct {
	APIVersion          string          `json:"api_version"`
	ConfigSchemaVersion int             `json:"config_schema_version"`
	Build               buildcaps.Flags `json:"build"`

	Endpoints []EndpointAvailability `json:"endpoints"`

	AuthorityState

	BootID          string          `json:"boot_id"`
	LedgerRetention LedgerRetention `json:"ledger_retention"`
}
