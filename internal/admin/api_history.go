// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jul/internal/config"
)

// This file holds the configuration-history and rollback handlers, split out of
// api.go to keep each admin API file focused and under the size bar (Finding
// CQ-3). Both the v1 (/api/history/*) and v2 (/api/config/history*, /api/config/
// rollback) surfaces live here; the shared rollbackToSnapshot serializes every
// rollback write under applyMu (Finding REG-1).

// handleHistoryList serves the configuration snapshot index, newest first.
func (s *Server) handleHistoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	entries, err := s.hist.list()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []historyEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleHistoryGet serves the raw TOML of a single snapshot for preview, keyed
// by the ?id= query parameter.
func (s *Server) handleHistoryGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	id := r.URL.Query().Get("id")
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
}

// rollbackToSnapshot re-applies the stored snapshot id through the validated raw
// write path, snapshotting the running config first so a rollback is itself
// reversible. It holds applyMu across the whole read-modify-write, so a rollback
// is serialized with every other config writer (raw apply, structured patch
// apply, and the other rollback endpoint). Both rollback handlers route through
// here so the serialization can never be applied to one endpoint but not the
// other (Finding REG-1). It returns the HTTP status to send plus a non-nil error
// on failure.
//
// The admin-subtree guard is enforced inside the lock after loading the
// snapshot and the current config, so a concurrent admin change cannot
// invalidate the authorization decision (N-02).
//
// baseVersion is the canonical configuration the operator reviewed in the
// preview. When non-empty it is compared, under applyMu, against the persisted
// config before authorization or persistence, so a rollback reviewed against an
// older baseline is rejected with 409 rather than silently reverting a change a
// concurrent writer made in the interim (Net-new issue 1).
func (s *Server) rollbackToSnapshot(id, baseVersion string, w http.ResponseWriter, r *http.Request) (ConfigApplyResult, int, error) {
	raw, err := s.hist.get(id)
	if err != nil {
		return ConfigApplyResult{}, http.StatusNotFound, err
	}
	next, err := config.Parse(raw)
	if err != nil {
		return ConfigApplyResult{}, http.StatusBadRequest, fmt.Errorf("rollback snapshot is not valid configuration: %w", err)
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	state, err := s.currentWriteState(true)
	if err != nil {
		return ConfigApplyResult{}, http.StatusServiceUnavailable, fmt.Errorf("cannot get current config state: %w", err)
	}

	// Optimistic concurrency (Net-new issue 1): bind the rollback to the exact
	// configuration the operator reviewed in the preview. When the Console sends
	// the base_version the preview reported, reject with 409 if the persisted
	// config changed since — so a concurrent edit by another operator or by
	// automation cannot be silently reverted by a rollback reviewed against an
	// older baseline. The check runs on every attempt, including the confirm_admin
	// retry, so a confirmed admin-reachability transition stays bound to the
	// operation that was reviewed. An empty base_version skips the check (an
	// explicit force-rollback or a legacy client).
	if baseVersion != "" && baseVersion != state.Version {
		s.recordAudit(r, string(ApplyOperationRollback), "config", "failure", "rejected: base version stale (concurrent change)")
		return ConfigApplyResult{
			OK:             false,
			Conflict:       true,
			CurrentVersion: state.Version,
			Message:        "The configuration changed since this rollback was previewed; refresh the preview and try again.",
		}, http.StatusConflict, nil
	}

	reqCtx := applyRequestContext(r, ApplyOperationRollback)
	s.bindManagedApplyDeadline(&reqCtx)
	reqCtx.Baseline = &state
	currentEffective := bindEffectiveBaseline(s, &reqCtx, state.Config)
	opCtx, cancel := managedApplyPrePersistenceContext(reqCtx, r.Context())
	defer cancel()
	effectiveNext, err := prepareMutationCandidateContext(opCtx, &reqCtx, next)
	if err != nil {
		// A resolution deadline is a pre-persistence timeout (504), a
		// cancellation is a client abort (408); both are audited here so the two
		// rollback routes behave identically. Any other error is an ordinary 400.
		if result, status, handled := candidatePreparationFailure("hot", err); handled {
			if result.TimedOutPhase != "" {
				s.recordTimeoutAudit(r, reqCtx.Operation, result)
			} else {
				s.recordAudit(r, string(reqCtx.Operation), "config", "failure", "canceled before persistence: phase=resolve")
			}
			return result, status, nil
		}
		return ConfigApplyResult{}, http.StatusBadRequest, err
	}

	// Authorize against the same exact raw-first baseline passed to the
	// coordinator, not a separate LoadConfig call.
	if authErr := s.requireAdminManageAgainst(r, "config.rollback", currentEffective, effectiveNext.Effective); authErr != nil {
		authID, ok := authErr.(*AuthorizationError)
		if ok {
			return ConfigApplyResult{}, authID.Status, authErr
		}
		return ConfigApplyResult{}, http.StatusForbidden, authErr
	}
	prev := state.Raw

	// Self-lockout guard (finding 9): rolling back to an older snapshot can move
	// the admin listener, change credentials, flip the RBAC/legacy mode, or
	// disable the console — the same reachability-affecting changes the forward
	// apply endpoints confirm. admin:manage authorizes the *permission* but does
	// not confirm the operator accepts losing their own session, so require the
	// explicit confirmation unless confirm_admin=true.
	if r.URL.Query().Get("confirm_admin") != "true" {
		id, _ := rbacIdentityFromRequest(r)
		if changes := s.reachabilityChanges(currentEffective, effectiveNext.Effective, id); len(changes) > 0 {
			return ConfigApplyResult{}, http.StatusConflict, &adminReachabilityError{changes: changes}
		}
	}

	if s.deps.ApplyConfigRaw != nil {
		result, applyErr := s.deps.ApplyConfigRaw(reqCtx, raw, "hot")
		if applyErr != nil {
			return result, configApplyErrorStatus(result, applyErr), applyErr
		}
		if result.TimedOutPhase != "" {
			// Centralize the coordinator-returned rollback timeout audit here so
			// /api/history/rollback and /api/config/rollback record it exactly once.
			s.recordTimeoutAudit(r, reqCtx.Operation, result)
		}
		status := configApplyResultStatus(result)
		// AC-05: skip eager handler-side history when the managed coordinator
		// records it at terminalization (see handleConfigRaw for rationale).
		if result.OK && isTerminalApplyResult(result) && !s.deps.ManagedHistoryActive {
			s.recordHistory(prev)
		}
		return result, status, nil
	}

	if err := s.deps.WriteConfigRaw(raw); err != nil {
		// Map coordinator rejections to the correct HTTP status so the handler
		// does not report a false success.
		if errors.Is(err, ErrRestartRequired) {
			return ConfigApplyResult{}, http.StatusConflict, err
		}
		if strings.Contains(err.Error(), "staged restart is pending") {
			return ConfigApplyResult{}, http.StatusConflict, err
		}
		return ConfigApplyResult{}, http.StatusBadRequest, err
	}
	s.recordHistory(prev)
	return ConfigApplyResult{OK: true, Mode: "hot", Message: "Configuration rolled back."}, http.StatusOK, nil
}

// handleHistoryRollback re-applies a stored snapshot through the validated raw
// write path, which reloads on success. The running config is snapshotted first
// so a rollback is itself reversible.
func (s *Server) handleHistoryRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationRollback)) {
		return
	}
	if s.deps.ApplyConfigRaw == nil && s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID          string `json:"id"`
		BaseVersion string `json:"base_version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, code, err := s.rollbackToSnapshot(req.ID, req.BaseVersion, w, r)
	if err != nil {
		if re, ok := err.(*adminReachabilityError); ok {
			writeJSON(w, code, adminGuardResponse{
				OK:          false,
				AdminChange: true,
				Message:     "This rollback affects how you reach the admin console; re-apply with confirmation to proceed.",
				Changes:     re.changes,
			})
			return
		}
		if isStructuredApplyError(result) {
			writeJSON(w, code, result)
			return
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	if !result.OK {
		writeJSON(w, code, result)
		return
	}
	if code == http.StatusAccepted {
		writeJSON(w, code, result)
		return
	}
	writeJSON(w, http.StatusOK, ConfigMutationResponse{Status: "rolled back", ID: req.ID, ConfigApplyResult: result})
}

// currentRaw reads the running raw configuration, or nil when unavailable. It is
// used to snapshot the prior config just before a successful edit.
func (s *Server) currentRaw() []byte {
	if s.deps.ReadConfigRaw == nil {
		return nil
	}
	raw, err := s.deps.ReadConfigRaw()
	if err != nil {
		return nil
	}
	return raw
}

// recordHistory snapshots the prior configuration after a successful edit.
// Snapshot failures are logged but never surfaced to the operator: the edit
// already succeeded, and a missing snapshot must not look like a failed save.
func (s *Server) recordHistory(prev []byte) {
	if len(prev) == 0 || !s.hist.enabled() {
		return
	}
	if _, err := s.hist.snapshot(prev); err != nil && s.log != nil {
		s.log.Warn("config history snapshot failed", "error", err)
	}
}

// handleConfigHistoryList serves the v2 snapshot index at GET /api/config/history.
func (s *Server) handleConfigHistoryList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	entries, err := s.hist.list()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []historyEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleConfigHistoryGet serves a single snapshot by path parameter at
// GET /api/config/history/{id}. The id is validated to prevent path traversal.
func (s *Server) handleConfigHistoryGet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	// Go 1.22+ ServeMux path parameter extraction.
	id := r.PathValue("id")
	if id == "" {
		// Fallback: accept ?id= for compatibility.
		id = r.URL.Query().Get("id")
	}
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "raw": string(raw)})
}

// historyRollbackDiff is the rollback-preview response: the structured diff plus
// the canonical base_version it was computed against. The Console retains
// base_version and submits it with the rollback so rollbackToSnapshot can reject
// a rollback whose reviewed baseline no longer matches the persisted config
// (optimistic concurrency; Net-new issue 1). The embedded ConfigDiff promotes
// its fields to the top level, preserving the pre-existing response shape.
type historyRollbackDiff struct {
	ConfigDiff
	BaseVersion string `json:"base_version"`
}

// handleConfigHistoryDiff serves a structured diff between the persisted
// configuration and a stored snapshot at GET /api/config/history/{id}/diff,
// together with the canonical base_version the diff was computed against so the
// Console can bind a subsequent rollback to the exact configuration the operator
// reviewed (Net-new issue 1). It reads the snapshot server-side and accepts no
// request body, so a least-privilege rollback-only role (history:rollback) can
// preview exactly what its rollback would change without holding config:write
// and without submitting arbitrary candidate TOML — unlike the generic POST
// /api/config/diff (N-02). The id is validated against path traversal by
// history.get; an unknown id is a 404.
//
// The baseline is the persisted source configuration (currentWriteState), not
// the live serving runtime; the two are normally identical and diverge only
// while a staged restart or external divergence is pending, when a hot rollback
// is already refused.
func (s *Server) handleConfigHistoryDiff(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if s.deps.LoadConfig == nil && s.deps.ReadConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	id := r.PathValue("id")
	if id == "" {
		id = r.URL.Query().Get("id")
	}
	raw, err := s.hist.get(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	after, err := config.Parse(raw)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "stored snapshot is not valid configuration",
		})
		return
	}
	// Diff against the persisted configuration and return the canonical version
	// the diff was computed against. base_version is derived identically to the
	// rollbackToSnapshot concurrency check (currentWriteState), so the value the
	// preview reports is exactly what a subsequent rollback is validated against.
	state, err := s.currentWriteState(false)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "cannot load current config: " + err.Error(),
		})
		return
	}
	writeJSON(w, http.StatusOK, historyRollbackDiff{
		ConfigDiff:  diffConfigs(state.Config, after),
		BaseVersion: state.Version,
	})
}
func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationRollback)) {
		return
	}
	if s.deps.ApplyConfigRaw == nil && s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID          string `json:"id"`
		BaseVersion string `json:"base_version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	result, code, err := s.rollbackToSnapshot(req.ID, req.BaseVersion, w, r)
	if err != nil {
		if authErr, ok := err.(*AuthorizationError); ok {
			writeJSON(w, authErr.Status, map[string]string{"error": authErr.Message})
			return
		}
		if re, ok := err.(*adminReachabilityError); ok {
			s.recordAudit(r, "config.rollback", "config", "failure", "rejected: admin-reachability change needs confirmation")
			writeJSON(w, code, adminGuardResponse{
				OK:          false,
				AdminChange: true,
				Message:     "This rollback affects how you reach the admin console; re-apply with confirmation to proceed.",
				Changes:     re.changes,
			})
			return
		}
		if isStructuredApplyError(result) {
			s.recordAudit(r, "config.rollback", "config", "failure", "rollback coordinator failed for snapshot "+req.ID)
			writeJSON(w, code, result)
			return
		}
		s.recordAudit(r, "config.rollback", "config", "failure", "rollback rejected for snapshot "+req.ID)
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	if !result.OK {
		// Timeout and cancellation audits are recorded centrally in
		// rollbackToSnapshot so both rollback routes behave identically; here we
		// only write the structured result.
		writeJSON(w, code, result)
		return
	}
	if code == http.StatusAccepted {
		s.recordAudit(r, "config.rollback.accepted", "config", "success", "rollback saved; live outcome pending for snapshot "+req.ID)
		writeJSON(w, code, result)
		return
	}
	s.recordAudit(r, "config.rollback", "config", "success", "rolled back to snapshot "+req.ID)

	s.emit("config", "rollback", "warning", "Configuration rolled back to a previous snapshot.")
	writeJSON(w, http.StatusOK, ConfigMutationResponse{Status: "rolled back", ID: req.ID, ConfigApplyResult: result})
}
