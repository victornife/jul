// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"net/http"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

// StreamPreflighter abstracts the stream (L4) server's preflight methods so
// the Preflight struct can work with both the real stream.Server and the
// no-op stub in lean builds.
type StreamPreflighter interface {
	PreflightBuild(streams []config.StreamServer, upstreams map[string]config.UpstreamConfig) error
	PreflightListeners(boundKeys map[string]struct{}, next []config.StreamServer) error
	BoundKeys() []string
}

// Preflight encapsulates the admin write validation gate sequence.  It holds
// the two runtime dependencies (handler builder and stream preflighter) that
// cannot be imported as pure functions.
type Preflight struct {
	// BuildHandlers dry-runs the full HTTP handler composition on a config.
	// The bool is commit; for preflight it is always false.
	BuildHandlers func(*config.Config, bool) (map[string]http.Handler, func(), error)

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

// Apply runs the admin write preflight gates:
//
//  1. Structural + stateless validation (ValidateRuntimeConfig).
//  2. TLS certificate file validation (PreflightTLS) — using a resolved clone
//     so secret-referenced cert/key paths (${env:...}, ${file:...}) are expanded
//     before file existence is checked.
//  3. Full HTTP handler dry-run via BuildHandlers (commit=false).
//  4. Stream config dry-run via Stream.PreflightBuild.
//
//  5. HTTP bind probe for newly introduced listen addresses.
//  6. Stream bind probe for newly introduced L4 listeners.
//  7. Restart-required checks (ACME, listener-rebind, tracing, access-log).
//  8. Startup-bound subsystem checks (cache, egress, admin, metrics).
//
// The listener and restart-required gates always run against the live runtime
// snapshot when it is available, even when prev is nil. prev is used only for
// ACME restart-required and optimistic concurrency/history, not as the primary
// baseline for listener state (R10-04).
//
// On success the validated candidate is returned. The caller must pass the
// exact candidate to the live reload so secret sources or the on-disk file
// cannot change between preflight and swap (R8-11).
// Any error aborts the write; the caller must not persist the config.
func (p *Preflight) Apply(c *config.Config, prev *config.Config) (*config.Candidate, error) {
	candidate, err := config.NewCandidate(c)
	if err != nil {
		return nil, err
	}
	if err := ValidateRuntimeConfig(candidate.Effective); err != nil {
		return nil, err
	}
	if err := server.PreflightTLS(candidate.Effective.Servers); err != nil {
		return nil, err
	}
	if err := p.dryRun(candidate.Effective); err != nil {
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
	streamBound := streamBoundKeys(p.Stream)

	if err := server.PreflightListeners(httpBound, candidate.Effective.Servers); err != nil {
		return nil, err
	}
	if err := p.Stream.PreflightListeners(streamBound, candidate.Effective.Streams); err != nil {
		return nil, err
	}

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
		if reason, need := server.ListenerRebindRequired(prev, candidate.Effective); need {
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

	return candidate, nil
}

func boundAddrs(s server.LiveSnapshot) map[string]struct{} {
	m := make(map[string]struct{}, len(s.Listeners))
	for addr := range s.Listeners {
		m[addr] = struct{}{}
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
func (p *Preflight) dryRun(c *config.Config) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("configuration rejected: building it panicked: %v", r)
		}
	}()
	if _, _, err = p.BuildHandlers(c, false); err != nil {
		return err
	}
	return p.Stream.PreflightBuild(c.Streams, IndexUpstreams(c.Upstreams))
}
