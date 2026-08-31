// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"strconv"

	"jul/internal/adminapi"
	"jul/internal/rbac"
)

// handleV1Config serves GET /api/v1/config: versions, authority, drift and any
// staged restart, as metadata. It returns no configuration bytes — raw export
// is not part of the external contract in v1 (ADR 0019 §24, §36).
func (s *Server) handleV1Config(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	authority := s.currentAuthority()
	writeAPIJSON(w, http.StatusOK, adminapi.ConfigResponse{
		APIVersion:       adminapi.APIVersion,
		ServingVersion:   s.servingVersion(),
		PersistedVersion: authority.DiskVersion,
		AuthorityState:   authorityState(authority),
		Drift:            driftState(authority),
		PendingRestart:   s.pendingRestartState(),
	})
}

// handleV1PendingRestart serves GET /api/v1/config/pending-restart.
func (s *Server) handleV1PendingRestart(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}
	writeAPIJSON(w, http.StatusOK, adminapi.PendingRestartResponse{
		APIVersion:          adminapi.APIVersion,
		PendingRestartState: s.pendingRestartState(),
		AuthorityState:      authorityState(s.currentAuthority()),
	})
}

// handleV1ApplyGet serves GET /api/v1/config/applies/{apply_id}: the exact
// outcome of one managed transaction.
//
// It answers 202 while the record is pending or finalizing and 200 once it is
// terminal, and reports `terminal` explicitly so a client branches on a boolean
// rather than on the status code — `saved_not_live` is a non-empty outcome that
// is *not* terminal, and a client that stopped polling on it would wait forever
// for a result that had not happened yet (ADR 0019 §33.2).
func (s *Server) handleV1ApplyGet(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}

	id := r.PathValue("apply_id")
	if !validManagedApplyID(id) {
		s.observeManagedApplyLookup("invalid")
		// The client built a bad request, which is a usage error rather than a
		// missing resource.
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
			"%q is not a valid apply id", id).WithDetails(adminapi.Details{Field: "apply_id"}))
		return
	}
	if s.deps.ManagedApplies == nil {
		s.observeManagedApplyLookup("missing")
		s.writeApplyNotFound(w, r)
		return
	}
	rec, ok := s.deps.ManagedApplies.Get(id)
	if !ok {
		s.observeManagedApplyLookup("missing")
		s.writeApplyNotFound(w, r)
		return
	}

	// A principal admitted only via history:rollback — a rollback-only custom
	// role holding neither status:read nor config:apply — may read only the
	// rollback records it owns, never the result of an unrelated transaction it
	// could otherwise probe by id. The same rule as the internal route, because
	// an external alias that relaxed it would be exactly the authorization
	// drift ADR 0019 §24 warns about.
	ident, _ := rbacIdentityFromRequest(r)
	if !ident.Has(rbac.StatusRead) && !ident.Has(rbac.ConfigApply) {
		if rec.Operation != ApplyOperationRollback ||
			rec.OwnerTokenID == "" || rec.OwnerTokenID != ident.TokenID {
			// The caller holds the permission it was admitted through, so the
			// denial is ownership, not a missing grant. Reporting a permission
			// it already has would send it to fix the wrong thing, and naming
			// the record would confirm the record exists.
			s.observeManagedApplyLookup("forbidden")
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeForbidden,
				"This managed apply record is not accessible to the current credential."))
			return
		}
	}

	out := s.applyResultResponse(rec)
	switch rec.State {
	case ManagedApplyPending, ManagedApplyFinalizing:
		s.observeManagedApplyLookup(string(rec.State))
		writeAPIJSON(w, http.StatusAccepted, out)
	case ManagedApplyTerminal:
		s.observeManagedApplyLookup("terminal")
		writeAPIJSON(w, http.StatusOK, out)
	default:
		// A state outside the bounded enum is a server consistency error. It is
		// never reported as a completed transaction.
		s.observeManagedApplyLookup("invalid")
		writeAPIError(w, r, adminapi.New(adminapi.CodeInternalError))
	}
}

// writeApplyNotFound reports an unknown or evicted apply id. It carries the
// kind but not the id: the id came from the caller, so echoing it adds nothing,
// and the ledger's bounds already tell a client why a record may be gone.
func (s *Server) writeApplyNotFound(w http.ResponseWriter, r *http.Request) {
	writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound,
		"No managed apply record with that id. It may never have existed, or it may have been evicted — "+
			"the ledger is process-local and its bounds are published in /api/v1/capabilities.").
		WithDetails(adminapi.Details{Kind: "managed_apply"}))
}

