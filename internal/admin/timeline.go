// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"sort"
	"sync"
	"time"
)

// timelineCap bounds the number of admin lifecycle events retained for the
// Console v2 Config & Runtime Event Timeline (Milestone 5.4).
const timelineCap = 256

// TimelineEvent is one entry in the merged operational timeline. It unifies
// admin lifecycle events (validate/apply/reload/rollback) with runtime events
// pulled from the observability histories (upstream health, certificate
// renewals) so an operator can answer "did a config change cause this?".
type TimelineEvent struct {
	Time     time.Time `json:"time"`
	Category string    `json:"category"` // config, runtime, tls, upstream
	Type     string    `json:"type"`     // validate_ok, apply, reload, rollback, health_change, cert_renewal, …
	Severity string    `json:"severity"` // info, warning, error
	Message  string    `json:"message"`
	// Ref optionally links to a config history snapshot id.
	Ref string `json:"ref,omitempty"`
}

// eventHistory is a fixed-size ring buffer of admin lifecycle events.
type eventHistory struct {
	mu   sync.Mutex
	buf  []TimelineEvent
	next int
	full bool
}

func newEventHistory(capacity int) *eventHistory {
	if capacity <= 0 {
		capacity = timelineCap
	}
	return &eventHistory{buf: make([]TimelineEvent, capacity)}
}

func (h *eventHistory) add(ev TimelineEvent) {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	h.mu.Lock()
	h.buf[h.next] = ev
	h.next = (h.next + 1) % len(h.buf)
	if h.next == 0 {
		h.full = true
	}
	h.mu.Unlock()
}

func (h *eventHistory) snapshot() []TimelineEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.next
	if h.full {
		n = len(h.buf)
	}
	out := make([]TimelineEvent, 0, n)
	for i := 0; i < n; i++ {
		idx := (h.next - 1 - i + len(h.buf)) % len(h.buf)
		out = append(out, h.buf[idx])
	}
	return out
}

// recordTimeline appends an admin lifecycle event to the in-memory timeline.
// It is best-effort and never blocks request handling.
func (s *Server) recordTimeline(category, typ, severity, message, ref string) {
	if s.timeline == nil {
		return
	}
	s.timeline.add(TimelineEvent{
		Time:     time.Now().UTC(),
		Category: category,
		Type:     typ,
		Severity: severity,
		Message:  message,
		Ref:      ref,
	})
}

// emit records an admin lifecycle event to the timeline and broadcasts a
// matching SSE event so live clients update. It centralizes the
// "append-to-timeline + broadcast" pattern used by the mutating handlers.
func (s *Server) emit(category, typ, severity, message string) {
	s.recordTimeline(category, typ, severity, message, "")
	s.broadcast(typ, map[string]string{
		"category": category,
		"severity": severity,
		"message":  message,
	})
}

// handleTimeline serves the merged operational timeline at
// GET /api/observability/timeline (Milestone 5.4). It interleaves the stored
// admin lifecycle events with upstream-health and certificate-renewal events
// pulled live from the observability histories, newest first.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}

	var events []TimelineEvent
	if s.timeline != nil {
		events = s.timeline.snapshot()
	}

	// Merge upstream health transitions.
	if s.deps.UpstreamHealthHistory != nil {
		for _, bh := range s.deps.UpstreamHealthHistory() {
			for _, ev := range bh.Recent {
				sev := "info"
				msg := bh.Pool + "/" + bh.Backend + " came up"
				if !ev.Healthy {
					sev = "warning"
					msg = bh.Pool + "/" + bh.Backend + " went down"
				}
				events = append(events, TimelineEvent{
					Time:     ev.Time,
					Category: "upstream",
					Type:     "health_change",
					Severity: sev,
					Message:  msg,
				})
			}
		}
	}

	// Merge certificate renewal events.
	if s.deps.CertRenewalHistory != nil {
		for _, ch := range s.deps.CertRenewalHistory() {
			for _, ev := range ch.Recent {
				sev := "info"
				msg := "Renewed certificate for " + ch.Domain
				typ := "cert_renewal"
				if !ev.Success {
					sev = "error"
					msg = "Certificate renewal failed for " + ch.Domain
					typ = "cert_error"
				}
				events = append(events, TimelineEvent{
					Time:     ev.Time,
					Category: "tls",
					Type:     typ,
					Severity: sev,
					Message:  msg,
				})
			}
		}
	}

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Time.After(events[j].Time)
	})
	if events == nil {
		events = []TimelineEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}
