// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"jul/internal/config"
)

// TestTracingRestartRequired pins the hot-apply policy for tracing: the
// OpenTelemetry tracer is wired once at startup, so any change to the
// [observability.tracing] block requires a restart, while an unchanged block
// hot-applies (it is a no-op for the running tracer).
func TestTracingRestartRequired(t *testing.T) {
	withTracing := func(mutate func(tr *config.TracingConfig)) *config.Config {
		c := &config.Config{}
		c.Observability.Tracing = config.TracingConfig{
			Enabled:  true,
			Exporter: "otlp-grpc",
			Endpoint: "localhost:4317",
		}
		if mutate != nil {
			mutate(&c.Observability.Tracing)
		}
		return c
	}

	t.Run("unchanged tracing hot-applies", func(t *testing.T) {
		if _, need := TracingRestartRequired(withTracing(nil), withTracing(nil)); need {
			t.Fatal("an unchanged tracing block must not require a restart")
		}
	})

	t.Run("enabling tracing requires restart", func(t *testing.T) {
		off := &config.Config{}
		if _, need := TracingRestartRequired(off, withTracing(nil)); !need {
			t.Fatal("enabling tracing must require a restart")
		}
	})

	t.Run("changing the endpoint requires restart", func(t *testing.T) {
		next := withTracing(func(tr *config.TracingConfig) { tr.Endpoint = "collector:4317" })
		reason, need := TracingRestartRequired(withTracing(nil), next)
		if !need {
			t.Fatal("changing the collector endpoint must require a restart")
		}
		if reason == "" {
			t.Fatal("expected a non-empty reason")
		}
	})

	t.Run("changing the sample ratio requires restart", func(t *testing.T) {
		next := withTracing(func(tr *config.TracingConfig) { tr.SampleRatio = 0.1 })
		if _, need := TracingRestartRequired(withTracing(nil), next); !need {
			t.Fatal("changing the sample ratio must require a restart")
		}
	})

	t.Run("disabling tracing requires restart", func(t *testing.T) {
		if _, need := TracingRestartRequired(withTracing(nil), &config.Config{}); !need {
			t.Fatal("disabling tracing must require a restart (the tracer stays wired)")
		}
	})
}
