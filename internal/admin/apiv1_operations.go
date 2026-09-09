// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"jul/internal/adminapi"
)

// v1ValidationResponse is the success shape of config validation. Invalid
// candidates use validation_failed so clients never have to inspect a success
// body to decide whether a candidate is admissible.
type v1ValidationResponse struct {
	OK bool `json:"ok"`
}

type v1RollbackRequest struct {
	ID          string `json:"id"`
	BaseVersion string `json:"base_version"`
}

// handleV1ConfigValidate validates an exact raw candidate through the canonical
// parser/resolver/validator. It performs no persistence, history, apply-ledger
// registration or runtime mutation.
func (s *Server) handleV1ConfigValidate(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	body, apiErr := readV1TOML(w, r)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if err := validateRaw(r.Context(), body); err != nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeValidationFailed,
			"The candidate configuration is not valid.").WithDetails(adminapi.Details{Errors: v1Findings(err)}))
		return
	}
	writeAPIJSON(w, http.StatusOK, v1ValidationResponse{OK: true})
}

// handleV1ConfigPlan classifies a raw candidate against one authoritative
// baseline using the same raw-preview engine as the Console. base_version is
// optional for a first plan; when supplied it pins the assessment and stale
// input is rejected. The response always supplies the exact base_version a
// later mutation must submit.
func (s *Server) handleV1ConfigPlan(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	body, apiErr := readV1TOML(w, r)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	state, err := s.currentWriteState(true)
	if err != nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStorageUnavailable,
			"The editable configuration is unavailable."))
		return
	}
	if requested := strings.TrimSpace(r.URL.Query().Get("base_version")); requested != "" && requested != state.Version {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStaleBaseVersion,
			"The configuration changed since this candidate was prepared.").WithDetails(adminapi.Details{
			BaseVersion: requested, CurrentVersion: state.Version,
		}))
		return
	}
	effective, live := s.patchRuntimeBaseline(nil, state.Config)
	assessment, err := previewRawCandidate(r.Context(), state.Config, effective, live, state.Version, body)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeOperationTimeout, "Candidate assessment did not complete in time."))
			return
		}
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeValidationFailed,
			"The candidate configuration could not be assessed.").WithDetails(adminapi.Details{Errors: v1Findings(err)}))
		return
	}
	writeAPIJSON(w, http.StatusOK, rawConfigPreviewResponse{
		OK:               true,
		BaseVersion:      assessment.BaseVersion,
		Valid:            assessment.Valid,
		ValidationErrors: assessment.ValidationErrors,
		Lint:             assessment.Lint,
		Diff:             assessment.Diff,
		Lifecycle:        s.patchLifecycleProjection(assessment.Lifecycle, assessment.Valid),
	})
}

func (s *Server) handleV1RouteTest(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	var in routeTestRequest
	if _, apiErr := readV1JSON(w, r, &in); apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	state, apiErr := s.v1ConfigState()
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	in.Path = strings.TrimSpace(in.Path)
	if in.Path == "" {
		in.Path = "/"
	}
	writeAPIJSON(w, http.StatusOK, testRoute(state.Config, in))
}

// handleV1ConfigPatch is the side-effect-free structured patch preview. It
// calls the canonical batch executor directly so v1's 1 MiB body policy does
// not inherit the Console transport's historical 64 KiB decoding cap.
func (s *Server) handleV1ConfigPatch(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if !patchReadAvailable(s) {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotImplemented, "Structured patch preview is unavailable."))
		return
	}
	var req patchApplyRequest
	if _, apiErr := readV1JSON(w, r, &req); apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if len(req.Ops) == 0 {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "At least one patch operation is required.").WithDetails(adminapi.Details{Field: "ops"}))
		return
	}
	_, execution, err := s.executeCurrentPatchBatch(r.Context(), nil, false, req.BaseVersion, req.Ops)
	if err != nil {
		s.writeV1PatchExecutionError(w, r, err, req.BaseVersion)
		return
	}
	writeAPIJSON(w, http.StatusOK, s.patchPreviewResponse(execution))
}

// The mutating adapters do only v1 admission. The canonical Console handler is
// then executed unchanged; v1Capture rewrites only its historical error shape.
func (s *Server) handleV1ConfigApply(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationConfigApply)) {
		return
	}
	base, apiErr := requiredBaseVersion(r)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	body, apiErr := readV1TOML(w, r)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	restoreRequestBody(r, body)
	s.runCanonicalV1(w, r, base, s.handleConfigApply)
}

