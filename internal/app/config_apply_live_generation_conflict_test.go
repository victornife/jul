// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/admin"
	"jul/internal/server"
)

// TestCoordinatorLiveGenerationConflictIsNotContentAware reproduces the
// mechanism behind #270 deterministically at the unit level: the coordinator
// rejects an apply/rollback whenever the live handler-generation advanced past
// the value the admin handler observed when it authorized the request, even
// when the candidate content does not actually conflict with anything now
// live. On the real server this generation also advances for a reload the
// file watcher fires for its own echo of an immediately preceding admin write
// (best-effort suppressed via a digest match, not guaranteed — see R10-01 in
// serve.go), so a rollback issued right after an apply can race that echo and
// see a "live runtime changed" conflict though the config it wants to persist
// is perfectly consistent with what is already live. This is the console-e2e
// "409 from /api/config/rollback" flake, reproduced without needing the real
// server, the file watcher, or any timing dependency.
func TestCoordinatorLiveGenerationConflictIsNotContentAware(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	const observedGeneration = uint64(41) // captured by the admin handler before calling into the coordinator
	const advancedGeneration = uint64(42) // bumped by an unrelated reload afterward, e.g. an unsuppressed watcher echo

	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(server.ReloadRequest) error {
			t.Fatal("SubmitReload must not be called; the generation conflict must reject before persistence")
			return nil
		},
		LiveSnapshot:   func() server.LiveSnapshot { return server.LiveSnapshot{Generation: advancedGeneration} },
		PlannedRestart: &PlannedRestartStore{},
	}

	// seed is byte-identical to what is already on disk and live: nothing
	// about the requested change actually conflicts with the running config.
	res, err := c.ApplyRaw(admin.ApplyRequestContext{LiveGeneration: observedGeneration}, seed, ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	if res.OK {
		t.Fatal("ok = true, want false: a bare generation bump is treated as a conflict regardless of content")
	}
	if !res.Conflict {
		t.Error("Conflict = false, want true")
	}
	if !strings.Contains(res.Message, "live runtime changed") {
		t.Errorf("message = %q, want a live-runtime-changed conflict", res.Message)
	}
}
