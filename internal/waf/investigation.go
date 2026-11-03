// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/observability"
	"jul/internal/redact"
	"jul/internal/versionid"
)

var sensitiveTargetSubstrings = []string{
	"secret",
	"token",
	"key",
	"pass",
	"auth",
	"cookie",
	"session",
	"jwt",
	"bearer",
	"code",
	"cred",
	"priv",
	"canary",
	"ssn",
	"oauth",
}

var wholeOrInternalCollections = map[string]struct{}{
	"TX":                  {},
	"RULE":                {},
	"ENV":                 {},
	"GLOBAL":              {},
	"IP":                  {},
	"SESSION":             {},
	"USER":                {},
	"REQBODY_ERROR_MSG":   {},
	"REQUEST_URI":         {},
	"REQUEST_URI_RAW":     {},
	"REQUEST_FILENAME":    {},
	"REQUEST_BASENAME":    {},
	"QUERY_STRING":        {},
	"REQUEST_BODY":        {},
	"RESPONSE_BODY":       {},
	"MATCHED_VAR":         {},
	"MATCHED_VARS":        {},
	"MATCHED_VAR_NAME":    {},
	"MATCHED_VARS_NAMES":  {},
	"FULL_REQUEST":        {},
	"FULL_REQUEST_LENGTH": {},
}

var selectableTargetCollections = map[string]struct{}{
	"ARGS":                  {},
	"ARGS_GET":              {},
	"ARGS_POST":             {},
	"ARGS_NAMES":            {},
	"ARGS_GET_NAMES":        {},
	"ARGS_POST_NAMES":       {},
	"REQUEST_HEADERS":       {},
	"REQUEST_HEADERS_NAMES": {},
	"REQUEST_COOKIES":       {},
	"REQUEST_COOKIES_NAMES": {},
	"FILES":                 {},
	"FILES_NAMES":           {},
	"XML":                   {},
}

// SanitizeMatchedTarget returns a bounded, secret-safe target representation
// ("<COLLECTION>" or "<COLLECTION>:<safe_key>" or "<COLLECTION>:[redacted]")
// for a matched rule variable (#632, ADR 0031 §5.2). Raw variable values,
// macro-expanded messages, and token-like or secret-bearing parameter names are
// never retained.
func SanitizeMatchedTarget(collection, key string) string {
	col := strings.ToUpper(strings.TrimSpace(collection))
	if col == "" {
		return ""
	}
	if _, internal := wholeOrInternalCollections[col]; internal {
		return col
	}
	if _, selectable := selectableTargetCollections[col]; !selectable {
		return col
	}
	trimmedKey := strings.TrimSpace(key)
	if trimmedKey == "" {
		return col
	}
	if col == "XML" {
		return "XML:/*"
	}
	if !isSafeTargetKey(trimmedKey) {
		return col + ":[redacted]"
	}
	return col + ":" + strings.ToLower(trimmedKey)
}

func isSafeTargetKey(key string) bool {
	if len(key) == 0 || len(key) > 32 {
		return false
	}
	// Reject if global secret redaction modifies the key.
	if redact.Apply(key) != key {
		return false
	}
	// First character must be an ASCII letter; remaining characters may only be
	// ASCII letters, '_', or '-'. Any digit, punctuation, or non-ASCII byte is
	// treated as potentially data/token-bearing and redacted.
	for i := 0; i < len(key); i++ {
		c := key[i]
		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if i == 0 {
			if !isAlpha {
				return false
			}
			continue
		}
		if !isAlpha && c != '_' && c != '-' {
			return false
		}
	}
	lower := strings.ToLower(key)
	for _, sub := range sensitiveTargetSubstrings {
		if strings.Contains(lower, sub) {
			return false
		}
	}
	return true
}

// InvestigationFilter selects retained WAF transaction events in
// InvestigationStore.Snapshot (#632-B).
type InvestigationFilter struct {
	EventID    string
	Outcome    string
	Mode       string
	RuleID     int
	RouteID    string
	PathPrefix string
	Since      time.Time
	Limit      int
}

