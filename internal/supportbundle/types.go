// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package supportbundle builds bounded, operator-triggered diagnostic archives.
// It has no upload, phone-home, arbitrary file include or command-execution path.
package supportbundle

import (
	"context"
	"time"
)

const (
	// FormatVersion is the archive-manifest compatibility version.
	FormatVersion = 1
	// RedactionProfile identifies the structural/string redaction policy.
	RedactionProfile = "jul-diagnostics-v1"
)

// Snapshot contains only explicit inputs needed by the closed collector set.
// RedactValues is a defensive final scan list for resolved secrets known by the
// caller; collectors must still exclude sensitive fields structurally.
type Snapshot struct {
	Product      string
	Version      string
	Commit       string
	BuildProfile string
	ConfigPath   string
	Capabilities map[string]bool
	CheckNetwork bool
	IncludeLogs  bool
	LogTailBytes int64
	RedactValues []string
}

// Limits bounds collection and archive resource use.
type Limits struct {
	TotalTimeout         time.Duration
	PerCollectorTimeout  time.Duration
	MaxArtifacts         int
	MaxArtifactBytes     int64
	MaxUncompressedBytes int64
	MaxCompressedBytes   int64
}

// DefaultLimits returns conservative local defaults.
func DefaultLimits() Limits {
	return Limits{
		TotalTimeout:         30 * time.Second,
		PerCollectorTimeout:  8 * time.Second,
		MaxArtifacts:         32,
		MaxArtifactBytes:     2 << 20,
		MaxUncompressedBytes: 12 << 20,
		MaxCompressedBytes:   8 << 20,
	}
}

// Artifact is one fixed, path-safe archive entry.
type Artifact struct {
	Path        string
	ContentType string
	Sensitivity string
	Data        []byte
	Truncated   bool
}

// Collector is the separate support-bundle seam. It is intentionally not the
// same interface as diagnostics.Check: collectors produce archive artifacts,
// while checks produce stable diagnostic results.
type Collector interface {
	Name() string
	Collect(context.Context, Snapshot) ([]Artifact, error)
}

// CollectorFunc adapts a function into a Collector for focused tests and the
// closed production registry.
type CollectorFunc struct {
	ID string
	Fn func(context.Context, Snapshot) ([]Artifact, error)
}

// Name returns the fixed collector identifier.
func (collector CollectorFunc) Name() string { return collector.ID }

// Collect executes the adapted collector.
func (collector CollectorFunc) Collect(ctx context.Context, snapshot Snapshot) ([]Artifact, error) {
	if collector.Fn == nil {
		return nil, ErrCollectorUnimplemented
	}
	return collector.Fn(ctx, snapshot)
}

// CollectorStatus is the manifest outcome of one collector.
type CollectorStatus string

const (
	CollectorSuccess   CollectorStatus = "success"
	CollectorError     CollectorStatus = "error"
	CollectorTruncated CollectorStatus = "truncated"
	CollectorSkipped   CollectorStatus = "skipped"
)

// CollectorRecord records one collector without exposing its source inputs.
type CollectorRecord struct {
	Name      string          `json:"name"`
	Status    CollectorStatus `json:"status"`
	Artifacts int             `json:"artifacts"`
	Error     string          `json:"error,omitempty"`
}

// ArtifactRecord records integrity and sensitivity metadata for one entry.
type ArtifactRecord struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Bytes       int64  `json:"bytes"`
	SHA256      string `json:"sha256"`
	Sensitivity string `json:"sensitivity"`
	Truncated   bool   `json:"truncated,omitempty"`
}

// BoundRecord makes the active resource limits explicit in every bundle.
type BoundRecord struct {
	TotalTimeoutMillis        int64 `json:"total_timeout_ms"`
	PerCollectorTimeoutMillis int64 `json:"per_collector_timeout_ms"`
	MaxArtifacts              int   `json:"max_artifacts"`
	MaxArtifactBytes          int64 `json:"max_artifact_bytes"`
	MaxUncompressedBytes      int64 `json:"max_uncompressed_bytes"`
	MaxCompressedBytes        int64 `json:"max_compressed_bytes"`
}

// Manifest is the stable archive contract. Fields may be added within a format
// version; incompatible changes increment FormatVersion.
type Manifest struct {
	FormatVersion     int               `json:"format_version"`
	Product           string            `json:"product,omitempty"`
	Version           string            `json:"version,omitempty"`
	Commit            string            `json:"commit,omitempty"`
	BuildProfile      string            `json:"build_profile,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	DurationMillis    int64             `json:"duration_ms"`
	RedactionProfile  string            `json:"redaction_profile"`
	SensitivityNotice string            `json:"sensitivity_notice"`
	Requested         []string          `json:"requested_collectors"`
	Collectors        []CollectorRecord `json:"collectors"`
	Artifacts         []ArtifactRecord  `json:"artifacts"`
	Bounds            BoundRecord       `json:"bounds"`
}

// Bundle is the bounded in-memory collection result. Archive serialization is
// streamed separately so compressed output is never assembled in memory.
type Bundle struct {
	Manifest  Manifest
	Artifacts []Artifact
	limits    Limits
}

// FileResult is returned after an archive is safely published.
type FileResult struct {
	Path            string   `json:"path"`
	CompressedBytes int64    `json:"compressed_bytes"`
	Manifest        Manifest `json:"manifest"`
}
