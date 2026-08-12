// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"jul/internal/clientaddr"
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
func ClientAddress(policy *clientaddr.Policy, log *slog.Logger) Middleware {
	limiter := &logLimiter{}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := policy.Derive(clientaddr.PeerFromRemoteAddr(r.RemoteAddr), r.Header)
			if log != nil && id.Result != clientaddr.ResultAccepted && limiter.allow(clientAddrLogInterval) {
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

// logLimiter admits one event per interval without allocating or locking.
type logLimiter struct{ last atomic.Int64 }

func (l *logLimiter) allow(interval time.Duration) bool {
	now := time.Now().UnixNano()
	prev := l.last.Load()
	if now-prev < int64(interval) {
		return false
	}
	return l.last.CompareAndSwap(prev, now)
}
