// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"sync"
	"time"

	"jul/internal/middleware"
)

// logTailCap bounds the number of recent access-log entries retained in the
// ring buffer for the Console v2 Operations Log tab (Phase 4g). The buffer is
// fixed-size so memory stays bounded regardless of traffic volume.
const logTailCap = 512

// LogEntry is one completed request rendered for the Console Operations Log
// tail. It mirrors the access-log seam (middleware.AccessRecord) but is
// privacy-preserving for browser display: the path has identifier/email/token
// segments redacted to placeholders (SanitizePath), the query string is never
// retained, and the User-Agent is reduced to a coarse family. The client IP is
// kept because the log tail is the operator's access log — they need it to
// diagnose — and the admin surface is loopback- and token-gated.
type LogEntry struct {
	Time       time.Time `json:"time"`
	Method     string    `json:"method"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	Bytes      int64     `json:"bytes"`
	DurationMs float64   `json:"duration_ms"`
	Remote     string    `json:"remote,omitempty"`
	RequestID  string    `json:"request_id,omitempty"`
	TraceID    string    `json:"trace_id,omitempty"`
	UserAgent  string    `json:"user_agent,omitempty"` // coarse family only
	Proto      string    `json:"proto,omitempty"`
}

// LogTail is a fixed-size circular buffer of the most recent access-log entries
// plus a set of live followers for the Operations Log stream. It implements
// middleware.AccessSink, so the composition root adds it to the access-log sink
// set alongside the stdout/file/syslog sinks. It is safe for concurrent use and
// never blocks request handling: a slow stream follower has entries dropped
// rather than back-pressuring the access path.
type LogTail struct {
	mu   sync.Mutex
	buf  []LogEntry
	next int
	full bool
	subs map[chan LogEntry]struct{}
}

// NewLogTail returns a LogTail retaining the most recent capacity entries. A
// non-positive capacity uses the default bound.
func NewLogTail(capacity int) *LogTail {
	if capacity <= 0 {
		capacity = logTailCap
	}
	return &LogTail{
		buf:  make([]LogEntry, capacity),
		subs: make(map[chan LogEntry]struct{}),
	}
}

// Log records one completed request, redacting and normalizing sensitive fields
// before the entry ever enters the buffer or reaches a follower. It satisfies
// middleware.AccessSink.
func (t *LogTail) Log(rec middleware.AccessRecord) {
	t.record(LogEntry{
		Time:       rec.Time.UTC(),
		Method:     rec.Method,
		Host:       hostLabel(rec.Host),
		Path:       SanitizePath(rec.Path),
		Status:     rec.Status,
		Bytes:      rec.Bytes,
		DurationMs: float64(rec.Duration.Microseconds()) / 1000.0,
		Remote:     rec.Remote,
		RequestID:  rec.RequestID,
		TraceID:    rec.TraceID,
		UserAgent:  userAgentFamily(rec.UserAgent),
		Proto:      rec.Proto,
	})
}

// record appends one entry to the ring and fans it out to live followers. The
// fan-out is non-blocking: a follower whose buffer is full drops the entry, so
// a slow SSE reader never stalls the access-log path.
func (t *LogTail) record(e LogEntry) {
	t.mu.Lock()
	t.buf[t.next] = e
	t.next = (t.next + 1) % len(t.buf)
	if t.next == 0 {
		t.full = true
	}
	for ch := range t.subs {
		select {
		case ch <- e:
		default: // slow follower: drop
		}
	}
	t.mu.Unlock()
}

// Snapshot returns up to limit retained entries, newest-first. A non-positive
// limit returns every retained entry.
func (t *LogTail) Snapshot(limit int) []LogEntry {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := t.next
	if t.full {
		n = len(t.buf)
	}
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]LogEntry, 0, n)
	// Walk backwards from the most recently written slot so the newest entry is
	// first; the first n of that walk are the newest n.
	for i := 0; i < n; i++ {
		idx := (t.next - 1 - i + len(t.buf)) % len(t.buf)
		out = append(out, t.buf[idx])
	}
	return out
}

// Subscribe registers a live follower for the Operations Log stream. It returns
// a receive channel of new entries and an unsubscribe function the caller must
// invoke when done (e.g. on SSE disconnect). The channel is buffered and lossy
// by design — entries are dropped for a slow reader rather than blocking the
// access path. Calling the returned function more than once is safe.
func (t *LogTail) Subscribe() (<-chan LogEntry, func()) {
	ch := make(chan LogEntry, 64)
	t.mu.Lock()
	t.subs[ch] = struct{}{}
	t.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			t.mu.Lock()
			delete(t.subs, ch)
			t.mu.Unlock()
			close(ch)
		})
	}
	return ch, cancel
}
