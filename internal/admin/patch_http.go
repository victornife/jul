// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

// This file holds the HTTP/wire layer for the structured patch API. Candidate
// construction itself lives in patch_batch.go and is deliberately shared by:
//   POST /api/config/patch            — legacy one-op force preview
//   POST /api/config/patch/preview    — conflict-checked batch preview
//   POST /api/config/patch/candidate  — privileged canonical source view
//   POST /api/config/patch/apply      — atomic managed apply

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

// patchApplyRequest is a server-side, atomic, conflict-checked batch of patch
// operations. Empty BaseVersion is the existing explicit force mode.
type patchApplyRequest struct {
	BaseVersion string         `json:"base_version,omitempty"`
	Ops         []patchRequest `json:"ops"`
}

// conflictResponse is the 409 body returned when the current editable
// configuration no longer matches the version reviewed by the caller.
type conflictResponse struct {
	OK             bool   `json:"ok"`
	Conflict       bool   `json:"conflict"`
	Message        string `json:"message"`
	CurrentVersion string `json:"current_version,omitempty"`
}

// patchPreviewResponse is the secret-safe typed assessment shared by the
// legacy single preview, batch preview, and privileged candidate route.
type patchPreviewResponse struct {
	OK                 bool                    `json:"ok"`
	Summary            string                  `json:"summary"`
	OperationSummaries []patchOperationSummary `json:"operation_summaries"`
	BaseVersion        string                  `json:"base_version"`
	Valid              bool                    `json:"valid"`
	Diff               ConfigDiff              `json:"diff"`
	ValidationErrors   []validationError       `json:"validation_errors"`
	Lifecycle          patchLifecycleSummary   `json:"lifecycle"`
}

// patchCandidateResponse adds unresolved canonical TOML to the same assessment.
// The route is separately authorized with config:raw.
type patchCandidateResponse struct {
	patchPreviewResponse
	Candidate string `json:"candidate"`
}

// patchOperationFailureResponse carries the exact zero-based operation index
// and discriminator that rejected an otherwise atomic batch.
type patchOperationFailureResponse struct {
	OK      bool              `json:"ok"`
	Message string            `json:"message"`
	Errors  []validationError `json:"errors"`
	OpIndex int               `json:"op_index"`
	Op      string            `json:"op"`
}

// configVersion is a short, stable fingerprint of canonical configuration
// bytes. currentWriteState uses the same canonical representation through
// server.CanonicalVersion.
func configVersion(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:8])
}

func (s *Server) patchPreviewResponse(execution patchBatchExecution) patchPreviewResponse {
	operationSummaries := make([]patchOperationSummary, len(execution.OperationSummaries))
	copy(operationSummaries, execution.OperationSummaries)
	validationErrors := make([]validationError, len(execution.ValidationErrors))
	copy(validationErrors, execution.ValidationErrors)
	return patchPreviewResponse{
		OK:                 true,
		Summary:            execution.summaryText(),
		OperationSummaries: operationSummaries,
		BaseVersion:        execution.BaseVersion,
		Valid:              execution.Valid,
		Diff:               execution.Diff,
		ValidationErrors:   validationErrors,
		Lifecycle:          s.patchLifecycleProjection(execution.Lifecycle, execution.Valid),
	}
}

func patchReadAvailable(s *Server) bool {
	return s.deps.ReadConfigRaw != nil || s.deps.LoadConfig != nil
}

func decodePatchBatch(r *http.Request) (patchApplyRequest, error) {
	var req patchApplyRequest
	err := decodePatchJSON(io.LimitReader(r.Body, 1<<16), &req)
	return req, err
}

func decodePatchJSON(r io.Reader, dst any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("request body must contain exactly one JSON value")
		}
		return err
	}
	return nil
}

func writeEmptyPatchBatch(w http.ResponseWriter, subject string) {
	writeJSON(w, http.StatusBadRequest, validationErrorResponse{
		OK:      false,
		Message: "No patch operations were provided.",
		Errors:  humanizeErr(subject + ": at least one operation is required"),
	})
}

