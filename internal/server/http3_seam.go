// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"errors"
	"net/http"

	"jul/internal/config"
)

// h3Listener is the lifecycle handle for a staged HTTP/3 (QUIC) listener. The
// concrete type is built only in http3-tagged builds (see http3.go); the stub
// build (http3_stub.go) never produces a value, so this stays a small interface
// the untagged server lifecycle can hold, Activate, and Close without importing
// quic-go. Activation starts the accept loop; until Activate the QUIC socket
// exists but does not serve.
type h3Listener interface {
	// Activate starts accepting QUIC connections and serving HTTP/3. It must be
	// called only after the handler generation that covers this address has been
	// published, so in-flight requests see the correct handlers immediately.
	Activate() error

	// Close stops serving HTTP/3 and releases the UDP socket, draining in-flight
	// requests until ctx is done. The server lifecycle derives ctx from the
	// configured shutdown_timeout so HTTP/3 drains on the same budget as the
	// TCP listeners.
	Close(ctx context.Context) error

	// SetOnExit installs a callback the accept loop invokes at most once, with
	// the error that ended it, if it exits for any reason other than an
	// intentional Close (#161: this is what lets a live H3 failure transition
	// Alt-Svc to "clear" instead of continuing to advertise a dead listener).
	// Must be called before Activate to avoid a race with the accept loop
	// starting; the single serial reload loop already guarantees that order.
	SetOnExit(func(error))
}

// CheckHTTP3 reports whether the configuration can be served by this binary with
// respect to HTTP/3. HTTP/3 support is a build-time choice (the "http3" tag); a
// binary without it cannot open a QUIC listener. When such a binary is given a
// configuration that enables HTTP/3, this returns an error so startup fails fast
// with a clear, actionable message instead of silently serving only TCP. It is
// a no-op (returns nil) in http3-enabled builds or when no server enables it.
func CheckHTTP3(servers []config.ServerConfig) error {
	if http3Compiled {
		return nil
	}
	for i := range servers {
		if servers[i].HTTP3 != nil && servers[i].HTTP3.Enabled {
			return errors.New("http3 is enabled in the configuration but this binary was built without HTTP/3 support; rebuild with -tags http3")
		}
	}
	return nil
}

// http3EnabledForAddr reports whether any server block on addr enables HTTP/3.
// Validation guarantees such a block also has TLS, and HTTP/3 only starts inside
// the TLS branch of bind, so this drives whether a QUIC listener is opened.
func (s *Server) http3EnabledForAddr(addr string) bool {
	for i := range s.cfg.Servers {
		srv := &s.cfg.Servers[i]
		if srv.Listen == addr && srv.HTTP3 != nil && srv.HTTP3.Enabled {
			return true
		}
	}
	return false
}

// http3MaxAgeForAddr returns the Alt-Svc max-age (seconds) for the first server
// block on addr that enables HTTP/3, defaulting to 86400 when unset.
func (s *Server) http3MaxAgeForAddr(addr string) int {
	for i := range s.cfg.Servers {
		srv := &s.cfg.Servers[i]
		if srv.Listen == addr && srv.HTTP3 != nil && srv.HTTP3.Enabled {
			if srv.HTTP3.AltSvcMaxAge > 0 {
				return srv.HTTP3.AltSvcMaxAge
			}
			return 86400
		}
	}
	return 86400
}

// handlerForAddr returns the dynamic handler for addr, wrapped to advertise
// HTTP/3 via Alt-Svc according to state's current mode (#161). state is nil
// for a non-TLS or non-HTTP/3 address, in which case altSvcMiddleware is a
// no-op passthrough.
func (s *Server) handlerForAddr(addr string, state *DynamicAltSvc) http.Handler {
	return altSvcMiddleware(s.dynamicHandler(addr), state)
}
