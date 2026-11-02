// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// WAFRouteRef identifies the configured route and policy scope that served a
// WAF-inspected HTTP transaction (#632).
type WAFRouteRef struct {
	RouteID             string   `json:"route_id,omitempty"`
	Listen              string   `json:"listen,omitempty"`
	ServerNames         []string `json:"server_names,omitempty"`
	MatchType           string   `json:"match_type,omitempty"`
	PathPattern         string   `json:"path_pattern,omitempty"`
	MatchOrdinal        int      `json:"match_ordinal"`
	Scope               string   `json:"scope,omitempty"`
	Methods             []string `json:"methods,omitempty"`
	Predicates          string   `json:"predicates,omitempty"`
	CanonicalPredicates string   `json:"canonical_predicates,omitempty"`
}

// WAFMatchedRuleRecord is the bounded, secret-safe metadata for one matched
// rule in a WAF transaction (#632). It never includes raw request/response
// bodies, query strings, cookie/header values, file paths, or macro-expanded
// rule messages.
type WAFMatchedRuleRecord struct {
	RuleID         int      `json:"rule_id"`
	Phase          int      `json:"phase"`
	Severity       string   `json:"severity,omitempty"`
	SourceClass    string   `json:"source_class"`
	Disruptive     bool     `json:"disruptive"`
	Logged         bool     `json:"logged"`
	Tags           []string `json:"tags,omitempty"`
	MatchedTargets []string `json:"matched_targets,omitempty"`
}

// WAFEventRecord describes one non-clean WAF-inspected HTTP transaction (#632).
type WAFEventRecord struct {
	EventID                  string                 `json:"event_id"`
	Time                     string                 `json:"time"`
	Outcome                  string                 `json:"outcome"`
	Mode                     string                 `json:"mode"`
	WouldInterrupt           bool                   `json:"would_interrupt"`
	InterruptedAction        string                 `json:"interrupted_action,omitempty"`
	InterruptedRuleID        int                    `json:"interrupted_rule_id,omitempty"`
	ClientStatus             int                    `json:"client_status"`
	OriginReached            bool                   `json:"origin_reached"`
	OriginStatus             int                    `json:"origin_status,omitempty"`
	PartialResponseDelivered bool                   `json:"partial_response_delivered"`
	Hijacked                 bool                   `json:"hijacked"`
	PhasesEvaluated          []string               `json:"phases_evaluated,omitempty"`
	Method                   string                 `json:"method"`
	Host                     string                 `json:"host"`
	Path                     string                 `json:"path"`
	QueryOmitted             bool                   `json:"query_omitted"`
	Route                    WAFRouteRef            `json:"route"`
	Generation               uint64                 `json:"generation"`
	PolicyDigest             string                 `json:"policy_digest,omitempty"`
	ExternalDigest           string                 `json:"external_digest,omitempty"`
	CRSVersion               string                 `json:"crs_version,omitempty"`
	Paranoia                 int                    `json:"paranoia,omitempty"`
	StalePolicy              bool                   `json:"stale_policy"`
	MatchedRules             []WAFMatchedRuleRecord `json:"matched_rules,omitempty"`
	TotalMatchedRules        int                    `json:"total_matched_rules"`
	MatchesTruncated         bool                   `json:"matches_truncated"`
	EventTruncated           bool                   `json:"event_truncated,omitempty"`
	SupportHeaderEmitted     bool                   `json:"support_header_emitted"`
	RequestIDHint            string                 `json:"request_id_hint,omitempty"`
	TraceIDHint              string                 `json:"trace_id_hint,omitempty"`
	ErrorCategory            string                 `json:"error_category,omitempty"`
}

// WAFRetentionSpec describes the bounded in-memory WAF investigation buffer
// limits (#632).
type WAFRetentionSpec struct {
	MaxEvents     int `json:"max_events"`
	MaxEventBytes int `json:"max_event_bytes"`
	TTLSeconds    int `json:"ttl_seconds"`
}

// WAFCollectionStats reports current retention and process-lifetime buffer
// counters (#632).
type WAFCollectionStats struct {
	RetainedEvents int    `json:"retained_events"`
	RecordedTotal  uint64 `json:"recorded_total"`
	TruncatedTotal uint64 `json:"truncated_total"`
	DroppedTotal   uint64 `json:"dropped_total"`
}

// WAFEventsResponse is GET /api/v1/waf/events (#632-B).
type WAFEventsResponse struct {
	APIVersion          string             `json:"api_version"`
	CollectionState     string             `json:"collection_state"`
	ServingGeneration   uint64             `json:"serving_generation,omitempty"`
	ServingPolicyDigest string             `json:"serving_policy_digest,omitempty"`
	Retention           WAFRetentionSpec   `json:"retention"`
	Stats               WAFCollectionStats `json:"stats"`
	Events              []WAFEventRecord   `json:"events"`
}

// SecurityPostureAreaSummary summarises one of the 7 finite gateway security
// posture areas (#633): admin, trust, waf, cache, certificates, intent, or
// maintenance.
type SecurityPostureAreaSummary struct {
	Area           string `json:"area"`
	Status         string `json:"status"` // "ok", "warning", "error", "info", "unavailable"
	EpistemicState string `json:"epistemic_state"`
	ErrorCount     int    `json:"error_count"`
	WarningCount   int    `json:"warning_count"`
	InfoCount      int    `json:"info_count"`
	Summary        string `json:"summary"`
}

// SecurityPostureFinding is one actionable gateway posture finding (#633).
type SecurityPostureFinding struct {
	Code              string `json:"code"`
	Area              string `json:"area"`
	Severity          string `json:"severity"`        // "error", "warning", "info"
	EpistemicState    string `json:"epistemic_state"` // "observed", "configured", "inferred", "external_unknown", "unanalysed_custom", "not_applicable", "unavailable"
	Scope             string `json:"scope,omitempty"`
	Summary           string `json:"summary"`
	Remediation       string `json:"remediation,omitempty"`
	ServingGeneration uint64 `json:"serving_generation,omitempty"`
	PolicyDigest      string `json:"policy_digest,omitempty"`
}

// SecurityPostureResponse is GET /api/v1/security/posture (#633).
type SecurityPostureResponse struct {
	APIVersion        string                       `json:"api_version"`
	EvaluatedAt       string                       `json:"evaluated_at"`
	BaseVersion       string                       `json:"base_version,omitempty"`
	ServingGeneration uint64                       `json:"serving_generation,omitempty"`
	OverallStatus     string                       `json:"overall_status"`
	ErrorCount        int                          `json:"error_count"`
	WarningCount      int                          `json:"warning_count"`
	InfoCount         int                          `json:"info_count"`
	Areas             []SecurityPostureAreaSummary `json:"areas"`
	Findings          []SecurityPostureFinding     `json:"findings"`
}
