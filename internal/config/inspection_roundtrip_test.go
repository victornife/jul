// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

func TestInspectionDefaultsAndExplicitZeroRoundTrip(t *testing.T) {
	defaults, err := Parse([]byte("[global.inspection]\n"))
	if err != nil {
		t.Fatal(err)
	}

	if defaults.Global.Inspection.Mode != "enforce" ||
		defaults.Global.Inspection.MaxBufferedBytes == nil ||
		*defaults.Global.Inspection.MaxBufferedBytes != Size(256<<20) ||
		defaults.Global.Inspection.MaxResponseHeaderBytes == nil ||
		*defaults.Global.Inspection.MaxResponseHeaderBytes != Size(64<<10) ||
		defaults.Global.Inspection.MaxConcurrentEvaluations != 0 ||
		defaults.Global.Inspection.EvaluationQueueTimeout == nil ||
		*defaults.Global.Inspection.EvaluationQueueTimeout != Duration(time.Second) ||
		defaults.Global.Inspection.BodyTimeout == nil ||
		*defaults.Global.Inspection.BodyTimeout != Duration(60*time.Second) {
		t.Fatalf("inspection defaults = %+v", defaults.Global.Inspection)
	}

	explicitZero, err := Parse([]byte(`[global.inspection]
mode = "observe"
evaluation_queue_timeout = "0s"
body_timeout = "0s"
max_concurrent_evaluations = 0
max_response_header_bytes = "96k"
`))
	if err != nil {
		t.Fatal(err)
	}
	if explicitZero.Global.Inspection.EvaluationQueueTimeout == nil ||
		*explicitZero.Global.Inspection.EvaluationQueueTimeout != 0 ||
		explicitZero.Global.Inspection.BodyTimeout == nil ||
		*explicitZero.Global.Inspection.BodyTimeout != 0 {
		t.Fatalf("explicit zeros lost during parse: %+v", explicitZero.Global.Inspection)
	}
	clone, err := explicitZero.Clone()
	if err != nil {
		t.Fatal(err)
	}
	data, err := Marshal(clone)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`evaluation_queue_timeout = '0s'`, `body_timeout = '0s'`, `max_concurrent_evaluations = 0`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("canonical TOML omitted %q:\n%s", field, data)
		}
	}
	roundTrip, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Global.Inspection.Mode != "observe" ||
		roundTrip.Global.Inspection.EvaluationQueueTimeout == nil ||
		*roundTrip.Global.Inspection.EvaluationQueueTimeout != 0 ||
		roundTrip.Global.Inspection.BodyTimeout == nil ||
		*roundTrip.Global.Inspection.BodyTimeout != 0 ||
		roundTrip.Global.Inspection.MaxConcurrentEvaluations != 0 ||
		roundTrip.Global.Inspection.MaxResponseHeaderBytes == nil ||
		*roundTrip.Global.Inspection.MaxResponseHeaderBytes != Size(96<<10) {
		t.Fatalf("inspection values changed after round-trip: %+v", roundTrip.Global.Inspection)
	}
}

func TestStructuralValidationAppliesNoRouteIndependentBudgetFloor(t *testing.T) {
	cfg := ProxyTarget("127.0.0.1:9000", ":8080")
	headerLimit := Size(1)
	cfg.Global.Inspection.MaxResponseHeaderBytes = &headerLimit
	budget := Size(1024)
	cfg.Global.Inspection.MaxBufferedBytes = &budget
	// An uninspected configuration needs no inspection budget; the
	// effective-route minimum is enforced where compiled routes are known.
	for _, mode := range []string{"enforce", "observe"} {
		cfg.Global.Inspection.Mode = mode
		if err := Validate(cfg); err != nil {
			t.Fatalf("%s mode rejected a small budget on an uninspected configuration: %v", mode, err)
		}
	}
}

func TestInspectionValidationRejectsInvalidValues(t *testing.T) {
	sizePointer := func(value int64) *Size {
		v := Size(value)
		return &v
	}
	durationPointer := func(value time.Duration) *Duration {
		v := Duration(value)
		return &v
	}
	tests := []struct {
		name      string
		configure func(*Config)
		want      string
	}{
		{
			name: "invalid mode",
			configure: func(c *Config) {
				c.Global.Inspection.Mode = "invalid"
			},
			want: "[global.inspection].mode",
		},
		{
			name: "zero byte limit",
			configure: func(c *Config) {
				c.Global.Inspection.MaxBufferedBytes = sizePointer(0)
			},
			want: "[global.inspection].max_buffered_bytes",
		},
		{
			name: "non-positive response-header ceiling",
			configure: func(c *Config) {
				c.Global.Inspection.MaxResponseHeaderBytes = sizePointer(-1)
			},
			want: "[global.inspection].max_response_header_bytes",
		},
		{
			name: "response-header minimum overflow",
			configure: func(c *Config) {
				c.Global.Inspection.MaxResponseHeaderBytes = sizePointer(int64(^uint64(0) >> 1))
			},
			want: "too large to calculate the R3 estimate",
		},
		{
			name: "negative evaluation slots",
			configure: func(c *Config) {
				c.Global.Inspection.MaxConcurrentEvaluations = -1
			},
			want: "[global.inspection].max_concurrent_evaluations",
		},
		{
			name: "evaluation slots above maximum",
			configure: func(c *Config) {
				c.Global.Inspection.MaxConcurrentEvaluations = 1025
			},
			want: "[global.inspection].max_concurrent_evaluations",
		},
		{
			name: "negative queue timeout",
			configure: func(c *Config) {
				c.Global.Inspection.EvaluationQueueTimeout = durationPointer(-time.Second)
			},
			want: "[global.inspection].evaluation_queue_timeout",
		},
		{
			name: "queue timeout above maximum",
			configure: func(c *Config) {
				c.Global.Inspection.EvaluationQueueTimeout = durationPointer(10*time.Second + 1)
			},
			want: "[global.inspection].evaluation_queue_timeout",
		},
		{
			name: "negative body timeout",
			configure: func(c *Config) {
				c.Global.Inspection.BodyTimeout = durationPointer(-time.Second)
			},
			want: "[global.inspection].body_timeout",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ProxyTarget("127.0.0.1:9000", ":8080")
			tt.configure(cfg)
			err := Validate(cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, tt.want)
			}
		})
	}
}
