// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// patchBatchResourceIDs joins every non-empty ResourceID a batch's operations
// recorded (e.g. route_ids location_add minted or accepted), for the audit
// trail's ResourceID field. Empty when no operation touched a durable
// resource identity.
func patchBatchResourceIDs(summaries []patchOperationSummary) string {
	var ids []string
	for _, s := range summaries {
		if s.ResourceID != "" {
			ids = append(ids, s.ResourceID)
		}
	}
	return strings.Join(ids, ",")
}

// patchBatchSelectors is patchBatchResourceIDs' counterpart for Selector: it
// joins every op's revision-scoped selector, populated only when that same
// op had no ResourceID, so an ID-less route's audit trail still names which
// route was touched.
func patchBatchSelectors(summaries []patchOperationSummary) string {
	var selectors []string
	for _, s := range summaries {
		if s.Selector != "" {
			selectors = append(selectors, s.Selector)
		}
	}
	return strings.Join(selectors, ",")
}

// patchBatchBaseline binds an execution to one authoritative editable
// configuration, one effective serving configuration, and one live-listener
// snapshot. Every field is immutable input to executePatchBatch.
type patchBatchBaseline struct {
	Config    *config.Config
	Effective *config.Config
	Version   string
	Live      lifecycle.Live
}

// patchOperationSummary is the ordered typed result of one successfully
// dispatched operation. OpIndex is zero-based to address the request array
// directly without parsing human text.
type patchOperationSummary struct {
	OpIndex int    `json:"op_index"`
	Op      string `json:"op"`
	Summary string `json:"summary"`
	// ResourceID is the route_id the op's target route carries — whether a
	// location_add op created it (caller-supplied or minted) or an existing
	// identified route's op simply reports the identity it already had. A
	// route's route_id never changes as a side effect of any op.
	ResourceID string `json:"resource_id,omitempty"`
	// Selector is the revision-scoped selector (listen, server_names,
	// match_type, path, match_ordinal) for a route-targeting op whose target
	// has no route_id — populated only when ResourceID is empty, so an audit
	// event for an ID-less route still carries a stable, non-value-bearing
	// reference to which route it was, per ADR 0019 §7. Never carries
	// predicate/header/query values.
	Selector string `json:"selector,omitempty"`
}

// patchBatchExecution is the complete side-effect-free result shared by the
// preview, privileged candidate, and apply routes. Only apply proceeds to a
// later persistence/action stage.
type patchBatchExecution struct {
	BaseVersion string

	BeforeRaw       []byte
	BeforeConfig    *config.Config
	BeforeEffective *config.Config

	CandidateRaw       []byte
	CandidateConfig    *config.Config
	CandidateEffective *config.Config
	Candidate          *config.Candidate

	OperationSummaries []patchOperationSummary
	ValidationErrors   []validationError
	Diff               ConfigDiff
	Lifecycle          lifecycle.Result

	Valid bool
}

func (e patchBatchExecution) summaryStrings() []string {
	out := make([]string, 0, len(e.OperationSummaries))
	for _, summary := range e.OperationSummaries {
		out = append(out, summary.Summary)
	}
	return out
}

func (e patchBatchExecution) summaryText() string {
	return joinPatchSummaries(e.OperationSummaries)
}

func joinPatchSummaries(summaries []patchOperationSummary) string {
	if len(summaries) == 0 {
		return ""
	}
	out := summaries[0].Summary
	for i := 1; i < len(summaries); i++ {
		out += "; " + summaries[i].Summary
	}
	return out
}

// patchBaselineError identifies failure to bind the authoritative editable
// configuration before any operation is dispatched.
type patchBaselineError struct {
	Err error
}

func (e *patchBaselineError) Error() string { return e.Err.Error() }
func (e *patchBaselineError) Unwrap() error { return e.Err }

// patchVersionConflictError reports an optimistic-concurrency mismatch before
// any operation is dispatched.
type patchVersionConflictError struct {
	CurrentVersion string
	// Message overrides the default explanation. It is set for the one conflict
	// that is not a stale version but a missing one: an ordinal-bearing patch
	// with no base_version at all.
	Message string
}

func (e *patchVersionConflictError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return "configuration changed since the patch batch was prepared"
}

// patchOperationError identifies the exact operation that rejected an atomic
// batch. It deliberately carries no candidate bytes.
type patchOperationError struct {
	OpIndex int
	Op      string
	Err     error
}

func (e *patchOperationError) Error() string {
	return fmt.Sprintf("patch operation %d (%s): %v", e.OpIndex, e.Op, e.Err)
}

