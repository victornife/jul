// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
)

// auditCap bounds the number of audit events retained in the in-memory ring
// buffer (Console v2 Milestone 6.6). Older events are overwritten so memory
// stays bounded; operators wanting durable retention export the log.
const auditCap = 10000

// AuditEvent is one attributable, append-only record of a security- or
// config-relevant action. It is deliberately metadata-only: it never carries
// secret values, tokens, Authorization headers, cookies, or request/response
// bodies (Milestone 6.6 redaction rule).
type AuditEvent struct {
	ID        int64     `json:"id"`
	Time      time.Time `json:"time"`
	Actor     string    `json:"actor"`              // redacted auth subject or "anonymous"
	Operation string    `json:"operation"`          // e.g. config.apply, config.rollback, auth.fail
	Resource  string    `json:"resource,omitempty"` // affected resource, if any
	Result    string    `json:"result"`             // success | failure
	Detail    string    `json:"detail,omitempty"`   // short, redacted description
	SourceIP  string    `json:"source_ip,omitempty"`
}

// auditLog is a fixed-size ring buffer of audit events with a monotonic id and
// an optional durable JSONL sink. The ring buffer keeps recent events cheap to
// query for the console; the sink (when configured) appends every event to a
// rotating file so the trail survives restarts and ring overwrite (P2-12).
//
// The sink is fail-loud (P3-08): a path that cannot be opened at startup or a
// write that fails later is recorded as a degraded status surfaced in the
// runtime overview, rather than being silently dropped. Durability is favored
// over per-write fsync — events are written immediately but not flushed to
// stable storage on every record, an explicit, documented trade-off.
type auditLog struct {
	mu     sync.Mutex
	buf    []AuditEvent
	next   int
	full   bool
	nextID int64

	sinkPath      string         // configured durable path; "" when no sink requested
	sink          io.WriteCloser // rotating JSONL sink; nil when unconfigured or degraded
	sinkErr       error          // why the sink is unavailable (open failure), if any
	writeErr      error          // most recent write failure, if any
	writeFailures int            // count of failed durable writes since startup
	log           *slog.Logger   // for sink errors; nil-safe via helper
}

// AuditSinkStatus reports the health of the durable audit sink so a broken or
// degraded compliance trail is visible in the Console rather than silently
// dropped (P3-08). It is omitted from the overview when no durable sink is
// configured.
type AuditSinkStatus struct {
	// Configured is true when a durable audit path is set in [admin].
	Configured bool `json:"configured"`
	// Path is the configured durable audit-log file.
	Path string `json:"path,omitempty"`
	// Healthy is true when the sink is open and the last write succeeded.
	Healthy bool `json:"healthy"`
	// Error explains why the sink is degraded (open failure or last write error).
	Error string `json:"error,omitempty"`
	// WriteFailures counts durable writes that failed since startup.
	WriteFailures int `json:"write_failures,omitempty"`
}

func newAuditLog(capacity int) *auditLog {
	if capacity <= 0 {
		capacity = auditCap
	}
	return &auditLog{buf: make([]AuditEvent, capacity)}
}

// newAuditLogWithSink builds an audit log that also appends every event as JSONL
// to path, rotating at maxMB megabytes and retaining keep backups. The directory
// is created if missing. A path that cannot be opened degrades to the in-memory
// ring buffer AND records the failure as a degraded sink status (surfaced in the
// runtime overview) so a misconfigured durable path is loud, not silent, while
// never taking the admin API down.
func newAuditLogWithSink(capacity int, path string, maxMB, keep int, log *slog.Logger) *auditLog {
	a := newAuditLog(capacity)
	a.log = log
	if strings.TrimSpace(path) == "" {
		return a
	}
	a.sinkPath = path
	// Eagerly verify the sink is writable so a misconfigured path is surfaced at
	// startup rather than only when the first event would have been persisted.
	if err := ensureAuditSinkWritable(path); err != nil {
		a.sinkErr = err
		auditLogWarn(log, "audit sink unavailable; durable trail disabled", path, err)
		return a
	}
	a.sink = &lumberjack.Logger{
		Filename:   path,
		MaxSize:    maxMB,
		MaxBackups: keep,
		LocalTime:  true,
	}
	return a
}

// ensureAuditSinkWritable creates the parent directory and confirms the audit
// path can be opened for appending, returning the underlying error otherwise.
func ensureAuditSinkWritable(path string) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return err
	}
	return f.Close()
}

// Close releases the durable sink, if any. It is safe to call on a nil sink.
func (a *auditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sink != nil {
		err := a.sink.Close()
		a.sink = nil
		return err
	}
	return nil
}

