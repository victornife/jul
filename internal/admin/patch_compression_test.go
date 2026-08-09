// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestCompressionSetSparseDisableRetainsDormantSettings(t *testing.T) {
	c := issue80BaseConfig()
	before := c.Compression
	before.Encoders = append([]string(nil), before.Encoders...)
	before.Types = append([]string(nil), before.Types...)

	summary, err := applyPatch(c, patchRequest{
		Op:          "compression_set",
		Compression: &compressionPatch{Enabled: ptr(false)},
	})
	if err != nil {
		t.Fatalf("disable compression: %v", err)
	}
	if summary != "compression fields changed: enabled" {
		t.Fatalf("summary = %q, want enabled field only", summary)
	}
	if c.Compression.IsEnabled() {
		t.Fatal("compression remains enabled")
	}
	if !slices.Equal(c.Compression.Encoders, before.Encoders) ||
		c.Compression.Level != before.Level ||
		c.Compression.MinSize != before.MinSize ||
		!slices.Equal(c.Compression.Types, before.Types) ||
		c.Compression.Precompressed != before.Precompressed {
		t.Fatalf("disable reset dormant settings: got %+v want %+v", c.Compression, before)
	}

	if _, err := applyPatch(c, patchRequest{
		Op:          "compression_set",
		Compression: &compressionPatch{Enabled: ptr(true)},
	}); err != nil {
		t.Fatalf("re-enable compression: %v", err)
	}
	if !c.Compression.IsEnabled() || !slices.Equal(c.Compression.Encoders, before.Encoders) ||
		!slices.Equal(c.Compression.Types, before.Types) || c.Compression.Level != before.Level {
		t.Fatalf("re-enable did not reuse dormant settings: %+v", c.Compression)
	}
}

func TestCompressionSetExplicitZeroFalseAndEmptyListsUseCanonicalDefaults(t *testing.T) {
	emptyEncoders := []string{}
	emptyTypes := []string{}
	baseline := issue80BaseConfig()
	baseline.Compression.Encoders = []string{"gzip", "gzip"}
	result, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: baseline,
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "compression_set",
		Compression: &compressionPatch{
			Enabled:       ptr(true),
			Encoders:      &emptyEncoders,
			Level:         ptr(0),
			MinSize:       ptr("0"),
			Types:         &emptyTypes,
			Precompressed: ptr(false),
		},
	}})
	if err != nil {
		t.Fatalf("execute compression reset: %v", err)
	}
	if !result.Valid {
		t.Fatalf("compression reset invalid: %+v", result.ValidationErrors)
	}
	got := result.CandidateConfig.Compression
	if !got.IsEnabled() {
		t.Fatal("explicit enabled=true was lost")
	}
	if !slices.Equal(got.Encoders, []string{"gzip"}) {
		t.Fatalf("encoders = %v, want parser default [gzip]", got.Encoders)
	}
	if got.Level != 0 {
		t.Fatalf("level = %d, want explicit encoder default 0", got.Level)
	}
	if got.MinSize != config.Size(1<<10) {
		t.Fatalf("min_size = %s, want parser default 1k", got.MinSize.String())
	}
	if !slices.Equal(got.Types, config.DefaultCompressionTypes()) {
		t.Fatalf("types = %v, want parser defaults %v", got.Types, config.DefaultCompressionTypes())
	}
	if got.Precompressed {
		t.Fatal("explicit precompressed=false was lost")
	}
	if !result.Lifecycle.CanApplyHot || len(result.Lifecycle.RestartRequired) != 0 {
		t.Fatalf("compression lifecycle = %+v, want hot-only", result.Lifecycle)
	}
	for _, path := range []string{"compression.encoders", "compression.level", "compression.min_size", "compression.precompressed", "compression.types"} {
		if !hasLifecyclePath(result.Lifecycle, path) {
			t.Errorf("lifecycle changes = %+v, missing %s", result.Lifecycle.Changes, path)
		}
	}
}