// writePatchExecutionError renders failures common to preview and candidate.
// It returns false only for an internal executor error the caller should report
// as 500. No candidate bytes or configured values are serialized here.
func writePatchExecutionError(w http.ResponseWriter, err error) bool {
	var baselineErr *patchBaselineError
	if errors.As(err, &baselineErr) {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cannot load current configuration: " + baselineErr.Error(),
		})
		return true
	}
	var conflictErr *patchVersionConflictError
	if errors.As(err, &conflictErr) {
		writeJSON(w, http.StatusConflict, conflictResponse{
			OK:             false,
			Conflict:       true,
			Message:        "The configuration changed since this edit was prepared; reload and try again.",
			CurrentVersion: conflictErr.CurrentVersion,
		})
		return true
	}
	var operationErr *patchOperationError
	if errors.As(err, &operationErr) {
		writeJSON(w, http.StatusBadRequest, patchOperationFailureResponse{
			OK:      false,
			Message: fmt.Sprintf("Operation %d (%s) could not be applied; no change was made.", operationErr.OpIndex+1, operationErr.Op),
			Errors:  humanizeErr(operationErr.Err.Error()),
			OpIndex: operationErr.OpIndex,
			Op:      operationErr.Op,
		})
		return true
	}
	var candidateErr *patchCandidateError
	if errors.As(err, &candidateErr) {
		switch {
		case errors.Is(candidateErr, context.DeadlineExceeded):
			writeJSON(w, http.StatusGatewayTimeout, validationErrorResponse{
				OK:      false,
				Message: "Patch assessment exceeded the configured deadline; nothing was changed.",
			})
		case errors.Is(candidateErr, context.Canceled):
			writeJSON(w, http.StatusRequestTimeout, validationErrorResponse{
				OK:      false,
				Message: "Patch assessment was canceled before completion; nothing was changed.",
			})
		default:
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: "The configuration contains errors.",
				Errors:  humanizeErr(candidateErr.Error()),
			})
		}
		return true
	}
	return false
}

