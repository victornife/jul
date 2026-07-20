// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/observability"
	"jul/internal/rbac"
	"jul/internal/server"
)

// PreflightMode selects which validation gates run in Preflight.Apply.
type PreflightMode int

const (
	// PreflightHot runs all gates including restart-required rejection. It is
	// the mode used by the managed hot-apply path.
	PreflightHot PreflightMode = iota
	// PreflightStageRestart runs all shared gates plus startup-resource
	// preflights, but classifies lifecycle differences instead of rejecting
	// them. It is the mode used by the stage_restart apply path.
	PreflightStageRestart
)

// PreflightResult is the structured output of a successful Preflight.Apply.
type PreflightResult struct {
	// Candidate is the immutable resolved config that must be handed to the
	// live reload so secrets are not re-resolved between preflight and swap.
	Candidate *config.Candidate
	// Lifecycle contains the registered configuration fields whose effective
	// value changed relative to prev. It is populated only when prev is
	// non-nil and mode is PreflightStageRestart; it is used by the coordinator
	// to build the pending-restart marker without re-running the diff.
	Lifecycle lifecycle.ChangeSet
}

// StreamPreflighter abstracts the stream (L4) server's preflight methods so
// the Preflight struct can work with both the real stream.Server and the
// no-op stub in lean builds.
type StreamPreflighter interface {
	PreflightBuild(ctx context.Context, streams []config.StreamServer, upstreams map[string]config.UpstreamConfig) error
	PreflightListeners(ctx context.Context, boundKeys map[string]struct{}, next []config.StreamServer) error
	BoundKeys() []string
}

// Preflight encapsulates the admin write validation gate sequence.  It holds
// the two runtime dependencies (handler builder and stream preflighter) that
// cannot be imported as pure functions.
type Preflight struct {
	// BuildHandlers dry-runs the full HTTP handler composition on a config.
	// The bool is commit; for preflight it is always false.
	BuildHandlers func(context.Context, *config.Config, bool) (map[string]http.Handler, func(), error)

	// Stream validates stream (L4) configuration without binding sockets.
	Stream StreamPreflighter

	// StartupFP is the effective startup fingerprint used for restart-required
	// checks. When empty, restart-required classification is skipped so tests
	// without a startup baseline are not rejected.
	StartupFP lifecycle.Fingerprint

	// LiveSnapshot returns the current HTTP server runtime state: effective
	// config, bound listener fingerprints, and generation. When nil, preflight
	// falls back to the on-disk prev baseline (legacy behaviour).
	LiveSnapshot func() server.LiveSnapshot
}

