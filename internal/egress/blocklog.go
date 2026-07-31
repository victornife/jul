// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// blockLogWindow is the minimum interval between two identical block logs
// (same subsystem, host, and reason). It suppresses a flood when a
// misconfigured or hostile caller retries the same refused destination.
const blockLogWindow = 1 * time.Minute

// blockLogMaxKeys bounds the rate-limiter's memory. The key includes the
// destination host, which is unbounded, so the tracker is capped and reset
// wholesale when it fills rather than growing without limit. A reset only
// re-permits one log per key, so it can never leak an unbounded amount of state.
const blockLogMaxKeys = 4096

// blockLogKey identifies an "identical" block for rate-limiting. The host is
// used only to distinguish repeats; it is never a metric label.
type blockLogKey struct {
	subsystem string
	host      string
	reason    Reason
}

// blockLogger emits a rate-limited, secret-free structured log for each blocked
// outbound destination. It logs only the bounded subsystem, the normalized host,
// an optional resolved IP, and the typed reason — never a URL, query string, or
// credential, so no secret can reach the log through this path. It is safe for
// concurrent use.
type blockLogger struct {
	log    *slog.Logger
	now    func() time.Time
	window time.Duration
	max    int

	mu   sync.Mutex
	last map[blockLogKey]time.Time
}

// newBlockLogger builds a block logger over log. A nil logger yields a logger
// whose observer is a no-op.
func newBlockLogger(log *slog.Logger) *blockLogger {
	return &blockLogger{
		log:    log,
		now:    time.Now,
		window: blockLogWindow,
		max:    blockLogMaxKeys,
		last:   make(map[blockLogKey]time.Time),
	}
}

// NewBlockLogObserver returns a Decision observer that emits a rate-limited,
// structured warning (or, for plugin fetches, info) for each blocked outbound
// destination. Allowed decisions are ignored. The observer never logs a URL,
// query string, or credential — only the subsystem, normalized host, optional
// resolved IP, and typed reason — so it is secret-safe by construction.
func NewBlockLogObserver(log *slog.Logger) func(Decision) {
	return newBlockLogger(log).observe
}

// observe logs a block decision unless an identical one was logged within the
// rate-limit window. Non-block decisions are ignored.
func (b *blockLogger) observe(d Decision) {
	if b == nil || b.log == nil || d.Result != ResultBlock {
		return
	}
	if !b.allow(blockLogKey{subsystem: d.Subsystem, host: d.Host, reason: d.Reason}) {
		return
	}
	attrs := []slog.Attr{
		slog.String("subsystem", d.Subsystem),
		slog.String("host", d.Host),
		slog.String("reason", string(d.Reason)),
	}
	if d.IP != "" && d.IP != d.Host {
		attrs = append(attrs, slog.String("resolved_ip", d.IP))
	}
	b.log.LogAttrs(context.Background(), blockLevel(d.Subsystem),
		"egress blocked an outbound destination not in the [egress] allow-list", attrs...)
}

// allow reports whether a log for key is due, and records the time when it is.
// It bounds memory by resetting the tracker when it reaches its cap.
func (b *blockLogger) allow(key blockLogKey) bool {
	now := b.now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if last, ok := b.last[key]; ok && now.Sub(last) < b.window {
		return false
	}
	if len(b.last) >= b.max {
		b.last = make(map[blockLogKey]time.Time)
	}
	b.last[key] = now
	return true
}

// blockLevel maps a subsystem to its log severity. Identity/PKI/discovery blocks
// are operator-actionable misconfiguration or SSRF signals and log at warning;
// plugin-fetch denials are guest-triggered and expected, so they log at info to
// avoid drowning the warning stream.
func blockLevel(subsystem string) slog.Level {
	if subsystem == SubsystemPlugin {
		return slog.LevelInfo
	}
	return slog.LevelWarn
}
