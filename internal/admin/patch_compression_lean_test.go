// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !brotli

package admin

import (
	"context"
	"strings"
	"testing"
)

func TestCompressionSetUnsupportedEncoderFailsAuthoritativePreflight(t *testing.T) {
	baseline := issue80BaseConfig()
	before := mustConfigBytes(t, baseline)
	encoders := []string{"br"}
	result, err := executePatchBatch(context.Background(), patchBatchBaseline{Config: baseline}, "", []patchRequest{{
		Op:          "compression_set",
		Compression: &compressionPatch{Encoders: &encoders},
	}})
	if err != nil {
		t.Fatalf("execute compression patch: %v", err)
	}
	if result.Valid {
		t.Fatal("lean build accepted brotli encoder")
	}
	joined := result.summaryText()
	for _, validationErr := range result.ValidationErrors {
		joined += " " + validationErr.Summary + " " + validationErr.Detail
	}
	if !strings.Contains(joined, "not compiled in this build") {
		t.Fatalf("validation errors = %+v, want build-tag preflight rejection", result.ValidationErrors)
	}
	if after := mustConfigBytes(t, baseline); string(after) != string(before) {
		t.Fatal("authoritative preflight mutated baseline")
	}
}
