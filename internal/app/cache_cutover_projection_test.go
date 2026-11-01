// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"encoding/json"
	"errors"
	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/server"
	"strings"
	"testing"
)

func TestCacheCutoverProjectionIsGenerationBoundAndSecretSafe(t *testing.T) {
	f := &HandlerFactory{Cache: &cache.Cache{}}
	if f.CacheCutoverResult(42) != nil {
		t.Fatal("unpublished generation inherited result")
	}
	f.recordCacheCutover(42, []cache.InvalidateResult{
		{InvalidationEffective: "effective", InvalidatedResources: 3},
		{InvalidationEffective: "incomplete", FailedEntries: 1, RemainingEntries: 2},
	}, nil)
	result := f.CacheCutoverResult(42)
	if result.State != "partial" || result.InvalidationEffective != "incomplete" || result.InvalidatedResources != 3 || result.FailedEntries != 1 || result.RemainingEntries != 2 || result.Operations != 2 {
		t.Fatalf("mixed outcomes misrepresented: %+v", result)
	}
	f.recordCacheCutover(43, []cache.InvalidateResult{{InvalidationEffective: "uncertain"}, {InvalidationEffective: "incomplete"}}, errors.New("/private/secret=[REDACTED_SECRET]"))
	result = f.CacheCutoverResult(43)
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != "uncertain" || strings.Contains(string(raw), "credential") {
		t.Fatalf("unsafe projection: %s", raw)
	}
	f.recordCacheCutover(44, nil, errors.New("epoch write failed"))
	if got := f.CacheCutoverResult(44); got.State != "uncertain" || got.RecoveryGuidance == "" {
		t.Fatalf("epoch-only failure silent: %+v", got)
	}
	f.recordCacheCutover(45, []cache.InvalidateResult{{InvalidationEffective: "effective", InvalidatedResources: 1}}, nil)
	if got := f.CacheCutoverResult(45); got.State != "succeeded" || got.RecoveryGuidance != "" {
		t.Fatalf("healthy cutover stale failure: %+v", got)
	}
	f.recordCacheCutover(46, []cache.InvalidateResult{{}}, nil)
	if got := f.CacheCutoverResult(46); got.State != "uncertain" {
		t.Fatalf("unknown outcome reported effective: %+v", got)
	}
	f.recordCacheCutover(47, nil, nil)
	if got := f.CacheCutoverResult(47); got.State != "not_required" {
		t.Fatal(got)
	}
	f.Cache = nil
	if f.CacheCutoverResult(47) != nil {
		t.Fatal("disabled cache reported work")
	}
	staged := toAdminConfigApplyResult(ApplyResult{OK: true, Mode: ApplyStageRestart})
	if staged.CacheCutoverState != "staged_for_restart" || staged.CacheCutoverResult != nil {
		t.Fatalf("stage fabricated cleanup: %+v", staged)
	}
	rr := server.ReloadResult{CacheCutover: result}
	outcome := projectManagedApplyOutcome(admin.ApplyRequestContext{}, admin.ConfigApplyResult{OK: true, Reload: &rr}, admin.ManagedApplyFinalization{})
	if outcome.CacheCutover == nil || outcome.CacheCutover.InvalidationEffective != "uncertain" {
		t.Fatalf("managed audit projection lost cutover: %+v", outcome)
	}
}
