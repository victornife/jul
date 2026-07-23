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
func (s *Server) rollbackToSnapshot(id string, w http.ResponseWriter, r *http.Request) (int, error) {
	raw, err := s.hist.get(id)
	if err != nil {
		return http.StatusNotFound, err
	}
	next, err := config.Parse(raw)
	if err != nil {
		return http.StatusBadRequest, fmt.Errorf("rollback snapshot is not valid configuration: %w", err)
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// Object-level guard: rolling back to a snapshot that changes [admin]
	// requires admin:manage. This runs inside the lock so the current config
	// cannot change between the authorization check and the write (N-02).
	// P1-3: Use requireAdminManageForCandidate which returns error instead of
	// writing response directly, preventing double-write bugs.
	if authErr := s.requireAdminManageForCandidate(r, "config.rollback", next); authErr != nil {
		authID, ok := authErr.(*AuthorizationError)
		if ok {
			return authID.Status, authErr
		}
		return http.StatusForbidden, authErr
	}

	// M-06: Get current state for proper admin lockout confirmation
	// The admin lockout check is already done above via requireAdminManageForCandidate,
	// but we still need the raw snapshot for history recording.
	state, err := s.currentWriteState(true)
	if err != nil {
		// WriteConfigRaw is available but we cannot get current state - fail closed
		return http.StatusServiceUnavailable, fmt.Errorf("cannot get current config state: %w", err)
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
		if changes := s.reachabilityChanges(state.Config, next, id); len(changes) > 0 {
			return http.StatusConflict, &adminReachabilityError{changes: changes}
		}
	}

	if err := s.deps.WriteConfigRaw(raw); err != nil {
		// Map coordinator rejections to the correct HTTP status so the handler
		// does not report a false success.
		if errors.Is(err, ErrRestartRequired) {
			return http.StatusConflict, err
		}
		if strings.Contains(err.Error(), "staged restart is pending") {
			return http.StatusConflict, err
		}
		return http.StatusBadRequest, err
	}
	s.recordHistory(prev)
	return http.StatusOK, nil
}

// handleHistoryRollback re-applies a stored snapshot through the validated raw
// write path, which reloads on success. The running config is snapshotted first
// so a rollback is itself reversible.
func (s *Server) handleHistoryRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	code, err := s.rollbackToSnapshot(req.ID, w, r)
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
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "id": req.ID})
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

// handleConfigRollback re-applies a stored snapshot via the validated write path
// at POST /api/config/rollback. The running config is snapshotted first so the
// rollback is itself reversible.
func (s *Server) handleConfigRollback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.WriteConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	code, err := s.rollbackToSnapshot(req.ID, w, r)
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
		s.recordAudit(r, "config.rollback", "config", "failure", "rollback rejected for snapshot "+req.ID)
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}
	s.recordAudit(r, "config.rollback", "config", "success", "rolled back to snapshot "+req.ID)

	s.emit("config", "rollback", "warning", "Configuration rolled back to a previous snapshot.")
	writeJSON(w, http.StatusOK, map[string]string{"status": "rolled back", "id": req.ID})
}