func (s *Server) applyResultResponse(rec ManagedApplyRecord) adminapi.ApplyResultResponse {
	out := adminapi.ApplyResultResponse{
		APIVersion:  adminapi.APIVersion,
		ApplyID:     rec.ID,
		State:       string(rec.State),
		Terminal:    rec.State == ManagedApplyTerminal,
		Operation:   string(rec.Operation),
		Mode:        rec.Result.Mode,
		Outcome:     applyOutcome(rec),
		OK:          rec.Result.OK,
		StartedAt:   adminapi.Timestamp(rec.StartedAt),
		CompletedAt: adminapi.Timestamp(rec.CompletedAt),
		Deadline:    adminapi.Timestamp(rec.Deadline),
		Restored:    rec.Result.Restored,
		Degraded:    recordDegradations(rec),
		BootID:      s.bootID(),
	}
	if rec.Result.FinalDiskVersion != "" {
		out.PersistedVersion = rec.Result.FinalDiskVersion
	} else {
		out.PersistedVersion = rec.Result.PersistedVersion
	}
	if rec.Result.FinalServingVersion != "" {
		out.ServingVersion = rec.Result.FinalServingVersion
	} else {
		out.ServingVersion = rec.Result.ServingVersion
	}
	out.RestoreError = rec.Result.RestoreError
	return out
}

// recordDegradations maps a record's provenance failures onto ADR 0019 §33.2's
// closed set. It returns an empty, non-nil slice on a clean success.
func recordDegradations(rec ManagedApplyRecord) []adminapi.Degradation {
	out := []adminapi.Degradation{}
	if rec.HistoryError != "" {
		out = append(out, adminapi.Degradation{Kind: "history_error", Message: rec.HistoryError})
	}
	if rec.FinalizationError != "" {
		out = append(out, adminapi.Degradation{Kind: "finalization_error", Message: rec.FinalizationError})
	}
	return out
}

// applyOutcome reads the terminal reload outcome, which lives on the reload
// result rather than on the apply result. It is empty until a reload has
// produced one, which is exactly the state in which a client must keep polling.
func applyOutcome(rec ManagedApplyRecord) string {
	if rec.Result.Reload == nil {
		return ""
	}
	return string(rec.Result.Reload.Outcome)
}

// handleV1HistoryList serves GET /api/v1/config/history: safe metadata for the
// stored snapshots, newest first, paginated.
//
// History is the only v1 collection that paginates, because it is the only one
// whose size is unbounded (ADR 0019 §24a).
func (s *Server) handleV1HistoryList(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodGet) {
		return
	}

	limit, apiErr := historyLimit(r.URL.Query().Get("limit"))
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}

	entries, err := s.hist.list()
	if err != nil {
		// The store could not be read. That is not the caller's fault and not a
		// validation failure; the error class is what a client acts on, and the
		// message deliberately carries no path.
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStorageUnavailable,
			"The configuration history store could not be read."))
		return
	}

	// s.hist.list() returns newest first already; the cursor names the last
	// entry of the previous page, so the next page starts after it.
	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		entries = historyAfter(entries, cursor)
		if entries == nil {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest,
				"The cursor does not name a known history entry. Cursors are opaque and expire as snapshots are pruned; "+
					"restart the listing without one.").WithDetails(adminapi.Details{Field: "cursor"}))
			return
		}
	}

	out := adminapi.HistoryListResponse{
		APIVersion: adminapi.APIVersion,
		Entries:    []adminapi.HistoryEntry{},
		Limit:      limit,
	}
	for i, e := range entries {
		if i == limit {
			out.NextCursor = entries[i-1].ID
			break
		}
		out.Entries = append(out.Entries, adminapi.HistoryEntry{
			ID:               e.ID,
			RecordedAt:       adminapi.Timestamp(e.Time),
			SizeBytes:        e.Size,
			ApplyID:          e.ApplyID,
			Operation:        string(e.Operation),
			Mode:             e.Mode,
			Outcome:          e.Outcome,
			PreviousVersion:  e.PreviousVersion,
			CandidateVersion: e.CandidateVersion,
			MetadataError:    e.MetadataError,
		})
	}
	writeAPIJSON(w, http.StatusOK, out)
}

// historyLimit applies §24a's default and cap. An out-of-range value is a
// usage error rather than being silently clamped: a client asking for 1000
// entries and receiving 200 without being told has a paging bug it cannot see.
func historyLimit(raw string) (int, *adminapi.Error) {
	if raw == "" {
		return adminapi.HistoryLimitDefault, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, adminapi.Errorf(adminapi.CodeInvalidRequest, "limit must be an integer").
			WithDetails(adminapi.Details{Field: "limit"})
	}
	if n < 1 || n > adminapi.HistoryLimitMax {
		return 0, adminapi.Errorf(adminapi.CodeInvalidRequest,
			"limit must be between 1 and %d", adminapi.HistoryLimitMax).
			WithDetails(adminapi.Details{Field: "limit"})
	}
	return n, nil
}

// historyAfter returns the entries following the cursor entry, or nil when the
// cursor names no known entry. An empty (but non-nil) result means the cursor
// named the last entry, which is the end of the listing rather than an error.
func historyAfter(entries []historyEntry, cursor string) []historyEntry {
	for i, e := range entries {
		if e.ID == cursor {
			return entries[i+1:]
		}
	}
	return nil
}
