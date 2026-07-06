// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ── Console health & usage observability (Milestone 5.7) ────────────────────

// clientErrorCap bounds the frontend error reports retained in memory.
const clientErrorCap = 128

// adminLatencyReservoir bounds the admin request latencies kept for percentile
// estimation.
const adminLatencyReservoir = 256

// ClientError is one frontend JavaScript error reported by the SPA for
// operational visibility (Milestone 5.7). It is redacted and bounded.
type ClientError struct {
	Time    time.Time `json:"time"`
	Message string    `json:"message"`
	Source  string    `json:"source,omitempty"`
	Line    int       `json:"line,omitempty"`
	Col     int       `json:"col,omitempty"`
}

// consoleHealth tracks the Console's own request latencies, error counts, and
// reported frontend errors so operators can diagnose why the admin UI is slow
// or failing — separately from Jul's runtime health.
type consoleHealth struct {
	mu sync.Mutex

	requests   int64
	errors     int64
	latencies  []float64
	latencyPos int

	clientErrors    []ClientError
	clientErrorNext int
	clientErrorFull bool
}

func newConsoleHealth() *consoleHealth {
	return &consoleHealth{
		latencies:    make([]float64, 0, adminLatencyReservoir),
		clientErrors: make([]ClientError, clientErrorCap),
	}
}

func (h *consoleHealth) observe(durationMs float64, status int) {
	h.mu.Lock()
	h.requests++
	if status >= 500 {
		h.errors++
	}
	if len(h.latencies) < adminLatencyReservoir {
		h.latencies = append(h.latencies, durationMs)
	} else {
		h.latencies[h.latencyPos] = durationMs
		h.latencyPos = (h.latencyPos + 1) % adminLatencyReservoir
	}
	h.mu.Unlock()
}

func (h *consoleHealth) recordClientError(ce ClientError) {
	h.mu.Lock()
	h.clientErrors[h.clientErrorNext] = ce
	h.clientErrorNext = (h.clientErrorNext + 1) % len(h.clientErrors)
	if h.clientErrorNext == 0 {
		h.clientErrorFull = true
	}
	h.mu.Unlock()
}

func (h *consoleHealth) recentClientErrors() []ClientError {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.clientErrorNext
	if h.clientErrorFull {
		n = len(h.clientErrors)
	}
	out := make([]ClientError, 0, n)
	for i := 0; i < n; i++ {
		idx := (h.clientErrorNext - 1 - i + len(h.clientErrors)) % len(h.clientErrors)
		out = append(out, h.clientErrors[idx])
	}
	return out
}

func (h *consoleHealth) snapshot() (requests, errors int64, p50, p95, p99 float64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := make([]float64, len(h.latencies))
	copy(cp, h.latencies)
	sort.Float64s(cp)
	q := func(p float64) float64 {
		if len(cp) == 0 {
			return 0
		}
		idx := int(p * float64(len(cp)-1))
		return cp[idx]
	}
	return h.requests, h.errors, q(0.50), q(0.95), q(0.99)
}

// observeConsole wraps the admin mux to record per-request latency and error
// counts for the console-health endpoint. It must wrap the inner mux so it sees
// the real status code.
func (s *Server) observeConsole(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if s.health != nil {
			s.health.observe(float64(time.Since(start).Microseconds())/1000.0, sw.status)
		}
	})
}

// statusRecorder captures the response status for latency/error accounting. It
// implements http.Flusher pass-through so SSE streaming keeps working.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wroteHeader {
		s.wroteHeader = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// handleConsoleHealth serves the Console's own health at GET /api/admin/health
// (Milestone 5.7). It reports the admin API's request/error counts, latency
// percentiles, the live SSE connection count, and recent frontend errors. It is
// separable from Jul's runtime health (/readyz).
func (s *Server) handleConsoleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	requests, errors, p50, p95, p99 := int64(0), int64(0), 0.0, 0.0, 0.0
	var clientErrors []ClientError
	if s.health != nil {
		requests, errors, p50, p95, p99 = s.health.snapshot()
		clientErrors = s.health.recentClientErrors()
	}
	status := "ok"
	if requests > 0 && float64(errors)/float64(requests) > 0.1 {
		status = "degraded"
	}
	var sseConns int
	if s.limiter != nil {
		sseConns = s.limiter.eventConnCount()
	}
	if clientErrors == nil {
		clientErrors = []ClientError{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        status,
		"requests":      requests,
		"errors":        errors,
		"latency_p50":   p50,
		"latency_p95":   p95,
		"latency_p99":   p99,
		"sse_conns":     sseConns,
		"client_errors": clientErrors,
	})
}

// handleClientError accepts a frontend JavaScript error report at
// POST /api/admin/client-errors (Milestone 5.7). The payload is bounded and
// redacted before storage.
func (s *Server) handleClientError(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<10))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	var req struct {
		Message string `json:"message"`
		Source  string `json:"source"`
		Line    int    `json:"line"`
		Col     int    `json:"col"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	if s.health != nil {
		s.health.recordClientError(ClientError{
			Time:    time.Now().UTC(),
			Message: capStr(redactAuditText(req.Message), 512),
			Source:  capStr(stripQuery(req.Source), 256),
			Line:    req.Line,
			Col:     req.Col,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── Operational-depth panel endpoints (Milestones 5.1, 5.2, 5.5, 5.6) ───────

// handleRequestSamples serves recent request samples at
// GET /api/observability/requests (Milestone 5.1).
func (s *Server) handleRequestSamples(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []any{}
	if s.deps.RequestSamples != nil {
		samples := s.deps.RequestSamples()
		writeJSON(w, http.StatusOK, samples)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleFailingRoutes serves the top failing routes at
// GET /api/observability/failing-routes (Milestone 5.2). An optional ?limit=
// caps the number of rows (default 20).
func (s *Server) handleFailingRoutes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	limit := 20
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	out := []any{}
	if s.deps.FailingRoutes != nil {
		writeJSON(w, http.StatusOK, s.deps.FailingRoutes(limit))
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleUpstreamHistory serves upstream health history at
// GET /api/observability/upstream-history (Milestone 5.5).
func (s *Server) handleUpstreamHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []any{}
	if s.deps.UpstreamHealthHistory != nil {
		writeJSON(w, http.StatusOK, s.deps.UpstreamHealthHistory())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleCertHistory serves certificate renewal history at
// GET /api/observability/cert-history (Milestone 5.6).
func (s *Server) handleCertHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	out := []any{}
	if s.deps.CertRenewalHistory != nil {
		writeJSON(w, http.StatusOK, s.deps.CertRenewalHistory())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// capStr truncates s to n bytes.
func capStr(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// stripQuery removes any query string and fragment from a URL-ish source value.
func stripQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}
