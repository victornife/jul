// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"

	"jul/internal/adminapi"
)

// runExternalMutationV1 applies idempotency before the canonical operation and
// projects the canonical Console result into the deliberately bounded v1 DTO
// afterwards. Authority and request admission happen in the caller before this
// point, so a denied or malformed request never creates an idempotency binding.
func (s *Server) runExternalMutationV1(
	w http.ResponseWriter,
	r *http.Request,
	baseVersion string,
	requestBody []byte,
	handler func(http.ResponseWriter, *http.Request),
) {
	cap := newV1Capture()
	projectedReplay := s.runIdempotentCanonicalV1(cap, r, baseVersion, requestBody, handler)
	if cap.status < http.StatusBadRequest && !projectedReplay {
		s.projectV1MutationCapture(cap, r)
	}
	writeCapturedV1(w, cap)
}

func (s *Server) projectV1MutationCapture(cap *v1Capture, r *http.Request) {
	var result ConfigApplyResult
	if err := json.Unmarshal(cap.body.Bytes(), &result); err != nil {
		cap.reset()
		writeAPIError(cap, r, adminapi.Errorf(adminapi.CodeInternalError,
			"The canonical mutation returned an invalid success payload."))
		return
	}

	response := s.v1ConfigApplyResponse(result, cap.status)
	body, err := json.Marshal(response)
	if err != nil {
		cap.reset()
		writeAPIError(cap, r, adminapi.Errorf(adminapi.CodeInternalError,
			"The external mutation result could not be encoded."))
		return
	}

	cap.body.Reset()
	_, _ = cap.body.Write(body)
	_ = cap.body.WriteByte('\n')
	cap.header.Set("Content-Type", "application/json; charset=utf-8")
	cap.header.Set("Cache-Control", "no-store")
}

func (c *v1Capture) reset() {
	c.header = make(http.Header)
	c.status = 0
	c.body.Reset()
}

func (s *Server) v1ConfigApplyResponse(result ConfigApplyResult, status int) adminapi.ConfigApplyResponse {
	terminal := status != http.StatusAccepted
	state := "terminal"
	if !terminal {
		state = "pending"
	}

	outcome := result.AppOutcome
	if outcome == "" && result.Reload != nil {
		outcome = string(result.Reload.Outcome)
	}
	if outcome == "" && result.OK && result.PendingRestart != nil && result.PendingRestart.State == "managed_staged" {
		outcome = "staged"
	}

	persistedVersion := result.FinalDiskVersion
	if persistedVersion == "" {
		persistedVersion = result.PersistedVersion
	}
	if persistedVersion == "" {
		persistedVersion = result.Version
	}
	servingVersion := result.FinalServingVersion
	if servingVersion == "" {
		servingVersion = result.ServingVersion
	}

	degraded := make([]adminapi.Degradation, 0, len(result.Degraded))
	for _, d := range result.Degraded {
		degraded = append(degraded, adminapi.Degradation{Kind: d.Kind, Message: d.Message})
	}

	response := adminapi.ConfigApplyResponse{
		APIVersion:       adminapi.APIVersion,
		ApplyID:          result.ApplyID,
		State:            state,
		Terminal:         terminal,
		OK:               result.OK,
		Mode:             result.Mode,
		Outcome:          outcome,
		PersistedVersion: persistedVersion,
		DesiredVersion:   result.DesiredVersion,
		ServingVersion:   servingVersion,
		ConfigState:      result.ConfigState,
		Origin:           result.Origin,
		RestartRequired:  result.RestartRequired,
		CanStage:         result.CanStage,
		Restored:         result.Restored,
		RestoreError:     result.RestoreError,
		TimedOutPhase:    result.TimedOutPhase,
		Degraded:         degraded,
		BootID:           s.bootID(),
	}
	if result.PendingRestart != nil {
		response.PendingRestart = v1PendingRestartFromApply(result.PendingRestart)
	}
	return response
}

func v1PendingRestartFromApply(st *PendingRestartStatus) *adminapi.PendingRestartState {
	if st == nil {
		return nil
	}
	return &adminapi.PendingRestartState{
		Pending:          st.State != "" && st.State != "none",
		State:            st.State,
		StagedAt:         st.StagedAt,
		StagedVersion:    st.StagedVersion,
		PersistedVersion: st.PersistedVersion,
		ServingVersion:   st.ServingVersion,
		Subsystems:       append([]string(nil), st.Subsystems...),
		DiscardAvailable: st.DiscardAvailable,
	}
}
