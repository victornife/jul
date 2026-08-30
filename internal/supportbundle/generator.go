// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"jul/internal/diagnostics"
)

// Generator owns one fixed collector registry and a bounded concurrency gate.
type Generator struct {
	collectors []Collector
	limits     Limits
	semaphore  chan struct{}
	now        func() time.Time
}

// NewGenerator creates a generator. maxConcurrent defaults to one, preventing
// repeated operator requests from multiplying collection resource use.
func NewGenerator(collectors []Collector, limits Limits, maxConcurrent int) *Generator {
	limits = normalizeLimits(limits)
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	return &Generator{
		collectors: append([]Collector(nil), collectors...),
		limits:     limits,
		semaphore:  make(chan struct{}, maxConcurrent),
		now:        time.Now,
	}
}

// Build executes collectors in deterministic registry order. Collector errors,
// panics and per-collector timeouts are recorded and collection continues. A
// total cancellation/timeout aborts the build so callers never publish a bundle
// whose global consistency bound was exceeded.
func (generator *Generator) Build(ctx context.Context, snapshot Snapshot) (Bundle, error) {
	if err := generator.acquire(ctx); err != nil {
		return Bundle{}, err
	}
	defer generator.release()

	started := generator.now().UTC()
	runCtx, cancel := context.WithTimeout(ctx, generator.limits.TotalTimeout)
	defer cancel()
	if err := runCtx.Err(); err != nil {
		return Bundle{}, err
	}

	manifest := Manifest{
		FormatVersion:     FormatVersion,
		Product:           diagnostics.SanitizeString(snapshot.Product),
		Version:           diagnostics.SanitizeString(snapshot.Version),
		Commit:            diagnostics.SanitizeString(snapshot.Commit),
		BuildProfile:      diagnostics.SanitizeString(snapshot.BuildProfile),
		CreatedAt:         started,
		RedactionProfile:  RedactionProfile,
		SensitivityNotice: "Review this operator-generated bundle before sharing. Structural exclusions and redaction reduce risk but cannot guarantee absence of every business-sensitive identifier.",
		Requested:         make([]string, 0, len(generator.collectors)),
		Collectors:        make([]CollectorRecord, 0, len(generator.collectors)),
		Bounds:            boundsRecord(generator.limits),
	}

	bundle := Bundle{Manifest: manifest, limits: generator.limits}
	seen := make(map[string]struct{})
	var uncompressed int64

	for _, collector := range generator.collectors {
		if err := runCtx.Err(); err != nil {
			return Bundle{}, err
		}
		name := diagnostics.SanitizeString(collector.Name())
		bundle.Manifest.Requested = append(bundle.Manifest.Requested, name)

		artifacts, collectErr := generator.runCollector(runCtx, collector, snapshot)
		record := CollectorRecord{Name: name}
		if collectErr != nil {
			record.Status = CollectorError
			record.Error = diagnostics.SanitizeErrorString(collectErr.Error())
			bundle.Manifest.Collectors = append(bundle.Manifest.Collectors, record)
			continue
		}
		if len(artifacts) == 0 {
			record.Status = CollectorSkipped
			bundle.Manifest.Collectors = append(bundle.Manifest.Collectors, record)
			continue
		}

		prepared := make([]Artifact, 0, len(artifacts))
		preparedRecords := make([]ArtifactRecord, 0, len(artifacts))
		localSeen := make(map[string]struct{})
		var localBytes int64
		truncated := false
		var validationErr error
		for _, artifact := range artifacts {
			cleaned, err := generator.prepareArtifact(artifact, snapshot.RedactValues)
			if err != nil {
				validationErr = err
				break
			}
			if _, ok := seen[cleaned.Path]; ok {
				validationErr = fmt.Errorf("%w: %s", ErrDuplicateArtifact, cleaned.Path)
				break
			}
			if _, ok := localSeen[cleaned.Path]; ok {
				validationErr = fmt.Errorf("%w: %s", ErrDuplicateArtifact, cleaned.Path)
				break
			}
			if len(bundle.Artifacts)+len(prepared)+1 > generator.limits.MaxArtifacts {
				validationErr = ErrTooManyArtifacts
				break
			}
			localSeen[cleaned.Path] = struct{}{}
			localBytes += int64(len(cleaned.Data))
			if uncompressed+localBytes > generator.limits.MaxUncompressedBytes {
				validationErr = ErrBundleTooLarge
				break
			}
			if cleaned.Truncated {
				truncated = true
			}
			digest := sha256.Sum256(cleaned.Data)
			prepared = append(prepared, cleaned)
			preparedRecords = append(preparedRecords, ArtifactRecord{
				Path:        cleaned.Path,
				ContentType: cleaned.ContentType,
				Bytes:       int64(len(cleaned.Data)),
				SHA256:      hex.EncodeToString(digest[:]),
				Sensitivity: cleaned.Sensitivity,
				Truncated:   cleaned.Truncated,
			})
		}
		if validationErr != nil {
			record.Status = CollectorError
			record.Error = diagnostics.SanitizeErrorString(validationErr.Error())
			bundle.Manifest.Collectors = append(bundle.Manifest.Collectors, record)
			continue
		}
		for artifactPath := range localSeen {
			seen[artifactPath] = struct{}{}
		}
		bundle.Artifacts = append(bundle.Artifacts, prepared...)
		bundle.Manifest.Artifacts = append(bundle.Manifest.Artifacts, preparedRecords...)
		uncompressed += localBytes
		record.Artifacts = len(prepared)
		if truncated {
			record.Status = CollectorTruncated
		} else {
			record.Status = CollectorSuccess
		}
		bundle.Manifest.Collectors = append(bundle.Manifest.Collectors, record)
	}

	sort.SliceStable(bundle.Artifacts, func(i, j int) bool { return bundle.Artifacts[i].Path < bundle.Artifacts[j].Path })
	sort.SliceStable(bundle.Manifest.Artifacts, func(i, j int) bool { return bundle.Manifest.Artifacts[i].Path < bundle.Manifest.Artifacts[j].Path })
	bundle.Manifest.DurationMillis = generator.now().UTC().Sub(started).Milliseconds()
	if bundle.Manifest.DurationMillis < 0 {
		bundle.Manifest.DurationMillis = 0
	}
	return bundle, nil
}

