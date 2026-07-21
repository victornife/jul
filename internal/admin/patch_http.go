// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the admin HTTP handlers for the structured-patch API:
//   POST /api/config/patch            — preview a single op (handleConfigPatch)
//   POST /api/config/patch/preview    — preview a batch atomically (handleConfigPatchPreview)
//   POST /api/config/patch/candidate  — return the full candidate TOML for a batch,
//                                       gated by config:raw so operators never see
//                                       secret-bearing config (N-01)
//   POST /api/config/patch/apply      — apply a batch atomically (handleConfigPatchApply)
// and their supporting types (patchApplyRequest, conflictResponse) and
// the configVersion fingerprint helper. Separating these from patch.go lets
// the operation dispatch (applyPatch) and helpers (patch_helpers.go) be
// read independently of the HTTP/serialization layer.

import (
	"context"
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
// returns the generated diff for review BEFORE the change is applied — it does
// not persist and does not return the full candidate TOML, so operators cannot
// extract secrets from the structured preview (N-01). The UI shows the diff and
// the operator confirms via /api/config/patch/apply, which recomputes the
// candidate server-side.
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
	resp, err := s.previewPatchOps(r.Context(), []patchRequest{req})
	if err != nil {
		if ppe, ok := err.(*patchPreviewError); ok {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: ppe.Message,
				Errors:  ppe.Errors,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
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

// previewPatchOps applies ops to a freshly-loaded config and returns a secret-
// safe preview response: summary, diff, base_version, and optional validation
// diagnostics. It never includes the full candidate TOML (N-01).
func (s *Server) previewPatchOps(ctx context.Context, ops []patchRequest) (map[string]any, error) {
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		return nil, err
	}
	before, err := config.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	summaries := make([]string, 0, len(ops))
	for i, op := range ops {
		summary, aerr := applyPatch(cfg, op)
		if aerr != nil {
			return nil, &patchPreviewError{
				Message: fmt.Sprintf("Operation %d could not be applied; no change was made.", i+1),
				Errors:  humanizeErr(aerr.Error()),
			}
		}
		summaries = append(summaries, summary)
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	beforeCfg, err := config.Parse(before)
	if err != nil {
		return nil, err
	}
	resp := map[string]any{
		"ok":           true,
		"summary":      strings.Join(summaries, "; "),
		"diff":         diffConfigs(beforeCfg, cfg),
		"base_version": configVersion(before),
	}
	if verr := validateRaw(candidate); verr != nil {
		resp["validation_errors"] = humanizeErr(verr.Error())
	}
	return resp, nil
}

// patchPreviewError carries a structured validation failure from previewPatchOps
// so the HTTP layer can return 400 without leaking the candidate bytes.
type patchPreviewError struct {
	Message string
	Errors  []validationError
}

func (e *patchPreviewError) Error() string { return e.Message }

// handleConfigPatchPreview previews a batch of structured patch operations
// without persisting. It applies every op to a freshly-loaded config and
// returns the generated diff for review, exactly like handleConfigPatch but for
// multiple ops. The full candidate TOML is intentionally omitted; callers that
// need it must use /api/config/patch/candidate, which requires config:raw
// (N-01).
// POST /api/config/patch/preview
func (s *Server) handleConfigPatchPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
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
			Errors:  humanizeErr("patch preview: at least one operation is required"),
		})
		return
	}
	resp, err := s.previewPatchOps(r.Context(), req.Ops)
	if err != nil {
		if ppe, ok := err.(*patchPreviewError); ok {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: ppe.Message,
				Errors:  ppe.Errors,
			})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleConfigPatchCandidate returns the full candidate TOML that results from
// applying a batch of structured patch operations. It is gated by config:raw
// because the marshaled configuration may contain literal secrets (legacy admin
// token, RBAC principal tokens, discovery tokens, etc.). Operators with only
// config:write must review the structured diff instead (N-01).
// POST /api/config/patch/candidate
func (s *Server) handleConfigPatchCandidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if s.deps.LoadConfig == nil {
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
			Errors:  humanizeErr("patch candidate: at least one operation is required"),
		})
		return
	}
	cfg, err := s.deps.LoadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	for i, op := range req.Ops {
		if _, aerr := applyPatch(cfg, op); aerr != nil {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: fmt.Sprintf("Operation %d could not be applied; no change was made.", i+1),
				Errors:  humanizeErr(aerr.Error()),
			})
			return
		}
	}
	candidate, err := config.Marshal(cfg)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"candidate": string(candidate),
	})
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
	reqCtx := applyRequestContext(r)
	// Prefer the new correlated apply path; fall back to the legacy
	// WriteConfigRaw closure for tests and callers that have not migrated.
	applyConfig := s.deps.ApplyConfig
	if applyConfig == nil && s.deps.WriteConfigRaw != nil {
		applyConfig = func(_ ApplyRequestContext, cfg *config.Config, mode string) (ConfigApplyResult, error) {
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
		s.recordAudit(r, "config.patch", "config", "failure", "rejected: base version stale (concurrent change)")
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
	// Object-level guard: a structured patch may target [admin] fields (e.g.
	// rate limits via future ops). Any change in the [admin] subtree requires
	// admin:manage in addition to config:apply.
	if !s.authorizeConfigCandidate(w, r, "config.patch", cfg) {
		return
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
	result, err := applyConfig(reqCtx, cfg, mode)
	if err != nil {
		s.recordAudit(r, "config.patch", "config", "failure", "coordinator error: "+err.Error())
		s.emit("config", "apply_failed", "error", "Structured patch apply coordinator failed.")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if result.RestartRequired {
		// Return the full structured result so the client receives can_stage
		// and subsystem information, not just a plain message.
		s.recordAudit(r, "config.patch", "config", "failure",
			"rejected: restart required")
		s.emit("config", "apply_failed", "warn",
			"Structured patch apply needs a restart to take effect; no change was applied.")
		writeJSON(w, http.StatusConflict, result)
		return
	}
	if len(result.ValidationErrors) > 0 {
		s.recordAudit(r, "config.patch", "config", "failure", "rejected: validation failed")
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
		s.recordAudit(r, "config.patch", "config", "success", strings.Join(summaries, "; "))
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