// statusReport returns the durable-sink health, or nil when no durable sink is
// configured (so the overview omits the field entirely).
func (a *auditLog) statusReport() *AuditSinkStatus {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sinkPath == "" {
		return nil
	}
	st := &AuditSinkStatus{
		Configured:    true,
		Path:          a.sinkPath,
		Healthy:       a.sink != nil && a.writeErr == nil,
		WriteFailures: a.writeFailures,
	}
	switch {
	case a.sinkErr != nil:
		st.Error = a.sinkErr.Error()
	case a.writeErr != nil:
		st.Error = a.writeErr.Error()
	}
	return st
}

func auditLogWarn(log *slog.Logger, msg, path string, err error) {
	if log != nil {
		log.Warn(msg, "path", path, "err", err)
	}
}

func (a *auditLog) record(ev AuditEvent) {
	a.mu.Lock()
	a.nextID++
	ev.ID = a.nextID
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	ev.Actor = redactActor(ev.Actor)
	ev.Detail = redactAuditText(ev.Detail)
	a.buf[a.next] = ev
	a.next = (a.next + 1) % len(a.buf)
	if a.next == 0 {
		a.full = true
	}
	// Append to the durable sink while holding the lock so concurrent records
	// produce well-formed, non-interleaved JSONL lines. The redacted event is
	// what is persisted, so the file carries no secrets. Write failures are
	// recorded (not just logged) so the degraded sink is surfaced in status
	// (P3-08); a later successful write clears the condition.
	if a.sink != nil {
		if line, err := json.Marshal(ev); err == nil {
			line = append(line, '\n')
			if _, werr := a.sink.Write(line); werr != nil {
				a.writeErr = werr
				a.writeFailures++
				auditLogWarn(a.log, "audit sink write failed", a.sinkPath, werr)
			} else {
				a.writeErr = nil
			}
		}
	}
	a.mu.Unlock()
}

// snapshot returns retained events newest-first, optionally filtered by
// operation prefix and result, capped at limit (0 = all).
func (a *auditLog) snapshot(opFilter, resultFilter string, limit int) []AuditEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.next
	if a.full {
		n = len(a.buf)
	}
	out := make([]AuditEvent, 0, n)
	for i := 0; i < n; i++ {
		idx := (a.next - 1 - i + len(a.buf)) % len(a.buf)
		ev := a.buf[idx]
		if opFilter != "" && !strings.HasPrefix(ev.Operation, opFilter) {
			continue
		}
		if resultFilter != "" && ev.Result != resultFilter {
			continue
		}
		out = append(out, ev)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// recordAudit appends an audit event. It is best-effort and never blocks
// request handling. actor/detail are redacted inside auditLog.record.
func (s *Server) recordAudit(operation, resource, result, detail, sourceIP string) {
	if s.audit == nil {
		return
	}
	s.audit.record(AuditEvent{
		Time:      time.Now().UTC(),
		Actor:     "operator", // single shared bearer token model; no per-user identity yet
		Operation: operation,
		Resource:  resource,
		Result:    result,
		Detail:    detail,
		SourceIP:  sourceIP,
	})
}

// redactActor maps any actor value to a non-identifying label. The admin API
// authenticates with a single shared bearer token, so there is no per-user
// identity to expose; we never echo the token itself.
func redactActor(actor string) string {
	if actor == "" {
		return "anonymous"
	}
	return actor
}

// redactAuditText strips anything that looks like a credential from a free-text
// audit detail before it is stored, as defense in depth.
func redactAuditText(s string) string {
	if s == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, marker := range []string{"authorization:", "bearer ", "token=", "cookie:", "password", "secret"} {
		if strings.Contains(lower, marker) {
			return "[redacted]"
		}
	}
	if len(s) > 512 {
		return s[:512]
	}
	return s
}

// handleAudit serves the audit log at GET /api/audit. Supported query
// parameters: op (operation prefix), result (success|failure), limit.
func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	if s.audit == nil {
		writeJSON(w, http.StatusOK, []AuditEvent{})
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	events := s.audit.snapshot(r.URL.Query().Get("op"), r.URL.Query().Get("result"), limit)
	if events == nil {
		events = []AuditEvent{}
	}
	writeJSON(w, http.StatusOK, events)
}

// handleAuditExport exports the audit log at GET /api/audit/export?format=json|csv.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w, http.MethodGet)
		return
	}
	var events []AuditEvent
	if s.audit != nil {
		events = s.audit.snapshot(r.URL.Query().Get("op"), r.URL.Query().Get("result"), 0)
	}

	switch strings.ToLower(r.URL.Query().Get("format")) {
	case "csv":
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"audit.csv\"")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "time", "actor", "operation", "resource", "result", "detail", "source_ip"})
		for _, ev := range events {
			_ = cw.Write([]string{
				strconv.FormatInt(ev.ID, 10),
				ev.Time.Format(time.RFC3339),
				ev.Actor,
				ev.Operation,
				ev.Resource,
				ev.Result,
				ev.Detail,
				ev.SourceIP,
			})
		}
		cw.Flush()
	default:
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=\"audit.json\"")
		if events == nil {
			events = []AuditEvent{}
		}
		_ = json.NewEncoder(w).Encode(events)
	}
}