func (generator *Generator) acquire(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case generator.semaphore <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-generator.semaphore
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (generator *Generator) release() { <-generator.semaphore }

func (generator *Generator) runCollector(parent context.Context, collector Collector, snapshot Snapshot) (artifacts []Artifact, err error) {
	ctx, cancel := context.WithTimeout(parent, generator.limits.PerCollectorTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("collector panicked: %v", recovered)
			artifacts = nil
		}
		if err == nil && ctx.Err() != nil {
			err = ctx.Err()
		}
	}()
	artifacts, err = collector.Collect(ctx, snapshot)
	return artifacts, err
}

func (generator *Generator) prepareArtifact(artifact Artifact, redactValues []string) (Artifact, error) {
	cleanPath, err := safeArtifactPath(artifact.Path)
	if err != nil {
		return Artifact{}, err
	}
	artifact.Path = cleanPath
	artifact.ContentType = diagnostics.SanitizeString(artifact.ContentType)
	artifact.Sensitivity = diagnostics.SanitizeString(artifact.Sensitivity)
	artifact.Data = redactExactValues(artifact.Data, redactValues)

	switch {
	case strings.Contains(strings.ToLower(artifact.ContentType), "json"):
		clean, sanitizeErr := diagnostics.SanitizeJSON(artifact.Data)
		if sanitizeErr != nil {
			return Artifact{}, fmt.Errorf("sanitize JSON artifact %s: %w", artifact.Path, sanitizeErr)
		}
		artifact.Data = append(clean, '\n')
	case strings.HasPrefix(strings.ToLower(artifact.ContentType), "text/"):
		artifact.Data = sanitizeText(artifact.Data)
	}

	if int64(len(artifact.Data)) > generator.limits.MaxArtifactBytes {
		if strings.HasPrefix(strings.ToLower(artifact.ContentType), "text/") {
			artifact.Data = truncateTextArtifact(artifact.Data, generator.limits.MaxArtifactBytes)
			artifact.Truncated = true
		} else {
			return Artifact{}, fmt.Errorf("%w: %s", ErrArtifactTooLarge, artifact.Path)
		}
	}
	return artifact, nil
}

func truncateTextArtifact(data []byte, limit int64) []byte {
	if limit <= 0 {
		return nil
	}
	marker := []byte("\n[truncated by support-bundle artifact limit]\n")
	if limit <= int64(len(marker)) {
		return append([]byte(nil), marker[:int(limit)]...)
	}
	prefixBytes := int(limit) - len(marker)
	out := make([]byte, 0, int(limit))
	out = append(out, data[:prefixBytes]...)
	out = append(out, marker...)
	return out
}

func safeArtifactPath(value string) (string, error) {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArtifactPath, value)
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || clean != value {
		return "", fmt.Errorf("%w: %q", ErrUnsafeArtifactPath, value)
	}
	return clean, nil
}

func redactExactValues(data []byte, values []string) []byte {
	out := append([]byte(nil), data...)
	ordered := append([]string(nil), values...)
	sort.SliceStable(ordered, func(i, j int) bool { return len(ordered[i]) > len(ordered[j]) })
	for _, value := range ordered {
		if value == "" {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(value), []byte("[REDACTED]"))
	}
	return out
}

func sanitizeText(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	var out bytes.Buffer
	for i, line := range lines {
		out.WriteString(diagnostics.SanitizeString(string(line)))
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
	}
	return out.Bytes()
}

func normalizeLimits(limits Limits) Limits {
	defaults := DefaultLimits()
	if limits.TotalTimeout <= 0 {
		limits.TotalTimeout = defaults.TotalTimeout
	}
	if limits.PerCollectorTimeout <= 0 {
		limits.PerCollectorTimeout = defaults.PerCollectorTimeout
	}
	if limits.MaxArtifacts <= 0 {
		limits.MaxArtifacts = defaults.MaxArtifacts
	}
	if limits.MaxArtifactBytes <= 0 {
		limits.MaxArtifactBytes = defaults.MaxArtifactBytes
	}
	if limits.MaxUncompressedBytes <= 0 {
		limits.MaxUncompressedBytes = defaults.MaxUncompressedBytes
	}
	if limits.MaxCompressedBytes <= 0 {
		limits.MaxCompressedBytes = defaults.MaxCompressedBytes
	}
	return limits
}

func boundsRecord(limits Limits) BoundRecord {
	return BoundRecord{
		TotalTimeoutMillis:        limits.TotalTimeout.Milliseconds(),
		PerCollectorTimeoutMillis: limits.PerCollectorTimeout.Milliseconds(),
		MaxArtifacts:              limits.MaxArtifacts,
		MaxArtifactBytes:          limits.MaxArtifactBytes,
		MaxUncompressedBytes:      limits.MaxUncompressedBytes,
		MaxCompressedBytes:        limits.MaxCompressedBytes,
	}
}