// InvestigationSnapshot is the point-in-time read projection of the bounded
// WAF investigation ring buffer (#632).
type InvestigationSnapshot struct {
	WAFCompiled    bool               `json:"waf_compiled"`
	Enabled        bool               `json:"enabled"`
	Degraded       bool               `json:"degraded,omitempty"`
	Capacity       int                `json:"capacity"`
	Retained       int                `json:"retained"`
	TTLSeconds     int                `json:"ttl_seconds"`
	MaxEventBytes  int                `json:"max_event_bytes"`
	RecordedTotal  uint64             `json:"recorded_total"`
	TruncatedTotal uint64             `json:"truncated_total"`
	DroppedTotal   uint64             `json:"dropped_total"`
	LastEventAt    string             `json:"last_event_at,omitempty"`
	Events         []TransactionEvent `json:"events"`
}

// EvidenceState returns the authoritative bounded evidence state ("unavailable",
// "disabled", "degraded", or "ok") for jul_security_evidence_state{producer="waf_investigation"}
// and GET /api/v1/security/posture (#632, #633).
func (snap InvestigationSnapshot) EvidenceState() string {
	if !snap.WAFCompiled {
		return "unavailable"
	}
	if !snap.Enabled {
		return "disabled"
	}
	if snap.Degraded || (snap.LastEventAt == "" && (snap.DroppedTotal > 0 || snap.TruncatedTotal > 0)) {
		return "degraded"
	}
	return "ok"
}

// InvestigationStore is a thread-safe, bounded, TTL-governed in-memory ring
// buffer of recent non-clean WAF transactions (#632, ADR 0031 §5).
type InvestigationStore struct {
	key     [32]byte
	seq     atomic.Uint64
	enabled atomic.Bool

	mu             sync.Mutex
	buf            []TransactionEvent
	next           int
	full           bool
	ttl            time.Duration
	recordedTotal  uint64
	truncatedTotal uint64
	droppedTotal   uint64
	lastEventAt    time.Time
	lastDropAt     time.Time
}

// NewInvestigationStore creates a bounded WAF investigation buffer. When
// capacity <= 0 it defaults to MaxInvestigationEvents (256); when ttl <= 0 it
// defaults to InvestigationEventTTL (15m).
func NewInvestigationStore(capacity int, ttl time.Duration) *InvestigationStore {
	if capacity <= 0 {
		capacity = MaxInvestigationEvents
	}
	if ttl <= 0 {
		ttl = InvestigationEventTTL
	}
	s := &InvestigationStore{
		buf: make([]TransactionEvent, capacity),
		ttl: ttl,
	}
	if _, err := rand.Read(s.key[:]); err != nil {
		copy(s.key[:], "jul-waf-investigation-fallback-k")
	}
	s.enabled.Store(true)
	return s
}

// SetEnabled enables or disables event collection. Disabling clears any
// currently retained events immediately so disabled mode retains zero records.
func (s *InvestigationStore) SetEnabled(enabled bool) {
	if s == nil {
		return
	}
	s.enabled.Store(enabled)
	if !enabled {
		s.Clear()
	}
}

// Enabled reports whether WAF investigation event collection is enabled.
func (s *InvestigationStore) Enabled() bool {
	if s == nil {
		return false
	}
	return s.enabled.Load()
}

// Clear removes all retained events from the ring buffer while preserving
// cumulative telemetry counters.
func (s *InvestigationStore) Clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	clear(s.buf)
	s.next = 0
	s.full = false
	s.lastDropAt = time.Time{}
	s.mu.Unlock()
}

// NewEventID mints a server-owned, unpredictable opaque event reference
// ("wev_<32-hex-chars>") safe for response headers and investigation lookup.
func (s *InvestigationStore) NewEventID() string {
	var seed [32]byte
	var n uint64
	if s != nil {
		seed = s.key
		n = s.seq.Add(1)
	} else {
		_, _ = rand.Read(seed[:])
		n = 1
	}
	var payload [16]byte
	binary.BigEndian.PutUint64(payload[:8], n)
	binary.BigEndian.PutUint64(payload[8:], uint64(time.Now().UnixNano()))
	mac := hmac.New(sha256.New, seed[:])
	_, _ = mac.Write([]byte("jul-waf-event-id-v1:"))
	_, _ = mac.Write(payload[:])
	sum := mac.Sum(nil)
	return "wev_" + hex.EncodeToString(sum[:16])
}

