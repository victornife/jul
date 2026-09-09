// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// ConfigApplyResponse is the bounded external result of a configuration
// mutation. It deliberately does not expose the Console's ConfigApplyResult or
// server.ReloadResult: those internal types carry implementation-only fields
// (including unbounded maps) that are not part of the v1 compatibility
// contract.
//
// A 202 response is non-terminal and carries the apply_id to poll through
// GET /api/v1/config/applies/{apply_id}. A 200 response is terminal. Clients
// should branch on Terminal/Outcome rather than inferring completion from the
// status code alone.
type ConfigApplyResponse struct {
	APIVersion string `json:"api_version"`
	ApplyID    string `json:"apply_id,omitempty"`
	State      string `json:"state"`
	Terminal   bool   `json:"terminal"`
	OK         bool   `json:"ok"`
	Mode       string `json:"mode,omitempty"`
	Outcome    string `json:"outcome,omitempty"`

	// IdempotentReplay is true only when this terminal result was returned from
	// the retained idempotency binding rather than by executing the mutation.
	IdempotentReplay bool `json:"idempotent_replay,omitempty"`

	PersistedVersion string `json:"persisted_version,omitempty"`
	DesiredVersion   string `json:"desired_version,omitempty"`
	ServingVersion   string `json:"serving_version,omitempty"`
	ConfigState      string `json:"config_state,omitempty"`
	Origin           string `json:"origin,omitempty"`

	PendingRestart  *PendingRestartState `json:"pending_restart,omitempty"`
	RestartRequired bool                 `json:"restart_required,omitempty"`
	CanStage        bool                 `json:"can_stage,omitempty"`

	Restored      bool   `json:"restored"`
	RestoreError  string `json:"restore_error,omitempty"`
	TimedOutPhase string `json:"timed_out_phase,omitempty"`

	// Degraded is always present. Its Kind values are the bounded ADR 0019
	// degradation catalogue; Message is human-oriented and never used as the
	// machine discriminator.
	Degraded []Degradation `json:"degraded"`

	// BootID scopes apply identities and idempotency bindings to this process.
	BootID string `json:"boot_id"`
}
