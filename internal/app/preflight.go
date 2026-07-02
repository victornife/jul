package app

import (
	"fmt"
	"net/http"

	"jul/internal/admin"
	"jul/internal/config"
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
}

// Apply runs the admin write preflight gates:
//
//  1. Structural + stateless validation (ValidateRuntimeConfig).
//  2. TLS certificate file validation (PreflightTLS).
//  3. Full HTTP handler dry-run via BuildHandlers (commit=false).
//  4. Stream config dry-run via Stream.PreflightBuild.
//
// When prev is non-nil, four additional gates run:
//
//  5. HTTP bind probe for newly introduced listen addresses.
//  6. Stream bind probe for newly introduced L4 listeners.
//  7. Restart-required checks (ACME, listener-rebind, tracing, access-log).
//
// Any error aborts the write; the caller must not persist the config.
func (p *Preflight) Apply(c *config.Config, prev *config.Config) error {
	if err := ValidateRuntimeConfig(c); err != nil {
		return err
	}
	if err := server.PreflightTLS(c.Servers); err != nil {
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
		if reason, need := server.ACMERestartRequired(prev.Servers, c.Servers); need {
			return fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
		if reason, need := server.ListenerRebindRequired(prev, c); need {
			return fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
		if reason, need := server.TracingRestartRequired(prev, c); need {
			return fmt.Errorf("%w: %s", admin.ErrRestartRequired, reason)
		}
		if reason, need := server.AccessLogRestartRequired(prev, c); need {
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
