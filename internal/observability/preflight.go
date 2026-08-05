// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"fmt"
	"os"
	"path/filepath"

	"jul/internal/config"
)

// PreflightAccessSinks validates that an AccessLogConfig can be applied at the
// next process startup without building the real sinks. For the "file" sink it
// proves the target directory is writable by creating and immediately removing
// a temporary sentinel file. It does not retain any file handle.
func PreflightAccessSinks(cfg config.AccessLogConfig) error {
	if !cfg.IsEnabled() {
		return nil
	}
	for _, name := range cfg.Sinks {
		if name != "file" {
			continue
		}
		if cfg.File == "" {
			// Config validation already rejects file sink without a path; skip
			// silently here so preflight is not the first to report the error.
			continue
		}
		dir := filepath.Dir(cfg.File)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("[observability.access_log] file sink: cannot create directory %q: %w", dir, err)
		}
		tmp, err := os.CreateTemp(dir, ".preflight-*")
		if err != nil {
			return fmt.Errorf("[observability.access_log] file sink: directory %q not writable: %w", dir, err)
		}
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		break // path is unique; only one file sink can be configured
	}
	return nil
}

// ValidateTracerConfig validates a TracingConfig without starting a tracing
// pipeline. It is a build-tag-agnostic config check: the actual tag check
// (whether the otel tag is compiled in) happens in NewTracer; this function
// only validates that required fields are populated when tracing is enabled.
func ValidateTracerConfig(cfg config.TracingConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.Endpoint == "" {
		return fmt.Errorf("[observability.tracing] endpoint is required when tracing is enabled")
	}
	switch cfg.Exporter {
	case "", "otlp-grpc", "otlp-http":
		// valid
	default:
		return fmt.Errorf("[observability.tracing] unknown exporter %q; valid values: otlp-grpc, otlp-http", cfg.Exporter)
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return fmt.Errorf("[observability.tracing] sample_ratio must be in [0, 1], got %g", cfg.SampleRatio)
	}
	return nil
}
