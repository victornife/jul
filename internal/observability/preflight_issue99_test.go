// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"math"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestValidateTracerConfigRejectsNonFiniteSampleRatio(t *testing.T) {
	for _, ratio := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		err := ValidateTracerConfig(config.TracingConfig{SampleRatio: ratio})
		if err == nil {
			t.Fatalf("sample ratio %v accepted", ratio)
		}
		if !strings.Contains(err.Error(), "finite") {
			t.Fatalf("error=%q, want finite-value diagnostic", err)
		}
	}
}
