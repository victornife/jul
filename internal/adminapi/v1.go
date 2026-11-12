// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"time"

	"jul/internal/buildcaps"
)

// APIVersion is the external namespace these DTOs belong to.
const APIVersion = "v1"

// Timestamp renders t as RFC 3339 with a Z offset (ADR 0019 §24a).
func Timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// LedgerRetention publishes the terminal ledger's bounds as a client contract.
type LedgerRetention struct {
	MinTerminalRecords int    `json:"min_terminal_records"`
	MinAgeSeconds      int    `json:"min_age_seconds"`
	Policy             string `json:"policy"`
}

// InspectionStatus reports the process inspection governor.
// RequestPathIntegration is "partial": plugin request paths are governed; WAF
// request charging and response-side accounting are not yet.
type InspectionStatus struct {
	Configured                      InspectionConfiguredLimits `json:"configured"`
	Effective                       InspectionEffectiveLimits  `json:"effective"`
	Usage                           InspectionUsage            `json:"usage"`
	ActiveLeases                    uint64                     `json:"active_leases"`
	ActiveEvaluations               uint64                     `json:"active_evaluations"`
	WaitingEvaluations              uint64                     `json:"waiting_evaluations"`
	Draining                        bool                       `json:"draining"`
	RequestPathIntegration          string                     `json:"request_path_integration"`
	BaseChargeBytes                 uint64                     `json:"base_charge_bytes"`
	BaseChargeScope                 string                     `json:"base_charge_scope"`
	PluginGuestMaxBytes             *uint64                    `json:"plugin_guest_max_bytes,omitempty"`
	PluginGuestLiveInstances        *uint64                    `json:"plugin_guest_live_instances,omitempty"`
	PluginGuestMaxInstances         *uint64                    `json:"plugin_guest_max_instances,omitempty"`
	PluginGuestActualBytes          *uint64                    `json:"plugin_guest_actual_bytes,omitempty"`
	PluginGuestActualBytesState     string                     `json:"plugin_guest_actual_bytes_state"`
	EvaluationHeadroomEstimateBytes *uint64                    `json:"evaluation_headroom_estimate_bytes,omitempty"`
	EvaluationHeadroomEstimateState string                     `json:"evaluation_headroom_estimate_state"`
}

type InspectionPluginGuestCapacity struct {
	MaxPermittedBytes uint64
	LiveInstances     uint64
	MaxInstances      uint64
}

type InspectionConfiguredLimits struct {
	Mode                     string `json:"mode"`
	MaxBufferedBytes         uint64 `json:"max_buffered_bytes"`
	MaxResponseHeaderBytes   uint64 `json:"max_response_header_bytes"`
	MaxConcurrentEvaluations int    `json:"max_concurrent_evaluations"`
	EvaluationQueueTimeout   string `json:"evaluation_queue_timeout"`
	BodyTimeout              string `json:"body_timeout"`
}

type InspectionEffectiveLimits struct {
	Mode                     string `json:"mode"`
	MaxBufferedBytes         uint64 `json:"max_buffered_bytes"`
	MaxResponseHeaderBytes   uint64 `json:"max_response_header_bytes"`
	MaxConcurrentEvaluations int    `json:"max_concurrent_evaluations"`
	EvaluationQueueTimeout   string `json:"evaluation_queue_timeout"`
	BodyTimeout              string `json:"body_timeout"`
}

type InspectionUsage struct {
	BaseBytes     uint64 `json:"base_bytes"`
	BufferBytes   uint64 `json:"buffer_bytes"`
	WAFStateBytes uint64 `json:"waf_state_bytes"`
}

// AuthorityState is the configuration-authority projection every state-reporting response carries.
type AuthorityState struct {
	ConfigAuthority          string `json:"config_authority"`
	ConfigAuthoritySource    string `json:"config_authority_source"`
	ConfigState              string `json:"config_state,omitempty"`
	ConfigInconsistentReason string `json:"config_inconsistent_reason,omitempty"`
}

// DriftState reports an unresolved external edit in managed authority.
type DriftState struct {
	Detected        bool   `json:"detected"`
	DetectedAt      string `json:"detected_at,omitempty"`
	BaselineVersion string `json:"baseline_version,omitempty"`
	DiskVersion     string `json:"disk_version,omitempty"`
	DiskRawDigest   string `json:"disk_raw_digest,omitempty"`
	DiskParseError  string `json:"disk_parse_error,omitempty"`
}

// PendingRestartState reports a staged planned restart.
type PendingRestartState struct {
	VersionComparison string   `json:"version_comparison,omitempty"`
	Pending           bool     `json:"pending"`
	State             string   `json:"state,omitempty"`
	StagedAt          string   `json:"staged_at,omitempty"`
	StagedVersion     string   `json:"staged_version,omitempty"`
	PersistedVersion  string   `json:"persisted_version,omitempty"`
	ServingVersion    string   `json:"serving_version,omitempty"`
	Subsystems        []string `json:"subsystems,omitempty"`
	DiscardAvailable  bool     `json:"discard_available"`
}

// StatusResponse is GET /api/v1/status.
type StatusResponse struct {
	APIVersion       string `json:"api_version"`
	Ready            bool   `json:"ready"`
	ServingVersion   string `json:"serving_version,omitempty"`
	PersistedVersion string `json:"persisted_version,omitempty"`
	AuthorityState
	Drift           DriftState          `json:"drift"`
	PendingRestart  PendingRestartState `json:"pending_restart"`
	LastApply       *ApplySummary       `json:"last_apply"`
	BootID          string              `json:"boot_id"`
	LedgerRetention LedgerRetention     `json:"ledger_retention"`
	Inspection      *InspectionStatus   `json:"inspection,omitempty"`
}

// ApplySummary is the bounded projection of a managed apply transaction.
type ApplySummary struct {
	ApplyID     string        `json:"apply_id"`
	State       string        `json:"state"`
	Operation   string        `json:"operation,omitempty"`
	Outcome     ReloadOutcome `json:"outcome,omitempty"`
	Mode        string        `json:"mode,omitempty"`
	StartedAt   string        `json:"started_at,omitempty"`
	CompletedAt string        `json:"completed_at,omitempty"`
	Degraded    []Degradation `json:"degraded"`
}

// Degradation is one member of ADR 0019 §33.2's closed set.
type Degradation struct {
	Kind    string `json:"kind"`
	Message string `json:"message,omitempty"`
}

// EndpointAvailability describes one external operation this build serves.
type EndpointAvailability struct {
	Path               string   `json:"path"`
	Methods            []string `json:"methods"`
	Available          bool     `json:"available"`
	RequiredCapability string   `json:"required_capability,omitempty"`
	Stability          string   `json:"stability"`
	Permissions        []string `json:"permissions,omitempty"`
	RequiresConfigRaw  bool     `json:"requires_config_raw,omitempty"`
	SunsetOn           string   `json:"sunset_on,omitempty"`
}

// CapabilitiesResponse is GET /api/v1/capabilities (ADR 0019 §30).
type CapabilitiesResponse struct {
	APIVersion          string                 `json:"api_version"`
	ConfigSchemaVersion int                    `json:"config_schema_version"`
	Build               buildcaps.Flags        `json:"build"`
	Endpoints           []EndpointAvailability `json:"endpoints"`
	// ExitCodes is the exact ADR 0019 §33 table consumed by downstream automation.
	// The local `jul capabilities` command uses the same source of truth.
	ExitCodes []ExitCode `json:"exit_codes"`
	AuthorityState
	BootID          string          `json:"boot_id"`
	LedgerRetention LedgerRetention `json:"ledger_retention"`
}