// handleConfigPatch is the legacy one-operation force-preview wrapper. It uses
// the same executor and typed assessment as batch preview but keeps its historic
// request shape and never returns candidate TOML.
func (s *Server) handleConfigPatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !patchReadAvailable(s) {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	var req patchRequest
	if err := decodePatchJSON(io.LimitReader(r.Body, 1<<16), &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_, execution, err := s.executeCurrentPatchBatch(r.Context(), nil, false, "", []patchRequest{req})
	if err != nil {
		if !writePatchExecutionError(w, err) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, s.patchPreviewResponse(execution))
}

// handleConfigPatchPreview assesses an ordered batch without persistence.
func (s *Server) handleConfigPatchPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !patchReadAvailable(s) {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	req, err := decodePatchBatch(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeEmptyPatchBatch(w, "patch preview")
		return
	}
	_, execution, err := s.executeCurrentPatchBatch(r.Context(), nil, false, req.BaseVersion, req.Ops)
	if err != nil {
		if !writePatchExecutionError(w, err) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, s.patchPreviewResponse(execution))
}

// handleConfigPatchCandidate returns unresolved canonical TOML to config:raw
// callers, together with the exact same assessment produced by preview.
func (s *Server) handleConfigPatchCandidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	if !patchReadAvailable(s) {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}
	req, err := decodePatchBatch(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeEmptyPatchBatch(w, "patch candidate")
		return
	}
	_, execution, err := s.executeCurrentPatchBatch(r.Context(), nil, false, req.BaseVersion, req.Ops)
	if err != nil {
		if !writePatchExecutionError(w, err) {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, patchCandidateResponse{
		patchPreviewResponse: s.patchPreviewResponse(execution),
		Candidate:            string(execution.CandidateRaw),
	})
}

// handleConfigPatchApply re-executes the same batch from a fresh authoritative
// baseline under applyMu, then delegates persistence/reload/staging to the
// existing managed-apply coordinator.
func (s *Server) handleConfigPatchApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	reqCtx := applyRequestContext(r, ApplyOperationPatchApply)
	s.bindManagedApplyDeadline(&reqCtx)

	// Prefer the correlated managed path; retain the legacy closure only for
	// tests and embedding callers that have not migrated.
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
	if applyConfig == nil || s.deps.ReadConfigRaw == nil {
		http.Error(w, "501 Not Implemented", http.StatusNotImplemented)
		return
	}

	req, err := decodePatchBatch(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(req.Ops) == 0 {
		writeEmptyPatchBatch(w, "patch")
		return
	}

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

	// The lock makes baseline read, version check, execution, and coordinator
	// submission one atomic read-modify-write sequence.
	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	opCtx, cancel := managedApplyPrePersistenceContext(reqCtx, r.Context())
	defer cancel()
	state, execution, err := s.executeCurrentPatchBatch(opCtx, &reqCtx, true, req.BaseVersion, req.Ops)
	if err != nil {
		var baselineErr *patchBaselineError
		if errors.As(err, &baselineErr) {
			s.recordAudit(r, "config.patch", "config", "failure", "rejected: cannot load current config: "+baselineErr.Error())
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cannot load current configuration: " + baselineErr.Error()})
			return
		}
		var conflictErr *patchVersionConflictError
		if errors.As(err, &conflictErr) {
			s.recordAudit(r, "config.patch", "config", "failure", "rejected: base version stale (concurrent change)")
			writeJSON(w, http.StatusConflict, conflictResponse{
				OK:             false,
				Conflict:       true,
				Message:        "The configuration changed since this edit was prepared; reload and try again.",
				CurrentVersion: conflictErr.CurrentVersion,
			})
			return
		}
		var operationErr *patchOperationError
		if errors.As(err, &operationErr) {
			s.recordAudit(r, "config.patch", "config", "failure", fmt.Sprintf("rejected: operation %d (%s)", operationErr.OpIndex, operationErr.Op))
			writeJSON(w, http.StatusBadRequest, patchOperationFailureResponse{
				OK:      false,
				Message: fmt.Sprintf("Operation %d (%s) could not be applied; no change was made.", operationErr.OpIndex+1, operationErr.Op),
				Errors:  humanizeErr(operationErr.Err.Error()),
				OpIndex: operationErr.OpIndex,
				Op:      operationErr.Op,
			})
			return
		}
		var candidateErr *patchCandidateError
		if errors.As(err, &candidateErr) {
			if s.writeCandidatePreparationFailure(w, r, reqCtx, mode, candidateErr) {
				return
			}
			s.recordAudit(r, "config.patch", "config", "failure", "rejected: candidate preparation failed")
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: "The configuration contains errors.",
				Errors:  humanizeErr(candidateErr.Error()),
			})
			return
		}
		s.recordAudit(r, "config.patch", "config", "failure", "executor error: "+err.Error())
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	currentEffective := execution.BeforeEffective
	if !s.authorizeConfigTransition(w, r, "config.patch", currentEffective, execution.CandidateEffective) {
		return
	}
	if r.URL.Query().Get("confirm_admin") != "true" {
		id, _ := rbacIdentityFromRequest(r)
		if changes := s.reachabilityChanges(currentEffective, execution.CandidateEffective, id); len(changes) > 0 {
			s.recordAudit(r, "config.patch", "config", "failure", "rejected: admin-reachability change needs confirmation")
			s.emit("config", "apply_failed", "warn", "Structured patch would change admin access and was held for confirmation.")
			writeJSON(w, http.StatusConflict, adminGuardResponse{
				OK:          false,
				AdminChange: true,
				Message:     "This change affects how you reach the admin console; re-apply with confirmation to proceed.",
				Changes:     changes,
			})
			return
		}
	}
	if !execution.Valid {
		s.recordAudit(r, "config.patch", "config", "failure", "rejected: validation failed")
		s.emit("config", "apply_failed", "warn", "Structured patch apply rejected: validation failed.")
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The configuration contains errors.",
			Errors:  patchExecutionValidationErrors(execution),
		})
		return
	}

	prev := state.Raw
	result, applyErr := applyConfig(reqCtx, execution.CandidateConfig, mode)
	result.Summary = execution.summaryStrings()
	result.OperationSummaries = append([]patchOperationSummary(nil), execution.OperationSummaries...)
	result.Diff = execution.Diff
	lifecycleProjection := s.patchLifecycleProjection(execution.Lifecycle, execution.Valid)
	result.Lifecycle = &lifecycleProjection

	if applyErr != nil {
		s.recordAudit(r, "config.patch", "config", "failure", "coordinator error: "+applyErr.Error())
		status := configApplyErrorStatus(result, applyErr)
		if status == http.StatusServiceUnavailable {
			if result.Reload != nil && result.Reload.FailedPhase == "enqueue" {
				s.emit("config", "apply_failed", "error", result.Message)
			} else {
				s.emit("config", "apply_failed", "error", "Configuration storage could not be verified; no change was written.")
			}
			writeJSON(w, status, result)
			return
		}
		s.emit("config", "apply_failed", "error", "Structured patch apply coordinator failed.")
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": applyErr.Error()})
		return
	}

	if result.RestartRequired {
		s.recordAudit(r, "config.patch", "config", "failure", "rejected: restart required")
		s.emit("config", "apply_failed", "warn", "Structured patch apply needs a restart to take effect; no change was applied.")
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
	if result.TimedOutPhase != "" {
		s.recordTimeoutAudit(r, reqCtx.Operation, result)
	}
	if result.OK && isTerminalApplyResult(result) {
		if !s.deps.ManagedHistoryActive {
			s.recordHistory(prev)
		}
		s.recordAudit(r, "config.patch", "config", "success", execution.summaryText())
		s.emit("config", "apply", "info", "Structured patch validated and saved.")
	}

	writeJSON(w, configApplyResultStatus(result), result)
}

func patchExecutionValidationErrors(execution patchBatchExecution) []validationError {
	if len(execution.ValidationErrors) > 0 {
		return append([]validationError(nil), execution.ValidationErrors...)
	}
	out := make([]validationError, 0, len(execution.Lifecycle.ValidationRejected))
	for _, path := range execution.Lifecycle.ValidationRejected {
		out = append(out, validationError{
			Code:     "reserved_configuration_field",
			Path:     path,
			Summary:  "The candidate changes a reserved configuration field.",
			Detail:   "Remove the reserved field before applying the batch.",
			Severity: "error",
		})
	}
	return out
}