func TestCompressionSetDefensivelyCopiesSuppliedLists(t *testing.T) {
	c := issue80BaseConfig()
	encoders := []string{"gzip", "gzip"}
	types := []string{"text/*", "application/wasm"}
	if _, err := applyPatch(c, patchRequest{
		Op: "compression_set",
		Compression: &compressionPatch{
			Encoders: &encoders,
			Types:    &types,
		},
	}); err != nil {
		t.Fatalf("apply compression lists: %v", err)
	}

	// Duplicates are accepted just as raw TOML validation accepts them. More
	// importantly, request-owned backing arrays must never become config state.
	encoders[0] = "br"
	types[0] = "changed/type"
	if c.Compression.Encoders[0] != "gzip" || c.Compression.Types[0] != "text/*" {
		t.Fatalf("config aliases request slices: encoders=%v types=%v", c.Compression.Encoders, c.Compression.Types)
	}
}

func TestCompressionSetRejectsInvalidLateFieldWithoutMutation(t *testing.T) {
	c := issue80BaseConfig()
	before := mustConfigBytes(t, c)
	encoders := []string{"gzip", "br"}
	types := []string{"text/*"}
	_, err := applyPatch(c, patchRequest{
		Op: "compression_set",
		Compression: &compressionPatch{
			Enabled:       ptr(false),
			Encoders:      &encoders,
			Level:         ptr(7),
			Types:         &types,
			Precompressed: ptr(false),
			MinSize:       ptr("not-a-size"),
		},
	})
	if err == nil {
		t.Fatal("expected invalid min_size rejection")
	}
	if after := mustConfigBytes(t, c); !bytes.Equal(after, before) {
		t.Fatalf("rejected compression_set partially mutated config\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestCompressionSetValidationAndValueFreeSummary(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch compressionPatch
	}{
		{name: "empty payload", patch: compressionPatch{}},
		{name: "invalid encoder", patch: compressionPatch{Encoders: ptr([]string{"snappy"})}},
		{name: "blank encoder", patch: compressionPatch{Encoders: ptr([]string{""})}},
		{name: "blank MIME", patch: compressionPatch{Types: ptr([]string{"   "})}},
		{name: "negative level", patch: compressionPatch{Level: ptr(-1)}},
		{name: "high level", patch: compressionPatch{Level: ptr(12)}},
		{name: "negative size", patch: compressionPatch{MinSize: ptr("-1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := issue80BaseConfig()
			before := mustConfigBytes(t, c)
			_, err := applyPatch(c, patchRequest{Op: "compression_set", Compression: &tc.patch})
			if tc.name == "empty payload" {
				if err == nil || !strings.Contains(err.Error(), "at least one field") {
					t.Fatalf("error = %v, want presence rejection", err)
				}
			} else if err == nil {
				t.Fatal("expected validation rejection")
			}
			if after := mustConfigBytes(t, c); !bytes.Equal(after, before) {
				t.Fatal("rejected patch mutated config")
			}
		})
	}

	c := issue80BaseConfig()
	encoders := []string{"gzip", "br"}
	types := []string{"text/*"}
	summary, err := applyPatch(c, patchRequest{
		Op: "compression_set",
		Compression: &compressionPatch{
			Enabled:       ptr(false),
			Encoders:      &encoders,
			Level:         ptr(0),
			MinSize:       ptr("4k"),
			Types:         &types,
			Precompressed: ptr(false),
		},
	})
	if err != nil {
		t.Fatalf("apply all fields: %v", err)
	}
	want := "compression fields changed: enabled, encoders, level, min_size, types, precompressed"
	if summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	for _, forbidden := range []string{"false", "gzip", "br", "4k", "text/*"} {
		if strings.Contains(summary, forbidden) {
			t.Fatalf("summary leaked value %q: %q", forbidden, summary)
		}
	}
}

func TestCompressionSetNilPayloadRejected(t *testing.T) {
	if _, err := applyPatch(issue80BaseConfig(), patchRequest{Op: "compression_set"}); err == nil {
		t.Fatal("expected nil compression payload rejection")
	}
}