func (e *patchOperationError) Unwrap() error { return e.Err }

// patchCandidateError identifies a failure while resolving or validating the
// complete candidate. It unwraps context errors so handlers retain their
// existing 408/504 behavior.
type patchCandidateError struct {
	Err error
}

func (e *patchCandidateError) Error() string { return e.Err.Error() }
func (e *patchCandidateError) Unwrap() error { return e.Err }

// executePatchBatch is the sole ordered patch-batch executor. It is pure with
// respect to repository and runtime state: it clones, dispatches, canonicalizes,
// resolves, validates, diffs, and classifies private values only.
func executePatchBatch(
	ctx context.Context,
	baseline patchBatchBaseline,
	requestedVersion string,
	ops []patchRequest,
) (patchBatchExecution, error) {
	var out patchBatchExecution
	if ctx == nil {
		ctx = context.Background()
	}
	if baseline.Config == nil {
		return out, errors.New("patch batch: baseline configuration is nil")
	}
	if len(ops) == 0 {
		return out, errors.New("patch batch: at least one operation is required")
	}
	if err := ctx.Err(); err != nil {
		return out, &patchCandidateError{Err: err}
	}

	beforeRaw, err := config.Marshal(baseline.Config)
	if err != nil {
		return out, fmt.Errorf("patch batch: marshal baseline: %w", err)
	}
	baseVersion := baseline.Version
	if baseVersion == "" {
		baseVersion = configVersion(beforeRaw)
	}
	if requestedVersion != "" && requestedVersion != baseVersion {
		return out, &patchVersionConflictError{CurrentVersion: baseVersion}
	}
	// An empty base_version is an explicit force-apply, which is safe for a
	// coordinate tuple that names a route and unsafe for an ordinal: inserting a
	// same-path route above the target shifts every later ordinal, so a
	// force-applied ordinal patch edits a route the operator never previewed.
	// Requiring the CAS binding turns that race into a 409 (ADR 0018 §14).
	if requestedVersion == "" {
		for i, op := range ops {
			if op.MatchOrdinal == nil {
				continue
			}
			return out, &patchVersionConflictError{
				CurrentVersion: baseVersion,
				Message: fmt.Sprintf("operation %d (%s) selects a route by match_ordinal, which requires base_version",
					i+1, op.Op),
			}
		}
	}

	// Reparse the canonical form before cloning. This makes preview and apply
	// operate on the same logical representation regardless of comments,
	// aliases, or a loader-owned in-memory pointer.
	beforeConfig, err := config.Parse(beforeRaw)
	if err != nil {
		return out, fmt.Errorf("patch batch: reparse canonical baseline: %w", err)
	}
	candidateConfig, err := beforeConfig.Clone()
	if err != nil {
		return out, fmt.Errorf("patch batch: clone baseline: %w", err)
	}

	operationSummaries := make([]patchOperationSummary, 0, len(ops))
	for i, op := range ops {
		if err := ctx.Err(); err != nil {
			return out, &patchCandidateError{Err: err}
		}
		// Route-targeting ops (everything except location_add, which has no
		// target until it runs) are resolved against the *pre-op* candidate:
		// route_id is immutable outside minting, so this is a stable identity
		// even for an op like location_remove or location_set_match, whose
		// own effect would make the target unresolvable afterward.
		preOpIdentity := resolveRouteAuditIdentity(candidateConfig, op.locationTarget())
		summary, applyErr := applyPatch(candidateConfig, op)
		if applyErr != nil {
			return out, &patchOperationError{OpIndex: i, Op: op.Op, Err: applyErr}
		}
		opSummary := patchOperationSummary{
			OpIndex: i,
			Op:      op.Op,
			Summary: summary,
		}
		switch {
		case op.Op == "location_add":
			if srv, err := findServerByNames(candidateConfig, op.Listen, op.ServerNames); err == nil && len(srv.Locations) > 0 {
				opSummary.ResourceID, opSummary.Selector = auditIdentityOf(op.Listen, op.ServerNames, &srv.Locations[len(srv.Locations)-1])
			}
		case preOpIdentity != (routeAuditIdentity{}):
			opSummary.ResourceID, opSummary.Selector = preOpIdentity.ResourceID, preOpIdentity.Selector
		}
		operationSummaries = append(operationSummaries, opSummary)
	}

	candidateRaw, err := config.Marshal(candidateConfig)
	if err != nil {
		return out, fmt.Errorf("patch batch: marshal candidate: %w", err)
	}
	candidateConfig, err = config.Parse(candidateRaw)
	if err != nil {
		return out, fmt.Errorf("patch batch: reparse canonical candidate: %w", err)
	}

	beforeEffective := baseline.Effective
	if beforeEffective != nil {
		beforeEffective, err = beforeEffective.Clone()
		if err != nil {
			return out, fmt.Errorf("patch batch: clone effective baseline: %w", err)
		}
	} else {
		beforeCandidate, resolveErr := config.NewCandidateContext(ctx, beforeConfig)
		if resolveErr != nil {
			return out, &patchCandidateError{Err: resolveErr}
		}
		beforeEffective = beforeCandidate.Effective
	}

	candidate, err := config.NewCandidateContext(ctx, candidateConfig)
	if err != nil {
		return out, &patchCandidateError{Err: err}
	}

	validationErrors := make([]validationError, 0)
	if validateErr := validateEffectiveConfig(ctx, candidate.Effective); validateErr != nil {
		if errors.Is(validateErr, context.Canceled) || errors.Is(validateErr, context.DeadlineExceeded) {
			return out, &patchCandidateError{Err: validateErr}
		}
		validationErrors = append(validationErrors, humanizeErr(validateErr.Error())...)
	}

	classification, err := lifecycle.Classify(beforeEffective, candidate.Effective, baseline.Live)
	if err != nil {
		return out, fmt.Errorf("patch batch: classify lifecycle: %w", err)
	}

	out = patchBatchExecution{
		BaseVersion:        baseVersion,
		BeforeRaw:          beforeRaw,
		BeforeConfig:       beforeConfig,
		BeforeEffective:    beforeEffective,
		CandidateRaw:       candidateRaw,
		CandidateConfig:    candidateConfig,
		CandidateEffective: candidate.Effective,
		Candidate:          candidate,
		OperationSummaries: operationSummaries,
		ValidationErrors:   validationErrors,
		Diff:               diffConfigs(beforeConfig, candidateConfig),
		Lifecycle:          classification,
		Valid:              len(validationErrors) == 0 && len(classification.ValidationRejected) == 0,
	}
	return out, nil
}

