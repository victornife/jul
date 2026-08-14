// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"jul/internal/clientaddr"
	"jul/internal/logthrottle"
)

// clientAddrLogInterval is the shortest gap between two forwarding-header
// diagnostics from one listener. Malformed headers are attacker-controlled
// input, so the log line is rate limited to a heartbeat rather than emitted per
// request.
const clientAddrLogInterval = 10 * time.Second

// ClientAddress derives the canonical client address for every request and
// installs it in the request context.
//
// It is installed once per listen address, immediately after RequestID and
// outside the router, so derivation precedes every read of the Host header and
// every consumer — metrics, access logging, tracing, per-location auth, rate
// limiting and the WAF — observes the same identity. It never mutates
// RemoteAddr: the direct transport peer stays available through
// clientaddr.Peer.
//
// A nil policy still installs an identity, so consumers see the peer-derived
// value through the same accessor instead of falling back per consumer.
//
// observe, when non-nil, receives the bounded source and result enums for every
// request. The package cannot import internal/observability, so the collector is
// wired in as a hook the way the rate-limit and auth gates are.
func ClientAddress(policy *clientaddr.Policy, log *slog.Logger, observe func(source, result string)) Middleware {
	limiter := &logthrottle.Limiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := policy.Derive(clientaddr.PeerFromRemoteAddr(r.RemoteAddr), r.Header)
			if observe != nil {
				observe(id.Source.String(), id.Result.String())
			}
			if log != nil && id.Result != clientaddr.ResultAccepted && limiter.Allow(clientAddrLogInterval) {
				// Bounded fields only: the enums and the transport peer. The
				// asserted header is untrusted input and is never logged.
				log.Warn("forwarding header not used",
					"result", id.Result.String(),
					"peer", id.Peer.String(),
					"client_addr_policy", "listener")
			}
			next.ServeHTTP(w, r.WithContext(clientaddr.NewContext(r.Context(), id)))
		})
	}
}
