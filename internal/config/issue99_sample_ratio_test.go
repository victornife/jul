// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"math"
	"strings"
	"testing"
)

func TestTracingSampleRatioTOMLPresenceSemantics(t *testing.T) {
	cases := []struct {
		name  string
		field string
		want  float64
	}{
		{"omitted defaults to one", "", 1},
		{"explicit zero stays zero", "sample_ratio = 0\n", 0},
		{"fraction stays fractional", "sample_ratio = 0.25\n", 0.25},
		{"explicit one stays one", "sample_ratio = 1\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := "[observability.tracing]\nenabled = true\nendpoint = \"collector:4317\"\n" + tc.field
			cfg, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got := cfg.Observability.Tracing.SampleRatio; got != tc.want {
				t.Fatalf("sample_ratio=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestTracingSampleRatioTOMLRejectsNonFiniteValues(t *testing.T) {
	for _, token := range []string{"nan", "inf", "-inf"} {
		t.Run(token, func(t *testing.T) {
			raw := "[observability.tracing]\nenabled = true\nendpoint = \"collector:4317\"\nsample_ratio = " + token + "\n"
			cfg, err := Parse([]byte(raw))
			if err != nil {
				t.Fatalf("TOML parser should accept %s so semantic validation can reject it: %v", token, err)
			}
			errs := validateTracing(cfg.Observability.Tracing)
			if len(errs) == 0 {
				t.Fatalf("validateTracing accepted sample_ratio=%s", token)
			}
			if !strings.Contains(errs[0].Error(), "finite") {
				t.Fatalf("error=%q, want finite-value diagnostic", errs[0])
			}
		})
	}
}

func TestValidateTracingSampleRatioFiniteBoundaries(t *testing.T) {
	for _, ratio := range []float64{0, 0.25, 1} {
		if err := ValidateTracingSampleRatio(ratio); err != nil {
			t.Fatalf("ratio %v rejected: %v", ratio, err)
		}
	}
	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.01, 1.01} {
		if err := ValidateTracingSampleRatio(ratio); err == nil {
			t.Fatalf("ratio %v accepted", ratio)
		}
	}
}