// executeCurrentPatchBatch binds the authoritative editable baseline and one
// live runtime snapshot, then invokes the sole executor. Apply calls it under
// applyMu with requireRaw=true; read-only routes use requireRaw=false.
func (s *Server) executeCurrentPatchBatch(
	ctx context.Context,
	reqCtx *ApplyRequestContext,
	requireRaw bool,
	requestedVersion string,
	ops []patchRequest,
) (MutationBaseline, patchBatchExecution, error) {
	state, err := s.currentWriteState(requireRaw)
	if err != nil {
		return MutationBaseline{}, patchBatchExecution{}, &patchBaselineError{Err: err}
	}
	if reqCtx != nil {
		reqCtx.Baseline = &state
	}
	effective, live := s.patchRuntimeBaseline(reqCtx, state.Config)
	execution, err := executePatchBatch(ctx, patchBatchBaseline{
		Config:    state.Config,
		Effective: effective,
		Version:   state.Version,
		Live:      live,
	}, requestedVersion, ops)
	if err != nil {
		return state, patchBatchExecution{}, err
	}
	if reqCtx != nil {
		reqCtx.Candidate = execution.Candidate
	}
	return state, execution, nil
}

// patchRuntimeBaseline captures the effective config and bound listener
// addresses in one LiveSnapshot call. Apply also binds the snapshot and auth
// generations into its existing immutable request context.
func (s *Server) patchRuntimeBaseline(reqCtx *ApplyRequestContext, _ *config.Config) (*config.Config, lifecycle.Live) {
	if reqCtx != nil {
		reqCtx.AuthGeneration = s.AuthGeneration()
	}
	if s.deps.LiveSnapshot == nil {
		return nil, lifecycle.Live{BoundHTTPAddrs: []string{}}
	}

	snapshot := s.deps.LiveSnapshot()
	if reqCtx != nil {
		reqCtx.LiveGeneration = snapshot.Generation
	}
	addrs := make([]string, 0, len(snapshot.Listeners))
	for addr := range snapshot.Listeners {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	effective := snapshot.EffectiveConfig
	if effective == nil {
		// A missing effective snapshot is not equivalent to the unresolved raw
		// config. Let the executor resolve the authoritative baseline itself.
		return nil, lifecycle.Live{BoundHTTPAddrs: addrs}
	}
	return effective, lifecycle.Live{BoundHTTPAddrs: addrs}
}
