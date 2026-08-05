// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/server"
)

// TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears is the
// WS02 §3.9/§3.10 advisory-health test (package integration: real
// ConfigApplyCoordinator + real admin.Server history writer + real
// ManagedApplyRegistry, no HTTP server, no sleeps, no manually seeded ledger
// state, no mocked history writer). It proves two things through the SINGLE
// production finalizer wiring (coordinator.OnManagedApplyComplete =
// finalizer.Finalize):
//
//  1. A configuration-history snapshot FAILURE at terminalization publishes an
//     UNHEALTHY finalization advisory that carries the failing apply ID and a
//     bounded history detail — while the committed runtime apply itself STILL
//     SUCCEEDS (result.OK stays true). A finalization/history degradation never
//     turns an already-published apply into a failed apply.
//  2. A subsequent CLEAN finalize publishes a HEALTHY advisory, which CLEARS the
//     prior degradation (§3.9: "Clear advisory only after a subsequent managed
//     transaction finalizes without finalization/history degradation").
//
// The history failure is injected deterministically and WITHOUT mocking the
// trusted writer: the configured history directory path is created as a regular
// FILE, so the writer's os.MkdirAll(dir) fails for the first apply; removing the
// file before the second apply lets the real snapshot succeed.
func TestManagedApplyFinalizationAdvisoryTracksHistoryDegradationAndClears(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	histDir := filepath.Join(tmp, "history")
	if err := os.WriteFile(path, validConfigRaw(t, ":8080"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	// Poison the history directory: a regular file at histDir makes the trusted
	// writer's os.MkdirAll(histDir) fail, so the first terminal history snapshot
	// degrades — exercising the real RecordManagedHistory failure path.
	if err := os.WriteFile(histDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("poison history dir: %v", err)
	}

	registry := admin.NewManagedApplyRegistry(0, 0)
	adminSrv := admin.New(config.AdminConfig{
		Enabled:     true,
		Listen:      "127.0.0.1:0",
		HistoryDir:  histDir,
		HistoryKeep: 50,
	}, nil, admin.Deps{ManagedApplies: registry})
	if adminSrv == nil {
		t.Fatal("admin.New returned nil")
	}

	// Single advisory sink shared across BOTH applies — exactly as serve.go wires
	// the process-lifetime atomic pointer — so the clear-on-clean-finalize
	// behavior is observable across transactions.
	var advisory atomic.Pointer[admin.ManagedApplyAdvisory]
	var latest atomic.Pointer[admin.ManagedApplyOutcome]
	var latestSeq atomic.Uint64
	var latestMu sync.Mutex
	finalizer := &managedApplyFinalizer{
		registry:  registry,
		admin:     adminSrv,
		metrics:   observability.NewMetrics(),
		log:       nil,
		latest:    &latest,
		latestSeq: &latestSeq,
		latestMu:  &latestMu,
		setAdvisory: func(a admin.ManagedApplyAdvisory) {
			advisory.Store(&a)
		},
	}

	completedCh := make(chan admin.ManagedApplyFinalization, 1)
	reqCh := make(chan server.ReloadRequest, 1)
	c := &ConfigApplyCoordinator{
		BaseCtx:   context.Background(),
		Path:      path,
		Preflight: testPreflight(),
		SubmitReload: func(req server.ReloadRequest) error {
			reqCh <- req
			return nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			cfg := config.ProxyTarget("127.0.0.1:9000", ":8080")
			cfg.Global.ReloadTimeout = config.Duration(30 * time.Millisecond)
			return server.LiveSnapshot{EffectiveConfig: cfg}
		},
		waitMargin:     10 * time.Millisecond,
		PlannedRestart: &PlannedRestartStore{},
		OnManagedApplyStarted: func(start admin.ManagedApplyStart) error {
			applyID := start.Result.ApplyID
			if applyID == "" && start.Result.Reload != nil {
				applyID = start.Result.Reload.ID
			}
			if applyID == "" {
				return nil
			}
			return registry.BeginPending(admin.ManagedApplyRecord{
				ID:           applyID,
				State:        admin.ManagedApplyPending,
				Operation:    start.Context.Operation,
				StartedAt:    start.Context.StartedAt,
				Deadline:     start.Context.Deadline,
				Result:       start.Result,
				OwnerTokenID: start.Context.TokenID,
			})
		},
	}
	c.OnManagedApplyComplete = func(comp admin.ManagedApplyCompletion) admin.ManagedApplyFinalization {
		fin := finalizer.Finalize(comp)
		completedCh <- fin
		return fin
	}

	// runApply drives one committed hot apply to terminalization and returns the
	// synchronous apply result plus the terminal finalization provenance.
	runApply := func(t *testing.T, listen string) (admin.ConfigApplyResult, admin.ManagedApplyFinalization) {
		t.Helper()
		startedAt := time.Now().UTC()
		reqCtx := admin.ApplyRequestContext{
			Operation: admin.ApplyOperationConfigApply,
			StartedAt: startedAt,
			Deadline:  startedAt.Add(30 * time.Millisecond),
			TokenID:   "tok-owner-advisory",
			Actor:     "alice",
		}
		res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, listen), ApplyHot)
		if err != nil {
			t.Fatalf("apply error: %v", err)
		}
		if res.ApplyID == "" {
			t.Fatal("apply result carried no apply id")
		}
		req := <-reqCh
		req.Result <- server.ReloadResult{
			ID:             res.ApplyID,
			Source:         server.ReloadSourceAdmin,
			Outcome:        server.ReloadAppliedLive,
			Published:      true,
			DesiredVersion: server.CanonicalVersion(mustParse(t, validConfigRaw(t, listen))),
			ServingVersion: "v-live",
		}
		var fin admin.ManagedApplyFinalization
		select {
		case fin = <-completedCh:
		case <-time.After(2 * time.Second):
			t.Fatal("terminal finalization was not called")
		}
		return admin.ConfigApplyResult{ApplyID: res.ApplyID, OK: res.OK}, fin
	}

	// ── Apply #1: history snapshot fails → unhealthy advisory, apply still OK ──
	res1, fin1 := runApply(t, ":8081")
	if !res1.OK {
		t.Fatalf("apply #1 OK = false; a history degradation must not fail a committed apply")
	}
	if fin1.HistoryError == "" {
		t.Fatal("apply #1 did not surface the injected configuration-history failure")
	}
	adv1 := advisory.Load()
	if adv1 == nil {
		t.Fatal("history degradation did not publish a finalization advisory")
	}
	if adv1.Healthy {
		t.Errorf("advisory Healthy = true after a history failure, want false; detail=%q", adv1.Detail)
	}
	if adv1.ApplyID != res1.ApplyID {
		t.Errorf("advisory apply_id = %q, want %q", adv1.ApplyID, res1.ApplyID)
	}
	if !strings.Contains(adv1.Detail, "configuration history") {
		t.Errorf("advisory detail = %q, want it to mention the configuration-history degradation", adv1.Detail)
	}
	// The durable per-ID ledger record also carries the history degradation.
	if rec, ok := registry.Get(res1.ApplyID); !ok {
		t.Fatalf("terminal record for %q not found", res1.ApplyID)
	} else if rec.HistoryError == "" {
		t.Errorf("ledger record history_error empty, want the injected failure visible in the record")
	}

	// ── Un-poison the history dir so the next real snapshot can succeed. ──
	if err := os.Remove(histDir); err != nil {
		t.Fatalf("remove poisoned history dir: %v", err)
	}

	// ── Apply #2: clean finalize → healthy advisory, clearing the degradation ──
	res2, fin2 := runApply(t, ":8082")
	if !res2.OK {
		t.Fatalf("apply #2 OK = false, want true")
	}
	if fin2.FinalizationError != "" {
		t.Fatalf("apply #2 surfaced a finalization error: %q", fin2.FinalizationError)
	}
	if fin2.HistoryError != "" {
		t.Fatalf("apply #2 still reported a history error: %q", fin2.HistoryError)
	}
	if fin2.HistorySnapshotID == "" {
		t.Fatal("apply #2 clean finalize did not write a history snapshot")
	}
	adv2 := advisory.Load()
	if adv2 == nil {
		t.Fatal("clean finalize did not publish a finalization advisory")
	}
	if !adv2.Healthy {
		t.Errorf("advisory Healthy = false after a clean finalize; the prior degradation was not cleared. detail=%q", adv2.Detail)
	}
	if adv2.Detail != "" {
		t.Errorf("cleared advisory carried a detail = %q, want empty", adv2.Detail)
	}
	if adv2.ApplyID != res2.ApplyID {
		t.Errorf("cleared advisory apply_id = %q, want %q", adv2.ApplyID, res2.ApplyID)
	}
}
