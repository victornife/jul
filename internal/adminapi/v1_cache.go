// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

// CacheSelector is the wire representation of a scoped cache invalidation
// selector across preview, execution, and operation-status responses (#621,
// ADR 0027 §1 & §5).
type CacheSelector struct {
	// Kind is one of "exact_url", "prefix", "glob", "tag", "host", "key", or "all".
	Kind string `json:"kind"`
	// Host is the normalized authority (lowercase host[:port]) for host-scoped
	// selectors ("exact_url", "prefix", "glob", "tag", "host").
	Host string `json:"host,omitempty"`
	// URL is the full http(s)://host[:port]/path[?query] input for "exact_url".
	URL string `json:"url,omitempty"`
	// URI is the origin-form /path[?query] for "exact_url" (redacted as
	// ?<redacted> in retained operation records and audit summaries).
	URI string `json:"uri,omitempty"`
	// Prefix is the segment-bounded path prefix (starting with '/') for "prefix".
	Prefix string `json:"prefix,omitempty"`
	// Pattern is the bounded anchored path glob (starting with '/', supporting
	// '*' and standalone '**') for "glob".
	Pattern string `json:"pattern,omitempty"`
	// Tags is the 1..16 grouping tags for "tag".
	Tags []string `json:"tags,omitempty"`
	// TagMatch is "any" (default) or "all" for multi-tag "tag" selectors.
	TagMatch string `json:"tag_match,omitempty"`
	// Key is the legacy exact internal cache key (METHOD\nhost\nrequest_uri) for "key".
	Key string `json:"key,omitempty"`
	// RouteID optionally binds the selector to a configured location route_id.
	RouteID string `json:"route_id,omitempty"`
	// All is true when Kind is "all" (whole-node cache purge).
	All bool `json:"all,omitempty"`
}

// CacheGuidance is a structured server-owned explanation or operational warning
// returned alongside cache preview and invalidation results (#621, #625).
type CacheGuidance struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Scope    string `json:"scope,omitempty"`
	Summary  string `json:"summary"`
	Detail   string `json:"detail,omitempty"`
	DocsRef  string `json:"docs_ref,omitempty"`
}

// CacheResourceSample is a secret-safe per-resource summary returned by
// preview when include_samples is requested (#621, #622). Raw query strings and
// Vary request header values are never included.
type CacheResourceSample struct {
	Host            string   `json:"host"`
	Path            string   `json:"path"`
	HasQuery        bool     `json:"has_query,omitempty"`
	Methods         []string `json:"methods"`
	Representations int      `json:"representations"`
	Tiers           []string `json:"tiers"`
	BodyBytes       int64    `json:"body_bytes"`
	VaryFields      []string `json:"vary_fields,omitempty"`
	Tags            []string `json:"tags,omitempty"`
	FreshnessState  string   `json:"freshness_state"`
	CreatedAt       string   `json:"created_at,omitempty"`
	ExpiresAt       string   `json:"expires_at,omitempty"`
	StaleUntil      string   `json:"stale_until,omitempty"`
}

// CachePurgePreviewRequest is the request body for POST /api/v1/cache/purge/preview.
type CachePurgePreviewRequest struct {
	Selector       CacheSelector `json:"selector"`
	MaxScanEntries int           `json:"max_scan_entries,omitempty"`
	IncludeSamples bool          `json:"include_samples,omitempty"`
}

// CachePurgePreviewResponse is the response body for POST /api/v1/cache/purge/preview.
type CachePurgePreviewResponse struct {
	APIVersion             string                `json:"api_version"`
	CacheEnabled           bool                  `json:"cache_enabled"`
	TagIngestionEnabled    bool                  `json:"tag_ingestion_enabled"`
	Selector               CacheSelector         `json:"selector"`
	SelectorDigest         string                `json:"selector_digest"`
	PreviewToken           string                `json:"preview_token"`
	MatchedResources       int                   `json:"matched_resources"`
	MatchedRepresentations int                   `json:"matched_representations"`
	MemoryEntries          int                   `json:"memory_entries"`
	DiskEntries            int                   `json:"disk_entries"`
	MatchedBytes           int64                 `json:"matched_bytes"`
	MemoryBytes            int64                 `json:"memory_bytes"`
	DiskBytes              int64                 `json:"disk_bytes"`
	InflightFillsMatched   int                   `json:"inflight_fills_matched"`
	InflightTagUnknown     bool                  `json:"inflight_tag_unknown,omitempty"`
	CountAccuracy          string                `json:"count_accuracy"`
	Truncated              bool                  `json:"truncated"`
	ScanLimit              int                   `json:"scan_limit"`
	ScannedEntries         int                   `json:"scanned_entries"`
	Source                 string                `json:"source"`
	AsOf                   string                `json:"as_of"`
	CacheSequence          uint64                `json:"cache_sequence"`
	ExecutionModeContract  string                `json:"execution_mode_contract"`
	BroadScope             bool                  `json:"broad_scope"`
	Warnings               []CacheGuidance       `json:"warnings,omitempty"`
	Samples                []CacheResourceSample `json:"samples,omitempty"`
	AuthorityState
}

// CachePurgeRequest is the request body for POST /api/v1/cache/purge.
type CachePurgeRequest struct {
	Selector         CacheSelector `json:"selector"`
	PreviewToken     string        `json:"preview_token,omitempty"`
	ConfirmBroad     bool          `json:"confirm_broad,omitempty"`
	RetryOperationID string        `json:"retry_operation_id,omitempty"`
	TimeoutMS        int           `json:"timeout_ms,omitempty"`
}

// CachePurgeResponse is the response body for POST /api/v1/cache/purge and
// GET /api/v1/cache/purges/{operation_id}.
type CachePurgeResponse struct {
	APIVersion                 string          `json:"api_version"`
	OperationID                string          `json:"operation_id"`
	State                      string          `json:"state"`
	Terminal                   bool            `json:"terminal"`
	Outcome                    string          `json:"outcome"`
	InvalidationEffective      string          `json:"invalidation_effective"`
	IdempotentReplay           bool            `json:"idempotent_replay,omitempty"`
	Selector                   CacheSelector   `json:"selector"`
	SelectorDigest             string          `json:"selector_digest"`
	FenceSequence              uint64          `json:"fence_sequence,omitempty"`
	StateChangedSincePreview   bool            `json:"state_changed_since_preview,omitempty"`
	InvalidatedResources       int             `json:"invalidated_resources"`
	InvalidatedRepresentations int             `json:"invalidated_representations"`
	MemoryEntriesRemoved       int             `json:"memory_entries_removed"`
	DiskEntriesRemoved         int             `json:"disk_entries_removed"`
	ActiveReadersRetired       int             `json:"active_readers_retired,omitempty"`
	FencedInflightOps          int             `json:"fenced_inflight_ops,omitempty"`
	SkippedEntries             int             `json:"skipped_entries,omitempty"`
	FailedEntries              int             `json:"failed_entries,omitempty"`
	RemainingEntries           int             `json:"remaining_entries,omitempty"`
	StartedAt                  string          `json:"started_at"`
	CompletedAt                string          `json:"completed_at,omitempty"`
	RecoveryGuidance           string          `json:"recovery_guidance,omitempty"`
	Warnings                   []CacheGuidance `json:"warnings,omitempty"`
	BootID                     string          `json:"boot_id"`
	AuthorityState
}
