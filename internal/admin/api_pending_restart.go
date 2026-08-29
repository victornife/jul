// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the pending-restart read and discard handlers:
//
//   GET  /api/config/pending-restart         — read current staged status
//   POST /api/config/pending-restart/discard — discard and restore previous

import "net/http"

// handlePendingRestart returns the current managed planned-restart status.
// When no staged restart is pending it returns {pending: false} rather than
// 404, so callers do not need to distinguish "endpoint exists but empty" from
// "endpoint absent". GET /api/config/pending-restart
func (s *Server) handlePendingRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.deps.PendingRestart == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	st := s.deps.PendingRestart()
	if st == nil {
		writeJSON(w, http.StatusOK, map[string]any{"pending": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": true, "status": st})
}

// handleDiscardPendingRestart discards the managed staged restart and atomically
// restores the previous configuration. It verifies the marker is consistent, the
// disk digest matches the staged candidate, and the live serving version has not
// changed since staging. On any verification failure it returns 409 and leaves
// all files untouched.
// POST /api/config/pending-restart/discard
func (s *Server) handleDiscardPendingRestart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.denyIfFileOwned(w, r, "config.stage_restart.discarded") {
		return
	}
	if s.deps.DiscardPendingRestart == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}

	// Serialize the discard under applyMu so it cannot race with a concurrent
	// apply.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	// Snapshot the staged config into history BEFORE discarding it so the
	// operator can recover the staged candidate via expert history if needed
	// (M-04 fix: discard must be reversible through history).
	if s.deps.ReadConfigRaw != nil {
		if stagedBytes, err := s.deps.ReadConfigRaw(); err == nil {
			s.recordHistory(stagedBytes)
		}
	}

	result, err := s.deps.DiscardPendingRestart()
	if err != nil {
		s.recordAudit(r, "config.stage_restart.discarded", "config", "failure",
			"discard failed: "+err.Error())
		s.emit("config", "stage_restart_discard_failed", "warn",
			"Planned-restart discard failed: "+err.Error())
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":      false,
			"error":   err.Error(),
			"message": "Discard failed: " + err.Error(),
		})
		return
	}
	s.recordAudit(r, "config.stage_restart.discarded", "config", "success",
		"staged restart discarded; previous configuration restored")
	s.emit("config", "stage_restart_discarded", "info",
		"Staged configuration discarded and previous configuration restored.")
	// Also emit a config.restored event so the history timeline shows a restore
	// action separate from the discard (M-04 fix: restored event).
	s.recordAudit(r, "config.restored", "config", "success",
		"configuration restored from pre-stage backup")
	writeJSON(w, http.StatusOK, result)
}
