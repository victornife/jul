// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package stream implements the L4 (TCP/UDP) reverse proxy that serves the
// [[stream]] configuration tables alongside the HTTP listeners. It forwards raw
// connections and datagrams to backends without parsing the application
// protocol, supports SNI-based routing for TLS without terminating it, and can
// preserve the real client address across the proxy hop via the HAProxy PROXY
// protocol.
//
// The feature is compiled only into builds with the "stream" build tag. A lean
// build (without the tag) provides a stub whose Server is a no-op and whose
// Check rejects any configuration that declares a [[stream]] block, so such a
// build fails fast with a clear, actionable message instead of silently
// ignoring the configuration. This mirrors the http3 and wasmplugins seams.
package stream

import (
	"errors"
	"log/slog"

	"jul/internal/config"
)

// Hooks carries optional observation callbacks supplied by the composition root
// so this package stays decoupled from the metrics implementation. Each may be
// nil.
type Hooks struct {
	// OnConnDelta is invoked with +1 when an L4 connection (TCP) or session
	// (UDP) opens and -1 when it closes, labeled by protocol ("tcp"/"udp").
	OnConnDelta func(proto string, delta int64)
	// OnBytes is invoked with the number of bytes relayed, labeled by protocol
	// ("tcp"/"udp") and direction ("up" to backend, "down" to client).
	OnBytes func(proto, direction string, n int64)
	// OnUDPSessionEvicted is invoked when a UDP session is removed to enforce
	// limits, labeled by reason: "idle" (reaped after idle_timeout) or "lru"
	// (the least-recently-seen idle session reclaimed to admit a new client at
	// the session cap).
	OnUDPSessionEvicted func(reason string)
	// OnUDPSessionRejected is invoked when a new UDP client is dropped because a
	// listener's max_udp_sessions cap is reached and no session is reclaimable.
	OnUDPSessionRejected func()
}

// Options configures a stream Server.
type Options struct {
	Logger *slog.Logger
	Hooks  Hooks
}

// Check reports whether the configuration can be served by this binary with
// respect to L4 stream proxying. Stream support is a build-time choice (the
// "stream" tag); a binary without it cannot open stream listeners. When such a
// binary is given a configuration that declares any [[stream]] block, this
// returns an error so startup fails fast with a clear message instead of
// silently dropping the configuration. It is a no-op (returns nil) in
// stream-enabled builds or when no stream is configured.
func Check(streams []config.StreamServer) error {
	if Compiled {
		return nil
	}
	if len(streams) > 0 {
		return errors.New("[[stream]] (L4 TCP/UDP proxy) is configured but this binary was built without stream support; rebuild with -tags stream")
	}
	return nil
}
