// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

const rawPreviewBaseVersionHeader = "X-Jul-Base-Version"

var errRawCandidateSyntax = errors.New("raw candidate TOML could not be parsed")

type rawConfigPreviewAssessment struct {
	BaseVersion      string
	Valid            bool
	ValidationErrors []validationError
	Diff             ConfigDiff
	Lifecycle        lifecycle.Result
}

type rawConfigPreviewResponse struct {
	OK               bool                  `json:"ok"`
	BaseVersion      string                `json:"base_version"`
	Valid            bool                  `json:"valid"`
	ValidationErrors []validationError     `json:"validation_errors"`
	Diff             ConfigDiff            `json:"diff"`
	Lifecycle        patchLifecycleSummary `json:"lifecycle"`
}

// secretSafeRawValidationErrors preserves only a validator-derived field path.
// The original summary/detail may contain a configured literal, so raw preview
// never serializes either one. This keeps field-level UX where the validator can
// identify a path without weakening the config:raw secret boundary.
func secretSafeRawValidationErrors(err error) []validationError {
	if err == nil {
		return nil
	}
	humanized := humanizeErr(err.Error())
	if len(humanized) == 0 {
		humanized = []validationError{{Path: "config"}}
	}
	out := make([]validationError, 0, len(humanized))
	for _, issue := range humanized {
		path := issue.Path
		if path == "" {
			path = "config"
		}
		out = append(out, validationError{
			Code:     "candidate_validation",
			Path:     path,
			Summary:  "The candidate value is invalid.",
			Detail:   "Review this field and generate a fresh preview.",
			Severity: "error",
		})
	}
	return out
}

// previewRawCandidate is the value-free, side-effect-free raw candidate
// assessment used by the cache editor. The caller binds before/base/live once;
// candidate bytes never appear in the response or in an error.
func previewRawCandidate(
	ctx context.Context,
	before *config.Config,
	beforeEffective *config.Config,
	live lifecycle.Live,
	baseVersion string,
	candidateRaw []byte,
) (rawConfigPreviewAssessment, error) {
	var out rawConfigPreviewAssessment
	if ctx == nil {
		ctx = context.Background()
	}
	if before == nil {
		return out, errors.New("raw preview: baseline configuration is nil")
	}
	candidateConfig, err := config.Parse(candidateRaw)
	if err != nil {
		// Do not echo parser text: a malformed line can contain a literal secret.
		return out, errRawCandidateSyntax
	}
	candidate, err := config.NewCandidateContext(ctx, candidateConfig)
	if err != nil {
		return out, err
	}
	if beforeEffective == nil {
		baselineCandidate, resolveErr := config.NewCandidateContext(ctx, before)
		if resolveErr != nil {
			return out, resolveErr
		}
		beforeEffective = baselineCandidate.Effective
	}

	validationErrors := make([]validationError, 0)
	if validateErr := validateEffectiveConfig(ctx, candidate.Effective); validateErr != nil {
		if errors.Is(validateErr, context.Canceled) || errors.Is(validateErr, context.DeadlineExceeded) {
			return out, validateErr
		}
		validationErrors = append(validationErrors, secretSafeRawValidationErrors(validateErr)...)
	}
	classification, err := lifecycle.Classify(beforeEffective, candidate.Effective, live)
	if err != nil {
		return out, err
	}
	out = rawConfigPreviewAssessment{
		BaseVersion:      baseVersion,
		Valid:            len(validationErrors) == 0 && len(classification.ValidationRejected) == 0,
		ValidationErrors: validationErrors,
		Diff:             diffConfigs(before, candidateConfig),
		Lifecycle:        classification,
	}
	return out, nil
}

// handleConfigPreview classifies a raw candidate against the exact persisted or
// managed-staged baseline named by X-Jul-Base-Version. It never persists,
// reloads, logs, or returns candidate TOML.
func (s *Server) handleConfigPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	requestedVersion := strings.TrimSpace(r.Header.Get(rawPreviewBaseVersionHeader))
	if requestedVersion == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"ok":      false,
			"message": "A pinned base version is required for raw preview.",
		})
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "message": "Could not read the candidate."})
		return
	}
	state, err := s.currentWriteState(true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "message": "The editable configuration is unavailable."})
		return
	}
	if requestedVersion != state.Version {
		writeJSON(w, http.StatusConflict, map[string]any{
			"ok":              false,
			"conflict":        true,
			"current_version": state.Version,
			"message":         "The configuration changed since this raw candidate was generated.",
		})
		return
	}
	effective, live := s.patchRuntimeBaseline(nil, state.Config)
	assessment, err := previewRawCandidate(r.Context(), state.Config, effective, live, state.Version, body)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			writeJSON(w, http.StatusRequestTimeout, map[string]any{"ok": false, "message": "Raw preview timed out."})
			return
		}
		if errors.Is(err, errRawCandidateSyntax) {
			writeJSON(w, http.StatusBadRequest, validationErrorResponse{
				OK:      false,
				Message: "The candidate configuration contains invalid TOML.",
				Errors: []validationError{{
					Code:     "toml_syntax",
					Path:     "config",
					Summary:  "The candidate TOML could not be parsed.",
					Detail:   "Correct the syntax and generate a fresh preview.",
					Severity: "error",
				}},
			})
			return
		}
		writeJSON(w, http.StatusBadRequest, validationErrorResponse{
			OK:      false,
			Message: "The candidate configuration could not be assessed.",
			Errors: []validationError{{
				Code:     "candidate_assessment",
				Path:     "config",
				Summary:  "The candidate could not be resolved or classified.",
				Detail:   "Review referenced files, secrets, and external dependencies, then retry.",
				Severity: "error",
			}},
		})
		return
	}
	writeJSON(w, http.StatusOK, rawConfigPreviewResponse{
		OK:               true,
		BaseVersion:      assessment.BaseVersion,
		Valid:            assessment.Valid,
		ValidationErrors: assessment.ValidationErrors,
		Diff:             assessment.Diff,
		Lifecycle:        s.patchLifecycleProjection(assessment.Lifecycle, assessment.Valid),
	})
}
