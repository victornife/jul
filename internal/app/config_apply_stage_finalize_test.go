// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

// TestStageRestartTerminalFinalizedThroughLedger proves the WS02 §3.8 routing
// invariant for the stage_restart terminal path: a committed stage is a
// persisted mutation, so it flows through the SINGLE completeManagedApply helper
// exactly like the hot path, producing exactly one terminal ledger record for
// its apply ID, and its terminal result carries first-class persistence truth
// (§3.2 defect 8): Persisted is true, FinalDiskVersion is the candidate now on
// disk, and FinalServingVersion is the still-serving live version.
//
// It is a package-integration test that drives a real ConfigApplyCoordinator
// (file-backed PlannedRestartStore, real stage preflight) wired to a real
// admin.ManagedApplyRegistry through the shared production-fidelity harness
// wireProductionLedger — the same OnManagedApplyStarted/OnManagedApplyComplete
// field mapping serve.go installs — so the terminal record is produced by the
// real terminal path, not a bespoke shim or manually seeded ledger state. No
// HTTP server, no sleeps, and no reload goroutine are involved: the stage path
// is synchronous and submits no live reload.
func TestStageRestartTerminalFinalizedThroughLedger(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")

	// The serving/base configuration on disk.
	original := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	liveCfg, err := config.Parse(original)
	if err != nil {
		t.Fatalf("parse live config: %v", err)
	}
	liveVersion := server.CanonicalVersion(liveCfg)

	store := NewFilePlannedRestartStore(path)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflightStage(t, original),
		// The live runtime keeps serving the original configuration: a stage
		// never touches it. Returning a non-empty snapshot lets the test prove
		// FinalServingVersion tracks the serving runtime, distinct from the
		// on-disk candidate version.
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{EffectiveConfig: liveCfg} },
		PlannedRestart: store,
	}
	registry, startedCh, completedCh := wireProductionLedger(c)

	// A restart-required candidate: differs from the serving config in a
	// restart-required field (log_format) so stage_restart is accepted.
	candidate := restartRequiredConfigRaw(t, ":8080")
	persistedVersion := server.CanonicalVersion(mustParse(t, candidate))

	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
		Actor:     "alice",
	}
	res, err := c.ApplyRaw(reqCtx, candidate, ApplyStageRestart)
	if err != nil {
		t.Fatalf("ApplyRaw stage_restart: %v", err)
	}
	if !res.OK {
		t.Fatalf("stage ok = false, want true; message: %s", res.Message)
	}
	if res.Mode != ApplyStageRestart {
		t.Errorf("mode = %v, want %v", res.Mode, ApplyStageRestart)
	}
	if !store.IsPending() {
		t.Fatal("planned restart should be pending after a committed stage")
	}

	// §3.2 defect 8 / §3.8: the stage terminal result carries first-class
	// persistence truth before it is routed through completeManagedApply.
	if !res.Persisted {
		t.Error("stage terminal result must report Persisted = true")
	}
	if res.PersistedVersion != persistedVersion {
		t.Errorf("PersistedVersion = %q, want %q", res.PersistedVersion, persistedVersion)
	}
	if res.FinalDiskVersion != persistedVersion {
		t.Errorf("FinalDiskVersion = %q, want the staged candidate on disk %q", res.FinalDiskVersion, persistedVersion)
	}
	if res.FinalServingVersion != liveVersion {
		t.Errorf("FinalServingVersion = %q, want the still-serving live version %q", res.FinalServingVersion, liveVersion)
	}
	// A stage advances the disk but never the running runtime, so the disk and
	// serving versions must differ — proving FinalServingVersion is not a copy
	// of the on-disk candidate version.
	if res.FinalDiskVersion == res.FinalServingVersion {
		t.Errorf("FinalDiskVersion (%q) must differ from FinalServingVersion (%q) for a stage", res.FinalDiskVersion, res.FinalServingVersion)
	}

	applyID := res.ApplyID
	if applyID == "" {
		t.Fatal("committed stage carried no apply id")
	}

	// A stage submits no live reload, so no provisional saved_not_live pending
	// record is registered: OnManagedApplyStarted must NOT have fired. The
	// terminal record is created directly by the single completion helper.
	select {
	case <-startedCh:
		t.Fatal("OnManagedApplyStarted fired for a stage_restart (no reload is enqueued)")
	default:
	}

	// The terminal completion callback must have fired exactly once with a
	// clean finalization (no finalization error on a clean stage).
	select {
	case fin := <-completedCh:
		if fin.FinalizationError != "" {
			t.Fatalf("unexpected finalization error on clean stage: %q", fin.FinalizationError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization callback was not called for the stage")
	}

	// Exactly one durable terminal record for this apply ID, carrying the same
	// persistence truth threaded through the production ledger.
	rec, ok := registry.Get(applyID)
	if !ok {
		t.Fatalf("no terminal ledger record for staged apply %q", applyID)
	}
	if rec.State != admin.ManagedApplyTerminal {
		t.Errorf("state = %q, want terminal", rec.State)
	}
	if !rec.Result.OK {
		t.Error("terminal record result ok = false, want true")
	}
	if !rec.Result.Persisted {
		t.Error("terminal record must report Persisted = true")
	}
	if rec.Result.FinalDiskVersion != persistedVersion {
		t.Errorf("terminal record FinalDiskVersion = %q, want %q", rec.Result.FinalDiskVersion, persistedVersion)
	}
	if rec.Result.FinalServingVersion != liveVersion {
		t.Errorf("terminal record FinalServingVersion = %q, want %q", rec.Result.FinalServingVersion, liveVersion)
	}
	if rec.FinalizationError != "" {
		t.Errorf("terminal record carried a finalization error on a clean stage: %q", rec.FinalizationError)
	}
	if registry.TerminalCount() != 1 {
		t.Errorf("terminal count = %d, want exactly 1 for a single committed stage", registry.TerminalCount())
	}
}
