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
	PreflightListeners(old, next []config.StreamServer) error
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
// When prev is non-nil, eight additional gates run:
//
//  5. HTTP bind probe for newly introduced listen addresses.
//  6. Stream bind probe for newly introduced L4 listeners.
//  7. Restart-required checks (ACME, listener-rebind, tracing, access-log).
//  8. Startup-bound subsystem checks (cache, egress, admin, metrics).
//
// Any error aborts the write; the caller must not persist the config.
func (p *Preflight) Apply(c *config.Config, prev *config.Config) error {
	if err := ValidateRuntimeConfig(c); err != nil {
		return err
	}
	// Resolve secrets into a clone so secret-referenced cert/key paths are
	// expanded before PreflightTLS checks file existence. The returned redaction
	// state is discarded: preflight must not mutate the serving redaction
	// registry (R5-01).
	resolvedForTLS := c
	if expanded, _, _, rerr := config.Resolve(c); rerr == nil {
		resolvedForTLS = expanded
	}
	if err := server.PreflightTLS(resolvedForTLS.Servers); err != nil {
		return err
	}
	if err := p.dryRun(c); err != nil {
		return err
	}
	if prev != nil {
		if err := server.PreflightListeners(prev.Servers, c.Servers); err != nil {
			return err
		}
		if err := p.Stream.PreflightListeners(prev.Streams, c.Streams); err != nil {
			return err
		}
		// Restart-required classification is single-sourced from the lifecycle
		// registry. Compare the candidate's effective fingerprint against the
		// startup-bound fingerprint so secret-content rotation and effective
		// value changes are detected without duplicating the check list.
		if len(p.StartupFP.Values) > 0 {
			candidateFP := lifecycle.ComputeFingerprint(resolvedForTLS)
			if reason, need := lifecycle.RestartRequired(p.StartupFP, candidateFP); need {
				return fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
			}
		}
		if reason, need := server.ListenerRebindRequired(prev, c); need {
			return fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
	}
	return nil
}

// dryRun clones the config, expands secrets in place, builds all handlers
// (commit=false), and validates the stream configuration.  A panic during
// handler construction is recovered and returned as an error so a malformed
// config cannot crash the admin goroutine.
func (p *Preflight) dryRun(c *config.Config) (err error) {
	clone, cerr := c.Clone()
	if cerr != nil {
		return fmt.Errorf("clone config for preflight: %w", cerr)
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("configuration rejected: building it panicked: %v", r)
		}
	}()
	if _, _, err = p.BuildHandlers(clone, false); err != nil {
		return err
	}
	return p.Stream.PreflightBuild(clone.Streams, IndexUpstreams(clone.Upstreams))
}