// Apply runs the admin write preflight gates and returns a PreflightResult on
// success. ctx is reserved for future use and may be context.Background().
//
// Shared gates (both modes):
//
//  1. Structural + stateless validation (ValidateRuntimeConfig).
//  2. TLS certificate file validation (PreflightTLS).
//  3. Full HTTP handler dry-run via BuildHandlers (commit=false).
//  4. Stream config dry-run via Stream.PreflightBuild.
//  5. HTTP bind probe for newly introduced listen addresses.
//  6. Stream bind probe for newly introduced L4 listeners.
//
// Hot-only additional gates (PreflightHot):
//
//  7. Restart-required fingerprint check.
//  8. Runtime-aware rebind check for frozen settings on kept listeners.
//  9. ACME restart-required check.
//
// Stage-restart additional gates (PreflightStageRestart):
//
//  7. Startup-resource preflights (cache, admin, access-log, tracing, ACME).
//  8. Lifecycle diff classification (populates PreflightResult.Lifecycle).
//
// Any error aborts the write; the caller must not persist the config.
func (p *Preflight) Apply(ctx context.Context, c *config.Config, prev *config.Config, mode PreflightMode) (*PreflightResult, error) {
	candidate, err := config.NewCandidate(c)
	if err != nil {
		return nil, err
	}
	if err := ValidateRuntimeConfig(candidate.Effective); err != nil {
		return nil, err
	}
	// RBAC policy build is part of the reload contract (F-09): validate it
	// now, after secrets expansion, so token/role/permission errors that only
	// surface during policy construction are caught before persistence.
	if err := validateAdminRBACPolicy(candidate.Effective.Admin); err != nil {
		return nil, err
	}
	if err := server.PreflightTLS(candidate.Effective.Servers); err != nil {
		return nil, err
	}
	if err := p.dryRun(ctx, candidate.Effective); err != nil {
		return nil, err
	}

	// Use the live runtime snapshot as the authoritative baseline for
	// listener-related gates. If unavailable, fall back to the on-disk prev
	// config (legacy path).
	var live server.LiveSnapshot
	if p.LiveSnapshot != nil {
		live = p.LiveSnapshot()
	}
	httpBound := boundAddrs(live)
	http3Bound := boundHTTP3Addrs(live)
	streamBound := streamBoundKeys(p.Stream)

	// Probe only addresses NOT currently bound (new listeners). Occupied
	// addresses are checked for frozen-setting changes in the hot path only;
	// the stage-restart path classifies those changes rather than rejecting
	// them.
	if err := server.PreflightListeners(httpBound, http3Bound, candidate.Effective.Servers); err != nil {
		return nil, err
	}
	if err := p.Stream.PreflightListeners(ctx, streamBound, candidate.Effective.Streams); err != nil {
		return nil, err
	}

	result := &PreflightResult{Candidate: candidate}

	if mode == PreflightStageRestart {
		// Classify lifecycle changes; do not reject them.
		if prev != nil {
			result.Lifecycle = lifecycle.DiffConfig(prev, candidate.Effective)
		}
		// Validate that startup-consumed resources can be applied at the next
		// restart. These checks are side-effect-minimized: they create and
		// immediately remove a temp file to prove writability, then close all
		// handles.
		if err := startupResourcePreflight(candidate.Effective); err != nil {
			return nil, err
		}
		return result, nil
	}

	// ── Hot-apply gates ────────────────────────────────────────────────────

	// Restart-required classification is single-sourced from the lifecycle
	// registry. Compare the candidate's effective fingerprint against the
	// startup-bound fingerprint so secret-content rotation and effective
	// value changes are detected without duplicating the check list.
	if len(p.StartupFP.Values) > 0 {
		candidateFP := lifecycle.ComputeFingerprint(candidate.Effective)
		if reason, need := lifecycle.RestartRequired(p.StartupFP, candidateFP); need {
			return nil, fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
	}

	// Runtime-aware rebind check: compare the candidate against actually
	// bound listeners. Fall back to the config-only check when no snapshot
	// is available.
	if p.LiveSnapshot != nil {
		if reason, need := server.PreflightRebindRequired(live, candidate.Effective); need {
			return nil, fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
	} else if prev != nil {
		prevCandidate, err := config.NewCandidate(prev)
		if err != nil {
			return nil, err
		}
		if reason, need := server.ListenerRebindRequired(prevCandidate.Effective, candidate.Effective); need {
			return nil, fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
	}

	// ACME restart-required uses prev's server list only for the comparison.
	// When prev is unavailable, compare against an empty server list.
	var prevServers []config.ServerConfig
	if prev != nil {
		prevServers = prev.Servers
	}
	if reason, need := server.ACMERestartRequired(prevServers, candidate.Effective.Servers); need {
		return nil, fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
	}

	return result, nil
}

// startupResourcePreflight runs side-effect-minimized preflights for all
// startup-consumed subsystems that stage_restart mode must validate. Each
// check creates and immediately removes a temporary file to prove writability
// without retaining any handle, starting any goroutine, or contacting external
// services.
func startupResourcePreflight(cfg *config.Config) error {
	if err := cache.Preflight(cfg.Cache); err != nil {
		return err
	}
	if err := admin.PreflightConfig(cfg.Admin); err != nil {
		return err
	}
	if err := observability.PreflightAccessSinks(cfg.Observability.AccessLog); err != nil {
		return err
	}
	if err := observability.ValidateTracerConfig(cfg.Observability.Tracing); err != nil {
		return err
	}
	if err := server.PreflightACMEStartup(cfg.Servers); err != nil {
		return err
	}
	return nil
}

// validateAdminRBACPolicy builds the RBAC policy from the resolved admin
// config. This catches construction-time failures (duplicate token IDs after
// secret expansion, custom role permission expansion, missing admin-capable
// principals, etc.) before the config is persisted (F-09).
func validateAdminRBACPolicy(a config.AdminConfig) error {
	if !a.RBAC.Enabled {
		return nil
	}
	customRoles := make(map[string][]string, len(a.RBAC.Roles))
	for _, r := range a.RBAC.Roles {
		customRoles[r.Name] = r.Permissions
	}
	principals := make([]rbac.PrincipalDef, 0, len(a.RBAC.Principals))
	for _, p := range a.RBAC.Principals {
		principals = append(principals, rbac.PrincipalDef{
			Name:      p.Name,
			Role:      p.Role,
			Token:     p.Token,
			Disabled:  p.Disabled,
			ExpiresAt: p.ExpiresAt,
		})
	}
	if _, err := rbac.Build(
		a.RBAC.Enabled,
		a.RBAC.DefaultRole,
		customRoles,
		principals,
		a.Token,
		time.Now(),
	); err != nil {
		return fmt.Errorf("rbac policy: %w", err)
	}
	return nil
}

func boundAddrs(s server.LiveSnapshot) map[string]struct{} {
	m := make(map[string]struct{}, len(s.Listeners))
	for addr := range s.Listeners {
		m[addr] = struct{}{}
	}
	return m
}

func boundHTTP3Addrs(s server.LiveSnapshot) map[string]struct{} {
	m := make(map[string]struct{}, len(s.Listeners))
	for addr, info := range s.Listeners {
		if info.H3 {
			m[addr] = struct{}{}
		}
	}
	return m
}

func streamBoundKeys(sp StreamPreflighter) map[string]struct{} {
	keys := sp.BoundKeys()
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

// dryRun builds all handlers (commit=false) on the already-resolved effective
// config and validates the stream configuration. A panic during handler
// construction is recovered and returned as an error so a malformed config
// cannot crash the admin goroutine.
func (p *Preflight) dryRun(ctx context.Context, c *config.Config) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("configuration rejected: building it panicked: %v", r)
		}
	}()
	if _, _, err = p.BuildHandlers(ctx, c, false); err != nil {
		return err
	}
	return p.Stream.PreflightBuild(ctx, c.Streams, IndexUpstreams(c.Upstreams))
}
