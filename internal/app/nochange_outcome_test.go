// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/server"
)

func TestNoChangeIsSuccessfulAndNeverRestored(t *testing.T) {
	if !reloadOutcomeSucceeded(server.ReloadNoChange) {
		t.Fatal("no_change was not classified as a successful reload")
	}
	rr := server.ReloadResult{
		ID:             "rl_test_1",
		Outcome:        server.ReloadNoChange,
		Persisted:      true,
		Published:      false,
		DesiredVersion: "v2",
		ServingVersion: "v2",
	}
	result := (&ConfigApplyCoordinator{}).decorateResultNoRestore(ApplyHot, "v2", "v2", rr)
	if !result.OK || result.Reload == nil || result.Reload.Published || !strings.Contains(result.Message, "already effective") {
		t.Fatalf("decorated no_change = %+v", result)
	}
	if got := managedRestoredLabel(admin.ConfigApplyResult{OK: true, Reload: &rr}); got != "n/a" {
		t.Fatalf("managed restored label = %q, want n/a", got)
	}
}

func TestManagedNoChangePersistsAndTerminalizesByExactID(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	// The extra comment changes the exact persisted bytes while preserving the
	// parsed/effective configuration, reproducing the non-timing-dependent
	// false-churn source behind duplicate managed/file events.
	candidate := append(append([]byte(nil), seed...), []byte("\n# semantic duplicate\n")...)
	live, err := config.Parse(seed)
	if err != nil {
		t.Fatal(err)
	}
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			req.Result <- server.ReloadResult{
				ID:             req.ID,
				Source:         server.ReloadSourceAdmin,
				Outcome:        server.ReloadNoChange,
				Persisted:      true,
				Published:      false,
				DesiredVersion: server.CanonicalVersion(req.Candidate.Effective),
				ServingVersion: server.CanonicalVersion(req.Candidate.Effective),
			}
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: live, Generation: 17}
		},
		PlannedRestart: &PlannedRestartStore{},
	}
	registry, _, completed := wireProductionLedger(c)
	res, err := c.ApplyRaw(admin.ApplyRequestContext{Operation: admin.ApplyOperationConfigApply}, candidate, ApplyHot)
	if err != nil {
		t.Fatalf("ApplyRaw: %v", err)
	}
	if !res.OK || res.Reload == nil || res.Reload.Outcome != server.ReloadNoChange || res.Restored {
		t.Fatalf("no-change apply = %+v", res)
	}
	<-completed
	if got, err := os.ReadFile(path); err != nil || string(got) != string(candidate) {
		t.Fatalf("persisted bytes = %q, %v; want exact candidate", got, err)
	}
	record, ok := registry.Get(res.ApplyID)
	if !ok || record.State != admin.ManagedApplyTerminal || !record.Result.OK || record.Result.Reload == nil || record.Result.Reload.Outcome != server.ReloadNoChange {
		t.Fatalf("terminal record = %+v, found=%v", record, ok)
	}
}

func TestManagedNoChangeHistoryAndLatestOutcomeStayTruthful(t *testing.T) {
	historyDir := filepath.Join(t.TempDir(), "history")
	registry := admin.NewManagedApplyRegistry(0, 0)
	adminSrv := admin.New(config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:0", HistoryDir: historyDir, HistoryKeep: 50,
	}, nil, admin.Deps{ManagedApplies: registry})
	if adminSrv == nil {
		t.Fatal("admin.New returned nil")
	}
	var latest atomic.Pointer[admin.ManagedApplyOutcome]
	var latestSeq atomic.Uint64
	var latestMu sync.Mutex
	finalizer := &managedApplyFinalizer{
		registry:  registry,
		admin:     adminSrv,
		latest:    &latest,
		latestSeq: &latestSeq,
		latestMu:  &latestMu,
	}
	result := admin.ConfigApplyResult{
		ApplyID: "rl_408", OK: true, Mode: string(ApplyHot), PersistedVersion: "v2",
		Reload: &server.ReloadResult{ID: "rl_408", Outcome: server.ReloadNoChange, Persisted: true},
	}
	if err := registry.BeginPending(admin.ManagedApplyRecord{ID: result.ApplyID, State: admin.ManagedApplyPending, Result: result}); err != nil {
		t.Fatal(err)
	}
	fin := finalizer.Finalize(admin.ManagedApplyCompletion{
		Context:     admin.ApplyRequestContext{Operation: admin.ApplyOperationConfigApply, Actor: "alice"},
		Result:      result,
		PreviousRaw: validConfigRaw(t, ":8080"),
	})
	if fin.HistorySnapshotID == "" || fin.HistoryError != "" || fin.FinalizationError != "" {
		t.Fatalf("finalization = %+v", fin)
	}
	data, err := os.ReadFile(filepath.Join(historyDir, fin.HistorySnapshotID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata admin.HistoryMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Outcome != string(server.ReloadNoChange) || metadata.ApplyID != result.ApplyID {
		t.Fatalf("history metadata = %+v", metadata)
	}
	if got := latest.Load(); got == nil || got.ID != result.ApplyID || got.Outcome != string(server.ReloadNoChange) || !got.OK {
		t.Fatalf("latest outcome = %+v", got)
	}
}
