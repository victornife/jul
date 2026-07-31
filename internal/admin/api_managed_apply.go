// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"time"

	"jul/internal/rbac"
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
//	finalizing       → 202 with state=finalizing
//	terminal         → 200 with the complete terminal record
//	unknown/expired  → 404 structured error
//	invalid ID       → 400 structured error
//	invalid state    → 500 structured error
//
// A record is terminal only when state == terminal; HTTP 200 alone is never the
// terminal test. The response is always Cache-Control: no-store and omits actor
// and source IP.
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
	// A principal admitted only via history:rollback — a rollback-only custom
	// role that holds neither status:read nor config:apply — may read only the
	// rollback records it owns, never the result of an unrelated apply or patch
	// transaction it could otherwise probe by ID (N-01).
	ident, _ := rbacIdentityFromRequest(r)
	if !ident.Has(rbac.StatusRead) && !ident.Has(rbac.ConfigApply) {
		if rec.Operation != ApplyOperationRollback ||
			rec.OwnerTokenID == "" || rec.OwnerTokenID != ident.TokenID {
			s.observeManagedApplyLookup("forbidden")
			writeForbidden(w, rbac.HistoryRollback, ident)
			return
		}
	}
	switch rec.State {
	case ManagedApplyPending:
		s.observeManagedApplyLookup("pending")
		writeJSON(w, http.StatusAccepted, rec.toPublic())
	case ManagedApplyFinalizing:
		// The runtime outcome exists, but history, audit, metrics and the
		// terminal-ledger completion are still running: still in-flight to the
		// client, which must keep polling until state=terminal.
		s.observeManagedApplyLookup("finalizing")
		writeJSON(w, http.StatusAccepted, rec.toPublic())
	case ManagedApplyTerminal:
		s.observeManagedApplyLookup("terminal")
		writeJSON(w, http.StatusOK, rec.toPublic())
	default:
		// A state outside the bounded enum is a server consistency error; it is
		// never reported as a completed transaction.
		s.observeManagedApplyLookup("invalid")
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "managed apply has an invalid lifecycle state",
		})
	}
}

// observeManagedApplyLookup records one exact-ID lookup outcome when the
// composition root wired a bounded lookup metric (WS06 §7.5). result is a fixed
// low-cardinality enum: "pending", "finalizing", "terminal", "missing",
// "forbidden", or "invalid".
func (s *Server) observeManagedApplyLookup(result string) {
	if s.deps.ObserveManagedApplyLookup != nil {
		s.deps.ObserveManagedApplyLookup(result)
	}
}
