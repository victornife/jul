// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestCutoverPreviewUsesCapturedEffectiveIdentity(t *testing.T) {
	t.Setenv("JUL_CUTOVER_OLD", "http://127.0.0.1:9001")
	raw := config.ProxyTarget("${env:JUL_CUTOVER_OLD}", "127.0.0.1:8080")
	raw.Cache.Enabled = true
	raw.Cache.ReloadPolicy = "invalidate"
	raw.Servers[0].Locations[0].Cache = true
	originalBytes, err := config.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = config.Parse(originalBytes)
	if err != nil {
		t.Fatal(err)
	}
	before, err := config.NewCandidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	// The live snapshot retains A while resolving this exact same source now
	// produces B. Re-resolving the old source would erase that transition.
	t.Setenv("JUL_CUTOVER_OLD", "http://127.0.0.1:9002")
	rawBytes, err := config.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := previewRawCandidate(context.Background(), raw, before.Effective, lifecycle.Live{}, "base", rawBytes)
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Diff.CacheCutover == nil || assessment.Diff.CacheCutover.Disposition != "invalidate_on_publish" {
		t.Fatalf("lost effective cutover %+v", assessment.Diff)
	}
	encoded, err := json.Marshal(assessment.Diff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "127.0.0.1:9001") || strings.Contains(string(encoded), "127.0.0.1:9002") {
		t.Fatalf("resolved target leaked into preview: %s", encoded)
	}
	enabled := true
	loc := raw.Servers[0].Locations[0]
	batch, err := executePatchBatch(context.Background(), patchBatchBaseline{Config: raw, Effective: before.Effective, Version: "base"}, "base", []patchRequest{{Op: "route_toggle_cache", Listen: raw.Servers[0].Listen, ServerNames: raw.Servers[0].ServerNames, MatchType: loc.Match.Type, Path: loc.Match.Path, Enabled: &enabled}})
	if err != nil {
		t.Fatal(err)
	}
	if batch.Diff.CacheCutover == nil || batch.Diff.CacheCutover.Disposition != "invalidate_on_publish" {
		t.Fatalf("batch lost effective cutover: %+v", batch.Diff)
	}
	// A textual reference change that resolves to the same backend is not a
	// backend cutover. The structural diff still reports the source edit.
	t.Setenv("JUL_CUTOVER_NEW", "http://127.0.0.1:9001")
	candidate := strings.ReplaceAll(string(rawBytes), "JUL_CUTOVER_OLD", "JUL_CUTOVER_NEW")
	same, err := previewRawCandidate(context.Background(), raw, before.Effective, lifecycle.Live{}, "base", []byte(candidate))
	if err != nil {
		t.Fatal(err)
	}
	if same.Diff.CacheCutover != nil {
		t.Fatalf("raw-only reference edit invented cutover: %+v", same.Diff.CacheCutover)
	}
	if len(same.Diff.Modifications) == 0 {
		t.Fatal("structural source change disappeared")
	}
	// Missing serving evidence remains explicit even if resolving both raw
	// inputs under today's environment would produce an apparently equal B.
	unknown, err := previewRawCandidate(context.Background(), raw, nil, lifecycle.Live{}, "base", rawBytes)
	if err != nil {
		t.Fatal(err)
	}
	if unknown.Diff.CacheCutover == nil || unknown.Diff.CacheCutover.Disposition != "unavailable" {
		t.Fatalf("unknown baseline claimed retention: %+v", unknown.Diff)
	}
	if got := diffConfigsWithEffectiveCutover(raw, raw, before.Effective, nil, ""); got.CacheCutover == nil || got.CacheCutover.Disposition != "unavailable" {
		t.Fatal("missing candidate claimed retention")
	}
	if got := diffConfigsWithEffectiveCutover(&config.Config{}, &config.Config{}, nil, nil, ""); got.CacheCutover != nil {
		t.Fatal("disabled cache invented a cutover plan")
	}
}