func (s *Server) handleV1ConfigPatchApply(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if s.denyIfFileOwned(w, r, auditActionPatch) {
		return
	}
	var req patchApplyRequest
	body, apiErr := readV1JSON(w, r, &req)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.BaseVersion) == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "base_version is required for this mutation.").WithDetails(adminapi.Details{Field: "base_version"}))
		return
	}
	if len(req.Ops) == 0 {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "At least one patch operation is required.").WithDetails(adminapi.Details{Field: "ops"}))
		return
	}
	restoreRequestBody(r, body)
	s.runCanonicalV1(w, r, req.BaseVersion, s.handleConfigPatchApply)
}

func (s *Server) handleV1Rollback(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationRollback)) {
		return
	}
	var req v1RollbackRequest
	body, apiErr := readV1JSON(w, r, &req)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.ID) == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "id is required.").WithDetails(adminapi.Details{Field: "id"}))
		return
	}
	if strings.TrimSpace(req.BaseVersion) == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "base_version is required for this mutation.").WithDetails(adminapi.Details{Field: "base_version"}))
		return
	}
	restoreRequestBody(r, body)
	s.runCanonicalV1(w, r, req.BaseVersion, s.handleConfigRollback)
}

func (s *Server) handleV1AdoptPreview(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	// ADR 0019 treats this as a body-bearing operation. Decode the accepted
	// request shape strictly even though the canonical assessment reads the
	// external file itself; this pins unknown-field and size semantics.
	var req AdoptExternalRequest
	if _, apiErr := readV1JSON(w, r, &req); apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if s.deps.AdoptExternalPreview == nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotImplemented, "External-adoption preview is unavailable."))
		return
	}
	result, err := s.deps.AdoptExternalPreview()
	if err != nil {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeDriftDetected, "The external configuration cannot be adopted from the current state."))
		return
	}
	writeAPIJSON(w, http.StatusOK, result)
}

func (s *Server) handleV1Adopt(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if s.denyIfFileOwned(w, r, string(ApplyOperationAdoptExternal)) {
		return
	}
	var req AdoptExternalRequest
	body, apiErr := readV1JSON(w, r, &req)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.BaseVersion) == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "base_version is required for this mutation.").WithDetails(adminapi.Details{Field: "base_version"}))
		return
	}
	restoreRequestBody(r, body)
	s.runCanonicalV1(w, r, req.BaseVersion, s.handleAdoptExternal)
}

func (s *Server) handleV1DiscardPendingRestart(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPost) {
		return
	}
	if s.denyIfFileOwned(w, r, "config.stage_restart.discarded") {
		return
	}
	base, apiErr := requiredBaseVersion(r)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	s.runCanonicalV1(w, r, base, s.handleDiscardPendingRestart)
}

func (s *Server) handleV1ClientAddressWrite(w http.ResponseWriter, r *http.Request) {
	if !requireExternalMethod(w, r, http.MethodPatch) {
		return
	}
	if s.denyIfFileOwned(w, r, auditActionClientAddress) {
		return
	}
	var req listenerClientAddressRequest
	body, apiErr := readV1JSON(w, r, &req)
	if apiErr != nil {
		writeAPIError(w, r, apiErr)
		return
	}
	if strings.TrimSpace(req.BaseVersion) == "" {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, "base_version is required for this mutation.").WithDetails(adminapi.Details{Field: "base_version"}))
		return
	}
	restoreRequestBody(r, body)
	s.runCanonicalV1(w, r, req.BaseVersion, s.handleListenerClientAddress)
}

func v1Findings(err error) []adminapi.Finding {
	issues := humanizeErr(err.Error())
	if len(issues) == 0 {
		return []adminapi.Finding{{Code: "candidate_validation", Path: "config", Summary: "The candidate is invalid.", Severity: "error"}}
	}
	out := make([]adminapi.Finding, 0, len(issues))
	for _, issue := range issues {
		out = append(out, adminapi.Finding{Code: issue.Code, Path: issue.Path, Summary: issue.Summary, Detail: issue.Detail, Severity: issue.Severity})
	}
	return out
}

