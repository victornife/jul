// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"time"
)

// publicManagedApplyRecord is the status:read view of a terminal ledger record.
// It omits actor and source IP, which remain available only through the audit
// API (AC-02). The Result is the same structured ConfigApplyResult the console
// consumes; it already omits secrets and token digests.
type publicManagedApplyRecord struct {
	ID        string            `json:"id"`
	State     ManagedApplyState `json:"state"`
	Operation ApplyOperation    `json:"operation"`
	StartedAt time.Time         `json:"started_at"`
	// Deadline is the absolute transaction deadline projected for deadline-aware
	// polling (AC-08). It is omitted when zero (no bounded deadline recorded).
	Deadline    time.Time `json:"deadline,omitempty"`
	CompletedAt time.Time `json:"completed_at,omitempty"`

	Result ConfigApplyResult `json:"result"`

	HistorySnapshotID string `json:"history_snapshot_id,omitempty"`
	HistoryError      string `json:"history_error,omitempty"`
	FinalizationError string `json:"finalization_error,omitempty"`
}

// toPublic returns the status:read view of the record. ConfigApplyResult
// already omits actor, source IP, secrets, and token digests, so this is a
// straight projection; actor/source IP remain available only through the audit
// API.
func (rec ManagedApplyRecord) toPublic() publicManagedApplyRecord {
	return publicManagedApplyRecord{
		ID:                rec.ID,
		State:             rec.State,
		Operation:         rec.Operation,
		StartedAt:         rec.StartedAt,
		Deadline:          rec.Deadline,
		CompletedAt:       rec.CompletedAt,
		Result:            rec.Result,
		HistorySnapshotID: rec.HistorySnapshotID,
		HistoryError:      rec.HistoryError,
		FinalizationError: rec.FinalizationError,
	}
}

// handleManagedApplyGet serves a single managed-apply transaction by exact ID
// at GET /api/config/applies/{id} (AC-02). It lets a browser retrieve the exact
// terminal result of a recent accepted apply regardless of later transactions,
// closing the window where a newer result could replace the awaited one before
// the browser observed it.
//
// Response rules:
//
//	pending          → 202 with state=pending
//	terminal         → 200 with the complete terminal record
//	unknown/expired  → 404 structured error
//	invalid ID       → 400 structured error
//
// The response is always Cache-Control: no-store and omits actor and source IP.
func (s *Server) handleManagedApplyGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")

	id := r.PathValue("id")
	if !validManagedApplyID(id) {
		s.observeManagedApplyLookup("invalid")
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid apply id"})
		return
	}
	if s.deps.ManagedApplies == nil {
		s.observeManagedApplyLookup("missing")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "managed apply not found"})
		return
	}
	rec, ok := s.deps.ManagedApplies.Get(id)
	if !ok {
		s.observeManagedApplyLookup("missing")
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "managed apply not found"})
		return
	}
	if rec.State == ManagedApplyPending {
		s.observeManagedApplyLookup("pending")
		writeJSON(w, http.StatusAccepted, rec.toPublic())
		return
	}
	s.observeManagedApplyLookup("terminal")
	writeJSON(w, http.StatusOK, rec.toPublic())
}

// observeManagedApplyLookup records one exact-ID lookup outcome when the
// composition root wired a bounded lookup metric (WS06 §7.5). result is a fixed
// low-cardinality enum: "pending", "terminal", "missing", or "invalid".
func (s *Server) observeManagedApplyLookup(result string) {
	if s.deps.ObserveManagedApplyLookup != nil {
		s.deps.ObserveManagedApplyLookup(result)
	}
}
