// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"jul/internal/observability"
)

// logTailDefaultLimit caps the GET /api/observability/logs snapshot when the
// client does not specify ?limit=.
const logTailDefaultLimit = 200

// logTailStreamBacklog is how many recent entries the stream replays on connect
// so a freshly opened Operations Log tab shows context immediately instead of
// waiting for new traffic.
const logTailStreamBacklog = 100

// handleLogs serves recent access-log entries at GET /api/observability/logs
// (Phase 4g). An optional ?limit= caps the number of rows (default 200), and
// entries are returned newest-first. The buffer is bounded and
// privacy-preserving: paths are redacted, query strings dropped, and
// User-Agents reduced to a coarse family (see observability.LogTail).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	limit := logTailDefaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	out := []observability.LogEntry{}
	if s.deps.RecentLogs != nil {
		out = s.deps.RecentLogs(limit)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleLogsStream is the GET /api/observability/logs/stream SSE endpoint
// (Phase 4g). It replays a bounded backlog on connect, then streams each new
// access-log entry as a "log" event whose data is a LogEntry. Control frames
// ("connected", periodic "ping") match the /api/events shape so the frontend
// reuses the same SSE-over-fetch reader. Entries are best-effort: a follower
// that falls behind has entries dropped rather than back-pressuring traffic.
func (s *Server) handleLogsStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if s.deps.SubscribeLogs == nil {
		http.Error(w, "log streaming unavailable", http.StatusServiceUnavailable)
		return
	}

	// Bound concurrent SSE streams per client to prevent resource exhaustion,
	// sharing the same connection budget as /api/events (Milestone 1.6).
	release, ok := s.limiter.acquireConn(adminClientIP(r))
	if !ok {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
		return
	}
	defer release()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe before replaying the backlog so entries arriving during replay
	// are buffered on the channel rather than lost. This can briefly duplicate an
	// entry that lands in the window between the snapshot and the channel read;
	// for a best-effort log tail a rare duplicate is preferable to a gap.
	ch, cancel := s.deps.SubscribeLogs()
	defer cancel()

	_ = sendSSE(w, flusher, Event{Type: "connected", Time: time.Now().UTC()})

	if s.deps.RecentLogs != nil {
		backlog := s.deps.RecentLogs(logTailStreamBacklog)
		// Snapshot is newest-first; replay oldest-first so the UI appends in
		// chronological order.
		for i := len(backlog) - 1; i >= 0; i-- {
			if err := sendLogSSE(w, flusher, backlog[i]); err != nil {
				return
			}
		}
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return
			}
			if err := sendLogSSE(w, flusher, e); err != nil {
				return
			}
		case t := <-ticker.C:
			if err := sendSSE(w, flusher, Event{Type: "ping", Time: t.UTC()}); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		case <-s.quit:
			return
		}
	}
}

// sendLogSSE writes one access-log entry as a "log" SSE event, reusing the
// shared frame writer so the wire format matches /api/events.
func sendLogSSE(w http.ResponseWriter, flusher http.Flusher, e observability.LogEntry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return sendSSE(w, flusher, Event{Type: "log", Time: e.Time, Data: data})
}