func (s *Server) writeV1PatchExecutionError(w http.ResponseWriter, r *http.Request, err error, base string) {
	var conflictErr *patchVersionConflictError
	if errors.As(err, &conflictErr) {
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStaleBaseVersion, "The configuration changed since this edit was prepared.").WithDetails(adminapi.Details{BaseVersion: base, CurrentVersion: conflictErr.CurrentVersion}))
		return
	}
	var opErr *patchOperationError
	if errors.As(err, &opErr) {
		i := opErr.OpIndex
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeOperationFailed, "A patch operation was rejected.").WithDetails(adminapi.Details{OpIndex: &i, Op: opErr.Op, Errors: v1Findings(opErr.Err)}))
		return
	}
	var candidateErr *patchCandidateError
	if errors.As(err, &candidateErr) {
		if errors.Is(candidateErr, context.DeadlineExceeded) || errors.Is(candidateErr, context.Canceled) {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeOperationTimeout, "Patch assessment did not complete in time."))
			return
		}
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeValidationFailed, "The patch candidate is invalid.").WithDetails(adminapi.Details{Errors: v1Findings(candidateErr)}))
		return
	}
	writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInternalError, "The patch could not be assessed."))
}

// v1Capture is a minimal response writer used only to adapt the canonical
// Console encoder. It never changes the canonical operation's control flow.
type v1Capture struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newV1Capture() *v1Capture { return &v1Capture{header: make(http.Header)} }
func (c *v1Capture) Header() http.Header { return c.header }
func (c *v1Capture) WriteHeader(status int) { if c.status == 0 { c.status = status } }
func (c *v1Capture) Write(p []byte) (int, error) { if c.status == 0 { c.status = http.StatusOK }; return c.body.Write(p) }

func (s *Server) runCanonicalV1(w http.ResponseWriter, r *http.Request, baseVersion string, handler func(http.ResponseWriter, *http.Request)) {
	cap := newV1Capture()
	handler(cap, r)
	if cap.status == 0 {
		cap.status = http.StatusOK
	}
	if cap.status < 400 {
		w.Header().Set("Content-Type", cap.header.Get("Content-Type"))
		w.Header().Set("Cache-Control", "no-store")
		if retry := cap.header.Get("Retry-After"); retry != "" { w.Header().Set("Retry-After", retry) }
		w.WriteHeader(cap.status)
		_, _ = w.Write(cap.body.Bytes())
		return
	}
	// If a shared gate already emitted the external envelope, forward it as-is.
	var env adminapi.Envelope
	if json.Unmarshal(cap.body.Bytes(), &env) == nil && env.Error.Code != "" {
		if _, ok := adminapi.Spec(env.Error.Code); ok {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(cap.status)
			_, _ = w.Write(cap.body.Bytes())
			return
		}
	}

	var body map[string]any
	_ = json.Unmarshal(cap.body.Bytes(), &body)
	message := "The operation was rejected."
	if v, ok := body["message"].(string); ok && v != "" { message = v }
	if v, ok := body["error"].(string); ok && v != "" { message = v }

	switch cap.status {
	case http.StatusBadRequest:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInvalidRequest, message))
	case http.StatusForbidden:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeForbidden, "The principal is not authorized for this operation."))
	case http.StatusNotFound:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotFound, message))
	case http.StatusConflict:
		if current, ok := body["current_version"].(string); ok && current != "" {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStaleBaseVersion, message).WithDetails(adminapi.Details{BaseVersion: baseVersion, CurrentVersion: current}))
			return
		}
		if adminChange, _ := body["admin_change"].(bool); adminChange {
			changes, _ := stringSlice(body["changes"])
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeAdminReachabilityConf, message).WithDetails(adminapi.Details{Changes: changes}))
			return
		}
		if restart, _ := body["restart_required"].(bool); restart {
			subsystems, _ := stringSlice(body["subsystems"])
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeRestartRequired, message).WithDetails(adminapi.Details{Subsystems: subsystems}))
			return
		}
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeDriftDetected, message))
	case http.StatusNotImplemented:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeNotImplemented, "This operation is unavailable in the current build."))
	case http.StatusServiceUnavailable:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeStorageUnavailable, message))
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeOperationTimeout, message))
	default:
		writeAPIError(w, r, adminapi.Errorf(adminapi.CodeInternalError, fmt.Sprintf("The operation failed with HTTP %d.", cap.status)))
	}
}

func stringSlice(v any) ([]string, bool) {
	items, ok := v.([]any)
	if !ok { return nil, false }
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok { return nil, false }
		out = append(out, s)
	}
	return out, true
}
