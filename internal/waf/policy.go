// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import "time"

// HeaderWAFEvent is the opaque server-owned support reference header emitted on
// WAF-interrupted HTTP responses (#632). It carries a server-minted "wev_..."
// identifier and never discloses rule IDs, policy digests, or route paths.
const HeaderWAFEvent = "X-Jul-WAF-Event"

// Bounded retention and aggregation limits for the in-memory WAF investigation
// ring buffer (#632, ADR 0031 §5).
const (
	MaxInvestigationEvents  = 256
	MaxMatchedRulesPerEvent = 16
	MaxEventBytes           = 4096
	InvestigationEventTTL   = 15 * time.Minute
)

// Closed WAF transaction outcome values (#632-A, ADR 0031 §1.2). Every
// transaction reaching an enabled WAF middleware completes with exactly one of
// these outcomes.
const (
	OutcomeClean               = "clean"
	OutcomeMatchedObserved     = "matched_observed"
	OutcomeInterruptedRequest  = "interrupted_request"
	OutcomeInterruptedResponse = "interrupted_response"
	OutcomeRejectedBodyLimit   = "rejected_body_limit"
	OutcomeInspectionError     = "inspection_error"
)

// Closed investigation-buffer collection outcomes (#632-A, ADR 0031 §5.1).
const (
	InvestigationRecorded   = "recorded"
	InvestigationTruncated  = "truncated"
	InvestigationDropped    = "dropped"
	InvestigationSampledOut = "sampled_out"
)

// RouteAttribution identifies the configured route and policy scope that served
// a WAF-inspected transaction (#632).
type RouteAttribution struct {
	RouteID             string   `json:"route_id,omitempty"`
	Listen              string   `json:"listen,omitempty"`
	ServerNames         []string `json:"server_names,omitempty"`
	MatchType           string   `json:"match_type,omitempty"`
	PathPattern         string   `json:"path_pattern,omitempty"`
	MatchOrdinal        int      `json:"match_ordinal"`
	Scope               string   `json:"scope,omitempty"` // "global" or "override"
	Methods             []string `json:"methods,omitempty"`
	Predicates          string   `json:"predicates,omitempty"`
	CanonicalPredicates string   `json:"canonical_predicates,omitempty"`
}

// MatchedRuleSummary is the bounded, secret-safe metadata for one matched rule
// in a WAF transaction (#632). It never includes raw request/response bodies,
// query strings, cookie/header values, file paths, or macro-expanded messages.
type MatchedRuleSummary struct {
	RuleID         int      `json:"rule_id"`
	Phase          int      `json:"phase"`
	Severity       string   `json:"severity,omitempty"`
	SourceClass    string   `json:"source_class"`
	Disruptive     bool     `json:"disruptive"`
	Logged         bool     `json:"logged"`
	Tags           []string `json:"tags,omitempty"`
	MatchedTargets []string `json:"matched_targets,omitempty"`
}

// TransactionEvent records the trustworthy outcome and bounded provenance of a
// non-clean WAF-inspected HTTP transaction (#632).
type TransactionEvent struct {
	EventID                  string               `json:"event_id"`
	Time                     time.Time            `json:"time"`
	Outcome                  string               `json:"outcome"`
	Mode                     string               `json:"mode"`
	WouldInterrupt           bool                 `json:"would_interrupt"`
	InterruptedAction        string               `json:"interrupted_action,omitempty"`
	InterruptedRuleID        int                  `json:"interrupted_rule_id,omitempty"`
	ClientStatus             int                  `json:"client_status"`
	OriginReached            bool                 `json:"origin_reached"`
	OriginStatus             int                  `json:"origin_status,omitempty"`
	PartialResponseDelivered bool                 `json:"partial_response_delivered"`
	Hijacked                 bool                 `json:"hijacked"`
	PhasesEvaluated          []string             `json:"phases_evaluated,omitempty"`
	Method                   string               `json:"method"`
	Host                     string               `json:"host"`
	Path                     string               `json:"path"`
	QueryOmitted             bool                 `json:"query_omitted"`
	Route                    RouteAttribution     `json:"route"`
	Generation               uint64               `json:"generation"`
	PolicyDigest             string               `json:"policy_digest,omitempty"`
	ExternalDigest           string               `json:"external_digest,omitempty"`
	CRSVersion               string               `json:"crs_version,omitempty"`
	Paranoia                 int                  `json:"paranoia,omitempty"`
	MatchedRules             []MatchedRuleSummary `json:"matched_rules,omitempty"`
	TotalMatchedRules        int                  `json:"total_matched_rules"`
	MatchesTruncated         bool                 `json:"matches_truncated"`
	EventTruncated           bool                 `json:"event_truncated,omitempty"`
	SupportHeaderEmitted     bool                 `json:"support_header_emitted"`
	RequestIDHint            string               `json:"request_id_hint,omitempty"`
	TraceIDHint              string               `json:"trace_id_hint,omitempty"`
	ErrorCategory            string               `json:"error_category,omitempty"`
}

// PolicyInfo is the bounded description of one compiled WAF policy, taken
// from the engine build that serves it rather than re-read from TOML (#440).
// It never carries a rule file path, rule text, or rule message.
type PolicyInfo struct {
	// Mode is the enforcement mode Jul appended last: "block" or "detect".
	Mode string
	// BlockStatus is the SecDefaultAction status Jul emitted. It applies to
	// non-CRS rules only; CRS anomaly blocks use the CRS setup's own status.
	BlockStatus int
	CRSEnabled  bool
	// CRSVersion is the embedded CRS version, set only when CRSEnabled.
	CRSVersion string
	// Paranoia is the CRS paranoia level Jul set, or the CRS default (1) when
	// ParanoiaDefault; 0 without CRS. A post-CRS rule file may still change it.
	Paranoia        int
	ParanoiaDefault bool
	// RequestBodyLimitBytes is authoritative: Jul applies it after the rule
	// directives, so no SecRequestBodyLimit in a rule file can change it.
	RequestBodyLimitBytes int64
	// ResponseBodyInspection reports response-body access. Its limit is left
	// to the engine and rule directives and is not reported.
	ResponseBodyInspection bool
	// RuleFilesConfigured is the number of directives_files entries.
	RuleFilesConfigured int
	// ExternalFiles is the number of distinct filesystem files the compile
	// read (rule files, their includes and operator data files), and
	// ExternalDigest a context-keyed "ev1:<context-id>:<tag>" HMAC-SHA-256
	// digest over the exact bytes read, in read order (#649). Empty when
	// nothing external was read.
	ExternalFiles  int
	ExternalDigest string
	// PolicyDigest is the context-keyed "ev1:<context-id>:<tag>" HMAC-SHA-256
	// digest over the complete compiled policy inputs (domain
	// "waf-compiled-policy-v1", ADR 0031 §3.1).
	PolicyDigest string
	// InlineRules reports that inline_rules is non-empty.
	InlineRules bool
	Rules       RuleCounts
}

// RuleCounts classifies the compiled rules (SecRule, SecAction, SecMarker) by
// where they came from.
type RuleCounts struct {
	Total int
	// Embedded rules come from the CRS and Coraza configuration shipped in
	// the binary.
	Embedded int
	// External rules come from files on disk.
	External int
	// Inline rules come from inline_rules.
	Inline int
	// Generated rules are emitted by Jul itself (the CRS paranoia SecAction).
	Generated int
}
