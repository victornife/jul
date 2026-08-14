// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package logthrottle bounds the rate of diagnostics driven by untrusted input.
//
// A malformed forwarding header and a refused PROXY-protocol connection are
// both chosen by whoever is talking to the listener, so emitting one line per
// event would let a remote peer set the server's log volume. A throttled line
// still tells an operator that the condition is occurring; the hundredth copy
// adds nothing the first did not.
package logthrottle

import (
	"sync/atomic"
	"time"
)

// Limiter admits one event per interval without allocating or locking. The zero
// value is ready to use and admits the first event.
type Limiter struct{ last atomic.Int64 }

// Allow reports whether an event may be logged now.
//
// Concurrent callers that lose the compare-and-swap are suppressed, which is
// the intended outcome: exactly one line is emitted per interval however many
// goroutines race for it.
func (l *Limiter) Allow(interval time.Duration) bool {
	now := time.Now().UnixNano()
	prev := l.last.Load()
	if now-prev < int64(interval) {
		return false
	}
	return l.last.CompareAndSwap(prev, now)
}
