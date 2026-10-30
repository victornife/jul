// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package cachemeta owns detached, secret-safe cache metadata shared by the
// cache engine and external DTOs. It has no server/configuration dependencies.
package cachemeta

import "time"

type SafeResourceSummary struct {
	ResourceID       string                   `json:"resource_id,omitempty"`
	Details          []RepresentationMetadata `json:"details"`
	DetailsTruncated bool                     `json:"details_truncated,omitempty"`
	Host             string                   `json:"host"`
	Path             string                   `json:"path"`
	HasQuery         bool                     `json:"has_query,omitempty"`
	Methods          []string                 `json:"methods"`
	Representations  int                      `json:"representations"`
	Tiers            []string                 `json:"tiers"`
	BodyBytes        int64                    `json:"body_bytes"`
	VaryFields       []string                 `json:"vary_fields,omitempty"`
	Tags             []string                 `json:"tags,omitempty"`
	FreshnessState   string                   `json:"freshness_state"`
	CreatedAt        time.Time                `json:"created_at,omitempty,omitzero"`
	ExpiresAt        time.Time                `json:"expires_at,omitempty,omitzero"`
	StaleUntil       time.Time                `json:"stale_until,omitempty,omitzero"`
}

type TierStats struct {
	Bytes     int64 `json:"bytes"`
	MaxBytes  int64 `json:"max_bytes"`
	Entries   int   `json:"entries"`
	Evictions int64 `json:"evictions"`
}

type EntryEvidence struct {
	InsertedAt         time.Time `json:"inserted_at,omitempty,omitzero"`
	ValidatedAt        time.Time `json:"validated_at,omitempty,omitzero"`
	FreshnessSource    string    `json:"freshness_source"`
	SWRSource          string    `json:"swr_source"`
	SIESource          string    `json:"sie_source"`
	FreshnessSeconds   float64   `json:"freshness_seconds"`
	SWRSeconds         float64   `json:"swr_seconds"`
	SIESeconds         float64   `json:"sie_seconds"`
	RequiresValidation bool      `json:"requires_validation"`
	MustRevalidate     bool      `json:"must_revalidate"`
	SharedAuthReuse    bool      `json:"shared_auth_reuse"`
	PolicyID           [32]byte  `json:"-"`
}

type RepresentationMetadata struct {
	EffectiveSIESeconds *float64      `json:"effective_sie_seconds,omitempty"`
	ID                  string        `json:"id"`
	Method              string        `json:"method"`
	Tiers               []string      `json:"tiers"`
	BodyBytes           int64         `json:"body_bytes"`
	OriginAgeSeconds    float64       `json:"origin_age_seconds"`
	FreshnessState      string        `json:"freshness_state"`
	VaryFields          []string      `json:"vary_fields"`
	EvidenceState       string        `json:"evidence_state"`
	Evidence            EntryEvidence `json:"evidence"`
	RetainedPolicy      bool          `json:"retained_policy"`
}

type Inspection struct {
	Policy                 EffectivePolicy       `json:"policy"`
	State                  string                `json:"state"`
	CapturedAt             time.Time             `json:"captured_at"`
	CacheSequence          uint64                `json:"cache_sequence"`
	CountAccuracy          string                `json:"count_accuracy"`
	Truncated              bool                  `json:"truncated"`
	MatchedResources       int                   `json:"matched_resources"`
	MatchedRepresentations int                   `json:"matched_representations"`
	MemoryEntries          int                   `json:"memory_entries"`
	DiskEntries            int                   `json:"disk_entries"`
	MatchedBodyBytes       int64                 `json:"matched_body_bytes"`
	Resources              []SafeResourceSummary `json:"resources"`
	MemoryOccupancy        TierStats             `json:"memory_occupancy"`
	DiskOccupancy          *TierStats            `json:"disk_occupancy"`
	EvidenceState          string                `json:"evidence_state"`
	Decisions              []CacheDecision       `json:"decisions"`
	Warnings               []string              `json:"warnings"`
}

type CacheDecision struct {
	Source     string    `json:"source"`
	Stage      string    `json:"stage"`
	Summary    string    `json:"summary"`
	NextStep   string    `json:"next_step"`
	ResourceID string    `json:"resource_id"`
	Host       string    `json:"host"`
	Path       string    `json:"path"`
	HasQuery   bool      `json:"has_query"`
	Reason     string    `json:"reason"`
	ObservedAt time.Time `json:"observed_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

// EffectivePolicy reports current global or route-effective fallback values, never an unconditional
// cacheability or wall-clock guarantee. Entries can retain older computed policy.
type EffectivePolicy struct {
	DefaultTTLSeconds            float64  `json:"default_ttl_seconds"`
	MaxTTLSeconds                *float64 `json:"max_ttl_seconds,omitempty"`
	SWRSeconds                   float64  `json:"swr_seconds"`
	SIESeconds                   float64  `json:"sie_seconds"`
	MaxEntryBytes                int64    `json:"max_entry_bytes"`
	RangeEnabled                 bool     `json:"range_enabled"`
	FillLockEnabled              bool     `json:"fill_lock_enabled"`
	TagIngestionEnabled          bool     `json:"tag_ingestion_enabled"`
	RouteOverridesSupported      bool     `json:"route_overrides_supported"`
	GatewayFreshnessCapSupported bool     `json:"gateway_freshness_cap_supported"`
	BypassCookies                []string `json:"bypass_cookies,omitempty"`
	BypassAnyCookie              bool     `json:"bypass_any_cookie,omitempty"`
}