// Record sanitizes, bounds, and appends a non-clean WAF transaction event to
// the ring buffer. It returns the collection result enum and whether the event
// was stored.
func (s *InvestigationStore) Record(ev TransactionEvent) (string, bool) {
	if s == nil || !s.enabled.Load() {
		return "", false
	}
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	} else {
		ev.Time = ev.Time.UTC()
	}
	if ev.EventID == "" {
		ev.EventID = s.NewEventID()
	}
	ev.Method = strings.Clone(sanitizeHTTPMethod(ev.Method))
	ev.Host = strings.Clone(sanitizeEventHost(ev.Host))
	ev.Path = strings.Clone(redact.Apply(observability.SanitizePath(ev.Path)))
	ev.RequestIDHint = strings.Clone(sanitizeHintToken(ev.RequestIDHint))
	ev.TraceIDHint = strings.Clone(sanitizeHintToken(ev.TraceIDHint))
	ev.EventID = strings.Clone(ev.EventID)
	ev.Outcome = strings.Clone(ev.Outcome)
	ev.Mode = strings.Clone(ev.Mode)
	ev.PolicyDigest = strings.Clone(ev.PolicyDigest)
	ev.CRSVersion = strings.Clone(ev.CRSVersion)
	ev.Route = sanitizeRouteAttribution(ev.Route)
	if len(ev.PhasesEvaluated) > 0 {
		phases := make([]string, len(ev.PhasesEvaluated))
		for i, p := range ev.PhasesEvaluated {
			phases[i] = strings.Clone(p)
		}
		ev.PhasesEvaluated = phases
	}

	if ev.TotalMatchedRules < len(ev.MatchedRules) {
		ev.TotalMatchedRules = len(ev.MatchedRules)
	}
	if len(ev.MatchedRules) > MaxMatchedRulesPerEvent {
		ev.MatchedRules = append([]MatchedRuleSummary(nil), ev.MatchedRules[:MaxMatchedRulesPerEvent]...)
		ev.MatchesTruncated = true
	} else if len(ev.MatchedRules) > 0 {
		ev.MatchedRules = append([]MatchedRuleSummary(nil), ev.MatchedRules...)
	}
	for i := range ev.MatchedRules {
		ev.MatchedRules[i] = sanitizeMatchedRuleSummary(ev.MatchedRules[i])
	}

	enforceEventByteCap(&ev)

	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled.Load() {
		return "", false
	}
	overwroteLive := false
	if s.full {
		old := s.buf[s.next]
		if !old.Time.IsZero() && ev.Time.Sub(old.Time) <= s.ttl {
			overwroteLive = true
		}
	}
	s.buf[s.next] = ev
	s.next = (s.next + 1) % len(s.buf)
	if s.next == 0 {
		s.full = true
	}
	s.lastEventAt = ev.Time

	if overwroteLive {
		s.droppedTotal++
		s.lastDropAt = ev.Time
		if ev.MatchesTruncated || ev.EventTruncated {
			s.truncatedTotal++
		}
		s.recordedTotal++
		return InvestigationDropped, true
	}
	if ev.MatchesTruncated || ev.EventTruncated {
		s.truncatedTotal++
		s.recordedTotal++
		return InvestigationTruncated, true
	}
	s.recordedTotal++
	return InvestigationRecorded, true
}

