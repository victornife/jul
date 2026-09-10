// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/csv"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/rbac"
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
	Actor     string    `json:"actor"`              // authenticated principal or "anonymous"
	TokenID   string    `json:"token_id,omitempty"` // public credential identifier, if known
	Operation string    `json:"operation"`          // e.g. config.apply, config.rollback, auth.fail
	Resource  string    `json:"resource,omitempty"` // affected resource, if any
	// ResourceID is a durable resource identity touched by this event (ADR
	// 0019 §4/§5), when one is known — e.g. a route_id a patch batch minted
	// or referenced. It is distinct from Resource (a free-form label like
	// "config") and from apply/history/version identifiers, which already
	// have their own fields elsewhere; a single event may touch more than
	// one resource, in which case this is a comma-separated list.
	ResourceID string `json:"resource_id,omitempty"`
	// Selector carries a revision-scoped route selector (ADR 0018 §14:
	// listen, server_names, match_type, path, match_ordinal) for a
	// route-targeting operation whose target has no durable route_id —
	// populated only when ResourceID is empty for that same route, so a
	// route without an ID is still auditable by coordinates rather than
	// leaving the event silent about which route was touched. Like
	// ResourceID, more than one entry is comma-separated; it never carries a
	// predicate/header/query value.
	Selector string `json:"selector,omitempty"`
	Result   string `json:"result"`           // success | failure
	Detail   string `json:"detail,omitempty"` // short, redacted description
	SourceIP string `json:"source_ip,omitempty"`
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
	// mu is the single event linearization lock. ID assignment, ring placement,
	// sink-generation selection and write-lease/ticket allocation happen while
	// it is held; physical filesystem I/O never does.
	mu     sync.Mutex
	buf    []AuditEvent
	next   int
	full   bool
	nextID int64

	currentSink        *auditSinkGeneration
	sinkCfg            auditSinkConfig
	sinkConfigured     bool
	nextSinkGeneration uint64

	activeFailure      auditFailureCategory
	activeFailureAt    time.Time
	activeFailureErr   error // operator-log detail only; never serialized
	writeFailures      uint64
	rotateFailures     uint64
	cleanupFailures    uint64
	retirementFailures uint64
	log                *slog.Logger
}

// AuditSinkStatus is the bounded machine-safe view of the currently configured
// durable resource. Filesystem paths and raw OS errors deliberately live only
// in config:read settings/operator logs, never generic runtime health or readyz.
type AuditSinkStatus struct {
	Configured          bool      `json:"configured"`
	Active              bool      `json:"active"`
	Healthy             bool      `json:"healthy"`
	Generation          uint64    `json:"generation,omitempty"`
	WriteFailures       uint64    `json:"write_failures,omitempty"`
	RotateFailures      uint64    `json:"rotate_failures,omitempty"`
	CleanupFailures     uint64    `json:"cleanup_failures,omitempty"`
	RetireFailures      uint64    `json:"retirement_failures,omitempty"`
	LastFailureCategory string    `json:"last_failure_category,omitempty"`
	LastFailureAt       time.Time `json:"last_failure_at,omitempty"`
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

// recordAudit appends an audit event from an admin HTTP request. It is
// best-effort with respect to the audited operation: a sink failure never rolls it back.
// Durable persistence is synchronous and filesystem latency may therefore contribute
// to request/finalizer latency. actor/detail are redacted
// inside auditLog.record. Identity is extracted from the request context when
// RBAC or legacy auth has populated it; otherwise actor falls back to
// "anonymous".
func (s *Server) recordAudit(r *http.Request, operation, resource, result, detail string) {
	s.recordAuditResource(r, operation, resource, "", result, detail)
}

// recordAuditResource is recordAudit with an explicit durable ResourceID
// (ADR 0019 §4/§5), for events that touch one or more identified resources
// (e.g. a location_add that minted a route_id).
func (s *Server) recordAuditResource(r *http.Request, operation, resource, resourceID, result, detail string) {
	s.recordAuditResourceSelector(r, operation, resource, resourceID, "", result, detail)
}

// recordAuditResourceSelector is recordAuditResource with an additional
// revision-scoped Selector (ADR 0018 §14), for a route-targeting event whose
// target has no durable route_id.
func (s *Server) recordAuditResourceSelector(r *http.Request, operation, resource, resourceID, selector, result, detail string) {
	if s.audit == nil {
		return
	}
	var actor, tokenID string
	if id, ok := rbac.IdentityFromContext(r.Context()); ok {
		actor = id.Principal
		tokenID = id.TokenID
	}
	s.audit.record(AuditEvent{
		Time:       time.Now().UTC(),
		Actor:      actor,
		TokenID:    tokenID,
		Operation:  operation,
		Resource:   resource,
		ResourceID: resourceID,
		Selector:   selector,
		Result:     result,
		Detail:     detail,
		SourceIP:   adminClientIP(r),
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
