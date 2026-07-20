// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the admin HTTP handlers for the structured-patch API:
//   POST /api/config/patch        — preview a single op (handleConfigPatch)
//   POST /api/config/patch/apply  — apply a batch atomically (handleConfigPatchApply)
// and their supporting types (patchApplyRequest, conflictResponse) and
// the configVersion fingerprint helper. Separating these from patch.go lets
// the operation dispatch (applyPatch) and helpers (patch_helpers.go) be
// read independently of the HTTP/serialization layer.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jul/internal/config"
)

// handleConfigPatch applies a single structured edit to the running config and
// returns the generated full diff for review BEFORE the change is applied — it
// does not persist. The UI shows the diff and the operator confirms via the
// existing /api/config/apply path with the returned candidate TOML.
// POST /api/config/patch
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Snapshot the pre-patch config so the diff reflects exactly this edit.
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	summary, err := applyPatch(cfg, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The edit could not be applied.",
			Errors:  humanizeErr(err.Error()),
		})
		return
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Re-parse the before/after so the diff is computed over parsed models,
	// mirroring handleConfigDiff.
	beforeCfg, err := config.Parse(before)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{
		"ok":        true,
		"summary":   summary,
		"candidate": string(candidate),
		"diff":      diffConfigs(beforeCfg, cfg),
		// base_version is the version of the config this candidate was computed
		// from. A client echoes it back to /api/config/patch/apply so a stale
		// edit is rejected (409) instead of silently clobbering a concurrent
		// change (P2-12 optimistic concurrency).
		"base_version": configVersion(before),
	}
	// Cheap preview validation: parse the candidate and run the same structural,
	// secret-expansion, and WAF/auth dry-run checks as /api/config/validate, so
	// the operator sees problems BEFORE confirming the apply. This is advisory —
	// the authoritative, heavier full-factory preflight still runs at apply time
	// (WriteConfigRaw -> applyPreflight). A failing check does not block the
	// preview: the diff is still returned so the operator can see what the edit
	// would do, with the errors surfaced alongside it.
	if verr := validateRaw(candidate); verr != nil {
		resp["validation_errors"] = humanizeErr(verr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}

// configVersion is a short, stable fingerprint of a configuration used for
// optimistic concurrency. It is computed over the canonical marshaled form, so
// it is insensitive to comments and whitespace in the on-disk file and matches
// between a preview and a later apply of the same logical config.
func configVersion(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

// patchApplyRequest is a server-side, atomic, conflict-checked batch of patch
// operations. Unlike the preview endpoint it persists the result: every op is
// applied to a single freshly-loaded config under a lock, and the result is
// written through the same validated preflight as /api/config/apply.
type patchApplyRequest struct {
	// BaseVersion is the config version the ops were computed against (returned
	// by the preview as base_version, or by a config read). When non-empty the
	// apply is rejected with 409 Conflict if the live config has changed since,
	// preventing a stale edit from silently clobbering a concurrent change. An
	// empty value skips the check (an explicit force-apply).
	BaseVersion string `json:"base_version,omitempty"`
	// Ops are applied in order to one config; a failure in any op aborts the
	// whole batch before anything is written (all-or-nothing).
	Ops []patchRequest `json:"ops"`
}

// conflictResponse is the 409 body when an apply is rejected because the live
// config changed since the edit was prepared. CurrentVersion lets the client
// reload, recompute, and retry.
type conflictResponse struct {
	OK             bool   `json:"ok"`
	Conflict       bool   `json:"conflict"`
	Message        string `json:"message"`
	CurrentVersion string `json:"current_version,omitempty"`
}

// handleConfigPatchApply applies a batch of structured patch operations
// atomically and entirely server-side — it never trusts a client-rendered
// candidate. All ops are applied to one freshly-loaded config under s.applyMu,
// and the result is persisted through the same validated apply preflight as
// /api/config/apply, so a config that passes cannot fail the subsequent build.
// Optimistic concurrency (base_version) prevents a stale edit from silently
// clobbering a concurrent change (P2-12 lost update).
// POST /api/config/patch/apply
func (s *Server) handleConfigPatchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	// Prefer the new correlated apply path; fall back to the legacy
	// WriteConfigRaw closure for tests and callers that have not migrated.
	applyConfig := s.deps.ApplyConfig
	if applyConfig == nil && s.deps.WriteConfigRaw != nil {
		applyConfig = func(cfg *config.Config, mode string) (ConfigApplyResult, error) {
			data, err := config.Marshal(cfg)
			if err != nil {
				return ConfigApplyResult{OK: false, Mode: mode}, err
			}
			if err := s.deps.WriteConfigRaw(data); err != nil {
				result := ConfigApplyResult{OK: false, Mode: mode, Message: err.Error()}
				if errors.Is(err, ErrRestartRequired) {
					result.RestartRequired = true
					return result, nil
				}
				return result, err
			}
			return ConfigApplyResult{
				OK:             true,
				Mode:           mode,
				Version:        configVersion(data),
				ServingVersion: configVersion(data),
				Message:        "Configuration validated and saved.",
			}, nil
		}
	}
	if s.deps.LoadConfig == nil || applyConfig == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchApplyRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "No patch operations were provided.",
			Errors:  humanizeErr("patch: at least one operation is required"),
		})
		return
	}

	// Serialize the whole read-modify-write so the version check and the write
	// are atomic. Without this, two concurrent applies could both read the same
	// base version, both pass the conflict check, and the second would silently
	// clobber the first.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	before, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	currentVersion := configVersion(before)
	if req.BaseVersion != "" && req.BaseVersion != currentVersion {
		s.recordAudit("config.patch", "config", "failure", "rejected: base version stale (concurrent change)", adminClientIP(r))
		writeJSON(w, http.StatusConflict, conflictResponse{
			OK:             false,
			Conflict:       true,
			Message:        "The configuration changed since this edit was prepared; reload and try again.",
			CurrentVersion: currentVersion,
		})
		return
	}

	// Apply every op to the single loaded config. A failure in any op aborts the
	// whole batch before anything is written, so the apply is all-or-nothing.
	summaries := make([]string, 0, len(req.Ops))
	for i, op := range req.Ops {
		summary, aerr := applyPatch(cfg, op)
		if aerr != nil {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: fmt.Sprintf("Operation %d could not be applied; no change was made.", i+1),
				Errors:  humanizeErr(aerr.Error()),
			})
			return
		}
		summaries = append(summaries, summary)
	}

	// Snapshot the prior config, then persist through the authoritative apply
	// preflight. A rejection here means nothing was written, preserving the
	// all-or-nothing guarantee.
	prev := s.currentRaw()
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", "hot":
		mode = "hot"
	case "stage_restart":
		// valid
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("unknown apply mode %q; valid values: hot, stage_restart", mode),
		})
		return
	}
	result, err := applyConfig(cfg, mode)
	if err != nil {
		s.recordAudit("config.patch", "config", "failure", "coordinator error: "+err.Error(), adminClientIP(r))
		s.emit("config", "apply_failed", "error", "Structured patch apply coordinator failed.")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if result.RestartRequired {
		s.writeRestartRequired(w, r, "config.patch", errors.New(result.Message))
		return
	}
	if len(result.ValidationErrors) > 0 {
		s.recordAudit("config.patch", "config", "failure", "rejected: validation failed", adminClientIP(r))
		s.emit("config", "apply_failed", "warn", "Structured patch apply rejected: validation failed.")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: result.Message,
			Errors:  humanizeErr(strings.Join(result.ValidationErrors, "\n")),
		})
		return
	}

	if result.OK {
		s.recordHistory(prev)
		s.recordAudit("config.patch", "config", "success", strings.Join(summaries, "; "), adminClientIP(r))
		s.emit("config", "apply", "info", "Structured patch validated and saved.")
	}

	beforeCfg, _ := config.Parse(before)
	result.Summary = summaries
	result.Diff = diffConfigs(beforeCfg, cfg)

	status := http.StatusOK
	if !result.OK {
		status = http.StatusConflict
	}
	writeJSON(w, status, result)
}