// Snapshot returns retained non-expired events newest-first matching filter,
// along with buffer telemetry (#632).
func (s *InvestigationStore) Snapshot(now time.Time, filter InvestigationFilter) InvestigationSnapshot {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	if s == nil {
		return InvestigationSnapshot{
			WAFCompiled:   Compiled,
			Enabled:       false,
			Capacity:      MaxInvestigationEvents,
			TTLSeconds:    int(InvestigationEventTTL.Seconds()),
			MaxEventBytes: MaxEventBytes,
			Events:        []TransactionEvent{},
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	n := s.next
	if s.full {
		n = len(s.buf)
	}
	limit := filter.Limit
	if limit <= 0 || limit > len(s.buf) {
		limit = len(s.buf)
	}

	retained := 0
	activeTruncated := false
	events := make([]TransactionEvent, 0, min(n, limit))
	for i := 0; i < n; i++ {
		idx := (s.next - 1 - i + len(s.buf)) % len(s.buf)
		ev := s.buf[idx]
		if ev.Time.IsZero() || now.Sub(ev.Time) > s.ttl {
			continue
		}
		retained++
		if ev.MatchesTruncated || ev.EventTruncated {
			activeTruncated = true
		}
		if !matchesInvestigationFilter(ev, filter) {
			continue
		}
		if len(events) < limit {
			events = append(events, cloneTransactionEvent(ev))
		}
	}

	degraded := activeTruncated || (!s.lastDropAt.IsZero() && now.Sub(s.lastDropAt) <= s.ttl)

	var lastAt string
	if !s.lastEventAt.IsZero() {
		lastAt = s.lastEventAt.UTC().Format(time.RFC3339)
	}
	return InvestigationSnapshot{
		WAFCompiled:    Compiled,
		Enabled:        s.enabled.Load(),
		Degraded:       degraded,
		Capacity:       len(s.buf),
		Retained:       retained,
		TTLSeconds:     int(s.ttl.Seconds()),
		MaxEventBytes:  MaxEventBytes,
		RecordedTotal:  s.recordedTotal,
		TruncatedTotal: s.truncatedTotal,
		DroppedTotal:   s.droppedTotal,
		LastEventAt:    lastAt,
		Events:         events,
	}
}

func matchesInvestigationFilter(ev TransactionEvent, f InvestigationFilter) bool {
	if f.EventID != "" && ev.EventID != f.EventID {
		return false
	}
	if f.Outcome != "" && ev.Outcome != f.Outcome {
		return false
	}
	if f.Mode != "" && ev.Mode != f.Mode {
		return false
	}
	if f.RouteID != "" && ev.Route.RouteID != f.RouteID {
		return false
	}
	if f.PathPrefix != "" && !strings.HasPrefix(ev.Path, f.PathPrefix) {
		return false
	}
	if !f.Since.IsZero() && ev.Time.Before(f.Since) {
		return false
	}
	if f.RuleID > 0 {
		matched := ev.InterruptedRuleID == f.RuleID
		if !matched {
			for _, mr := range ev.MatchedRules {
				if mr.RuleID == f.RuleID {
					matched = true
					break
				}
			}
		}
		if !matched {
			return false
		}
	}
	return true
}

func cloneTransactionEvent(ev TransactionEvent) TransactionEvent {
	ev.PhasesEvaluated = append([]string(nil), ev.PhasesEvaluated...)
	ev.Route.ServerNames = append([]string(nil), ev.Route.ServerNames...)
	if len(ev.MatchedRules) > 0 {
		rules := make([]MatchedRuleSummary, len(ev.MatchedRules))
		for i, r := range ev.MatchedRules {
			r.Tags = append([]string(nil), r.Tags...)
			r.MatchedTargets = append([]string(nil), r.MatchedTargets...)
			rules[i] = r
		}
		ev.MatchedRules = rules
	}
	return ev
}

func sanitizeMatchedRuleSummary(r MatchedRuleSummary) MatchedRuleSummary {
	switch r.SourceClass {
	case "embedded", "external", "inline", "generated":
	default:
		r.SourceClass = "unknown"
	}
	if len(r.Tags) > 8 {
		r.Tags = r.Tags[:8]
	}
	var safeTags []string
	for _, t := range r.Tags {
		t = strings.TrimSpace(t)
		if t == "" || len(t) > 64 || redact.Apply(t) != t || hasControlOrQuote(t) {
			continue
		}
		if !slices.Contains(safeTags, t) {
			safeTags = append(safeTags, strings.Clone(t))
		}
	}
	r.Tags = safeTags

	if len(r.MatchedTargets) > 8 {
		r.MatchedTargets = r.MatchedTargets[:8]
	}
	var safeTargets []string
	for _, mt := range r.MatchedTargets {
		col, key, ok := strings.Cut(mt, ":")
		var clean string
		if ok {
			if key == "[redacted]" || key == "/*" {
				clean = strings.ToUpper(strings.TrimSpace(col)) + ":" + key
			} else {
				clean = SanitizeMatchedTarget(col, key)
			}
		} else {
			clean = SanitizeMatchedTarget(col, "")
		}
		if clean != "" && !slices.Contains(safeTargets, clean) {
			safeTargets = append(safeTargets, strings.Clone(clean))
		}
	}
	r.MatchedTargets = safeTargets
	return r
}

func sanitizeRouteAttribution(r RouteAttribution) RouteAttribution {
	if len(r.RouteID) > 128 {
		r.RouteID = r.RouteID[:128]
	}
	r.RouteID = strings.Clone(r.RouteID)
	if len(r.Listen) > 64 {
		r.Listen = r.Listen[:64]
	}
	r.Listen = strings.Clone(r.Listen)
	r.MatchType = strings.Clone(r.MatchType)
	if len(r.PathPattern) > 256 {
		r.PathPattern = r.PathPattern[:256]
	}
	r.PathPattern = strings.Clone(r.PathPattern)
	r.Scope = strings.Clone(r.Scope)
	r.Predicates = redact.Apply(r.Predicates)
	if len(r.Predicates) > 256 {
		r.Predicates = r.Predicates[:256]
	}
	r.Predicates = strings.Clone(r.Predicates)
	if r.CanonicalPredicates != "" {
		if _, ok := versionid.Parse(r.CanonicalPredicates); !ok {
			r.CanonicalPredicates = versionid.Process().Digest("waf-route-predicates-v1", []byte(r.CanonicalPredicates))
		}
	}
	r.CanonicalPredicates = strings.Clone(r.CanonicalPredicates)
	if len(r.Methods) > 16 {
		r.Methods = r.Methods[:16]
	}
	if len(r.Methods) > 0 {
		methods := make([]string, 0, len(r.Methods))
		for _, m := range r.Methods {
			if len(m) > 32 {
				m = m[:32]
			}
			methods = append(methods, strings.Clone(m))
		}
		r.Methods = methods
	}
	if len(r.ServerNames) > 8 {
		r.ServerNames = r.ServerNames[:8]
	}
	if len(r.ServerNames) > 0 {
		names := make([]string, 0, len(r.ServerNames))
		for _, n := range r.ServerNames {
			if len(n) > 128 {
				n = n[:128]
			}
			names = append(names, strings.Clone(n))
		}
		r.ServerNames = names
	}
	return r
}

func enforceEventByteCap(ev *TransactionEvent) {
	b, err := json.Marshal(ev)
	if err == nil && len(b) <= MaxEventBytes {
		return
	}
	ev.EventTruncated = true
	// Step 1: drop rule tags first.
	for i := range ev.MatchedRules {
		ev.MatchedRules[i].Tags = nil
	}
	if b, err = json.Marshal(ev); err == nil && len(b) <= MaxEventBytes {
		return
	}
	// Step 2: trim matched rules down until within MaxEventBytes.
	for len(ev.MatchedRules) > 0 {
		ev.MatchedRules = ev.MatchedRules[:len(ev.MatchedRules)-1]
		ev.MatchesTruncated = true
		if b, err = json.Marshal(ev); err == nil && len(b) <= MaxEventBytes {
			return
		}
	}
	ev.Route.ServerNames = nil
	ev.Route.Methods = nil
	ev.Route.Predicates = ""
	ev.Route.CanonicalPredicates = ""
}

func sanitizeHTTPMethod(m string) string {
	m = strings.ToUpper(strings.TrimSpace(m))
	if len(m) == 0 || len(m) > 16 {
		return "UNKNOWN"
	}
	for i := 0; i < len(m); i++ {
		c := m[i]
		if c < 'A' || c > 'Z' {
			return "UNKNOWN"
		}
	}
	return m
}

func sanitizeEventHost(h string) string {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" {
		return "_unmatched"
	}
	if len(h) > 128 || redact.Apply(h) != h || hasControlOrQuote(h) {
		return "_other"
	}
	return h
}

func sanitizeHintToken(s string) string {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 128 {
		return ""
	}
	if redact.Apply(s) != s {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c <= 0x20 || c >= 0x7f || c == '"' || c == '\'' || c == '<' || c == '>' || c == '\\' {
			return ""
		}
	}
	return s
}

func hasControlOrQuote(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 || c >= 0x7f || c == '"' || c == '\'' || c == '\\' {
			return true
		}
	}
	return false
}
