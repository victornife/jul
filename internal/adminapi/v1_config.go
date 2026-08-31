// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// ConfigResponse is GET /api/v1/config: the configuration-centric view of the
// same state /api/v1/status reports from the server's side — versions,
// authority, drift and any staged restart.
//
// It carries **no configuration bytes**. Raw export is not part of the external
// contract in v1 (ADR 0019 §24, §36); GET /api/v1/config/export returns the
// redacted structured projection, and this operation returns metadata only.
type ConfigResponse struct {
	APIVersion string `json:"api_version"`

	// ServingVersion is the canonical version of the live runtime;
	// PersistedVersion is the canonical version of what is on disk. They differ
	// while a staged restart or external divergence is pending.
	ServingVersion   string `json:"serving_version,omitempty"`
	PersistedVersion string `json:"persisted_version,omitempty"`

	AuthorityState
	Drift          DriftState          `json:"drift"`
	PendingRestart PendingRestartState `json:"pending_restart"`
}

// PendingRestartResponse is GET /api/v1/config/pending-restart.
type PendingRestartResponse struct {
	APIVersion string `json:"api_version"`
	PendingRestartState
	AuthorityState
}

// ApplyResultResponse is GET /api/v1/config/applies/{apply_id}: the exact
// outcome of one managed transaction, retrievable regardless of later
// transactions (ADR 0019 §24, §31).
//
// This is the operation an apply, stage or rollback polls. Two fields exist to
// stop a client mistaking progress for completion:
//
//   - `state` is the lifecycle enum: pending, finalizing or terminal.
//   - `terminal` is the boolean a client should branch on.
//
// **An HTTP 200 alone is never the terminal test**, and neither is a non-empty
// outcome: `saved_not_live` means the configuration was persisted but the live
// reload result is not yet known, the API answers 202, and the client must keep
// polling. Reaching the poll deadline is `operation_timeout` (ADR 0019 §33.2).
type ApplyResultResponse struct {
	APIVersion string `json:"api_version"`

	ApplyID string `json:"apply_id"`
	// State is "pending", "finalizing" or "terminal".
	State string `json:"state"`
	// Terminal reports whether State is terminal, so a client branches on one
	// boolean rather than re-deriving the enum's meaning.
	Terminal bool `json:"terminal"`

	Operation string `json:"operation,omitempty"`
	Mode      string `json:"mode,omitempty"`
	// Outcome is the terminal reload outcome, empty until the record is
	// terminal.
	Outcome string `json:"outcome,omitempty"`
	OK      bool   `json:"ok"`

	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	// Deadline is the transaction's absolute deadline, so a client can bound
	// its polling instead of guessing.
	Deadline string `json:"deadline,omitempty"`

	PersistedVersion string `json:"persisted_version,omitempty"`
	ServingVersion   string `json:"serving_version,omitempty"`

	// Restored and RestoreError describe the coordinator's restoration arm. For
	// a not_applied outcome they are the distinguishing fact: a clean
	// restoration means the system is where it started, a failed one means it
	// is not (ADR 0019 §33.2).
	Restored     bool   `json:"restored"`
	RestoreError string `json:"restore_error,omitempty"`

	// Degraded is present and empty on a clean success. A degradation never
	// upgrades or downgrades Outcome.
	Degraded []Degradation `json:"degraded"`

	// BootID delimits this record's ledger. A client that observes a changed
	// value knows the record it was polling is gone rather than pending.
	BootID string `json:"boot_id"`
}

// HistoryEntry is one row of the configuration-history listing: safe metadata
// only (ADR 0019 §24).
//
// It carries **no snapshot body**. A history snapshot is a configuration file
// and may contain literal secret values, which is why raw bodies stay on the
// internal route under `history:raw` and are withdrawn from v1 together with
// raw export (§36).
//
// It also carries no actor. Attribution is the audit API's surface, behind its
// own permission; publishing it here would widen who can see who changed what
// without anyone deciding to.
type HistoryEntry struct {
	ID string `json:"id"`
	// RecordedAt is RFC 3339 with a Z offset.
	RecordedAt string `json:"recorded_at"`
	SizeBytes  int64  `json:"size_bytes"`

	ApplyID   string `json:"apply_id,omitempty"`
	Operation string `json:"operation,omitempty"`
	Mode      string `json:"mode,omitempty"`
	Outcome   string `json:"outcome,omitempty"`

	PreviousVersion  string `json:"previous_version,omitempty"`
	CandidateVersion string `json:"candidate_version,omitempty"`

	// MetadataError reports that this snapshot's metadata sidecar could not be
	// read. The snapshot itself is still rollback-able, so the entry is listed
	// rather than hidden.
	MetadataError string `json:"metadata_error,omitempty"`
}

// HistoryListResponse is GET /api/v1/config/history.
//
// History is the one v1 collection whose size is unbounded, and therefore the
// only one that paginates (ADR 0019 §24a): every other collection is bounded by
// the configuration itself and returns in full, because paginating a route list
// would make an operator page through their own configuration.
type HistoryListResponse struct {
	APIVersion string `json:"api_version"`
	// Entries are newest first, by history id, which is monotonic by
	// construction.
	Entries []HistoryEntry `json:"entries"`
	// Limit is the page size actually applied, after the default and the cap.
	Limit int `json:"limit"`
	// NextCursor is supplied when more entries exist. **Treat it as opaque**:
	// its format is not part of the contract and may change. Pass it back
	// verbatim as `?cursor=`; never construct one.
	NextCursor string `json:"next_cursor,omitempty"`
}

// History pagination bounds, published so a client knows them without
// discovering them (ADR 0019 §24a).
const (
	HistoryLimitDefault = 50
	HistoryLimitMax     = 200
)
