// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
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

// rawSnapshotCount returns the number of raw configuration-history snapshots in
// dir (files whose name is not the ".json" provenance sidecar). It is used to
// prove the terminal finalizer writes the history snapshot exactly once per
// apply ID — a duplicate terminal callback must not add a second snapshot.
func rawSnapshotCount(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read history dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		n++
	}
	return n
}

// scrapeMetricLine returns the value token of the single unlabeled Prometheus
// sample named `name` exported by m, or fails when it is absent. It reads the
// public exposition handler so a test can assert a metric without touching the
// metrics package's private registry.
func scrapeMetricLine(t *testing.T, m *observability.Metrics, name string) string {
	t.Helper()
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == name {
			return fields[1]
		}
	}
	t.Fatalf("metric %q not found in exposition output", name)
	return ""
}

// TestManagedApplyFinalizerExactlyOnce is the WS02 §3.7 orchestrator test
// (package integration: real ConfigApplyCoordinator + real admin.Server history
// writer + real ManagedApplyRegistry, no HTTP server, no sleeps, no manually
// seeded ledger state). It proves the single managedApplyFinalizer.Finalize
// claims the transaction BEFORE the trusted configuration-history write and runs
// every terminal side effect exactly once per apply ID:
//
//   - the terminal reload result flows through the production wiring
//     (coordinator.OnManagedApplyComplete = finalizer.Finalize);
//   - exactly one raw history snapshot is written, and its snapshot id is
//     threaded onto the returned finalization and the durable per-ID ledger
//     record;
//   - the singular latest-outcome pointer advances to the finalized outcome;
//   - a DUPLICATE terminal callback for the same apply ID is deduplicated
//     through the ClaimFinalization guard: it repeats no history snapshot, adds
//     no new failure, and returns the already-recorded provenance.
//
// This directly exercises the §3.2 defects the slice fixes: history no longer
// runs before the finalization claim (defect 2), the terminal-ledger Complete
// error path is surfaced rather than ignored (defect 4), and FinalizationError
// is reliably threaded onto the terminal result (defect 6).
func TestManagedApplyFinalizerExactlyOnce(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "server.toml")
	histDir := filepath.Join(tmp, "history")
	seed := validConfigRaw(t, ":8080")
	if err := os.WriteFile(path, seed, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}

	// Real admin server with configuration history enabled, over a real ledger.
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

	// The finalizer is constructed with the EXACT field mapping serve.go installs
	// in production, including the advisory-health sink and the latest-outcome
	// high-water guard, so this test exercises the real composition-root wiring.
	var latest atomic.Pointer[admin.ManagedApplyOutcome]
	var latestSeq atomic.Uint64
	var latestMu sync.Mutex
	var advisory atomic.Pointer[admin.ManagedApplyAdvisory]
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

	// Capture the terminal completion object and observe the finalization without
	// racing the coordinator's async finalizer goroutine. The wrapper delegates
	// to finalizer.Finalize unchanged — it only records the completion and signals
	// the test — so production behavior is preserved.
	var compMu sync.Mutex
	var lastComp admin.ManagedApplyCompletion
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
		compMu.Lock()
		lastComp = comp
		compMu.Unlock()
		fin := finalizer.Finalize(comp)
		completedCh <- fin
		return fin
	}

	// 1. Submit a managed apply; the reload is withheld so ApplyRaw returns the
	//    provisional saved_not_live 202 after its bounded wait expires.
	reqCtx := admin.ApplyRequestContext{
		Operation: admin.ApplyOperationConfigApply,
		StartedAt: time.Now().UTC(),
		Deadline:  time.Now().Add(time.Minute).UTC(),
		TokenID:   "tok-owner-finalizer",
		Actor:     "alice",
	}
	res, err := c.ApplyRaw(reqCtx, validConfigRaw(t, ":8081"), ApplyHot)
	if err != nil {
		t.Fatalf("apply error: %v", err)
	}
	applyID := res.ApplyID
	if applyID == "" {
		t.Fatal("apply result carried no apply id")
	}

	// 2. Release the terminal reload completion (applied_live, published).
	req := <-reqCh
	req.Result <- server.ReloadResult{
		ID:             applyID,
		Source:         server.ReloadSourceAdmin,
		Outcome:        server.ReloadAppliedLive,
		Published:      true,
		DesiredVersion: server.CanonicalVersion(mustParse(t, validConfigRaw(t, ":8081"))),
		ServingVersion: "v2",
	}

	// 3. The finalizer terminalizes exactly once. FinalizationError must be empty
	//    and a history snapshot id must be threaded back onto the finalization.
	var fin admin.ManagedApplyFinalization
	select {
	case fin = <-completedCh:
	case <-time.After(2 * time.Second):
		t.Fatal("terminal finalization was not called")
	}
	if fin.FinalizationError != "" {
		t.Fatalf("unexpected finalization error: %q", fin.FinalizationError)
	}
	if fin.HistorySnapshotID == "" {
		t.Fatal("committed apply did not thread a history snapshot id onto the finalization")
	}

	// Exactly one raw history snapshot was written.
	if got := rawSnapshotCount(t, histDir); got != 1 {
		t.Fatalf("raw history snapshots after finalize = %d, want 1", got)
	}

	// The durable per-ID terminal ledger record carries the history provenance.
	rec, ok := registry.Get(applyID)
	if !ok {
		t.Fatalf("terminal record for %q not found in ledger", applyID)
	}
	if rec.State != admin.ManagedApplyTerminal {
		t.Fatalf("ledger state = %q, want terminal", rec.State)
	}
	if rec.HistorySnapshotID != fin.HistorySnapshotID {
		t.Errorf("ledger history_snapshot_id = %q, want %q", rec.HistorySnapshotID, fin.HistorySnapshotID)
	}
	if rec.FinalizationError != "" {
		t.Errorf("ledger finalization_error = %q, want empty", rec.FinalizationError)
	}

	// WS06 §7.5: the terminal completion published the retained-ledger gauge from
	// the real registry depth (TerminalCount == 1) through the production
	// finalizer wiring.
	if got := scrapeMetricLine(t, finalizer.metrics, "jul_managed_apply_terminal_registry_entries"); got != "1" {
		t.Errorf("jul_managed_apply_terminal_registry_entries = %q, want 1", got)
	}

	// The singular latest-outcome pointer advanced to this finalized outcome.
	if got := latest.Load(); got == nil || got.ID != applyID || !got.OK {
		t.Fatalf("latest outcome = %+v, want OK outcome for %q", got, applyID)
	}
	// WS02 §3.9: a clean finalize publishes a HEALTHY advisory (not nil) carrying
	// this apply ID and no detail — it clears any prior degradation rather than
	// leaving the advisory unset.
	if adv := advisory.Load(); adv == nil {
		t.Fatal("clean finalize did not publish a managed-apply finalization advisory")
	} else {
		if !adv.Healthy {
			t.Errorf("advisory Healthy = false on a clean finalize; detail=%q", adv.Detail)
		}
		if adv.Detail != "" {
			t.Errorf("advisory Detail = %q on a clean finalize, want empty", adv.Detail)
		}
		if adv.ApplyID != applyID {
			t.Errorf("advisory ApplyID = %q, want %q", adv.ApplyID, applyID)
		}
	}

	// 4. A DUPLICATE terminal callback for the same apply ID is deduplicated by
	//    the ClaimFinalization guard: no new history snapshot, no new failure,
	//    and the already-recorded provenance is returned.
	compMu.Lock()
	dupComp := lastComp
	compMu.Unlock()
	dupFin := finalizer.Finalize(dupComp)
	if dupFin.FinalizationError != "" {
		t.Fatalf("duplicate finalize surfaced a finalization error: %q", dupFin.FinalizationError)
	}
	if dupFin.HistorySnapshotID != fin.HistorySnapshotID {
		t.Errorf("duplicate finalize history id = %q, want recorded %q", dupFin.HistorySnapshotID, fin.HistorySnapshotID)
	}
	if got := rawSnapshotCount(t, histDir); got != 1 {
		t.Fatalf("raw history snapshots after duplicate finalize = %d, want 1 (exactly-once)", got)
	}
}

// scrapeLabeledMetric returns the value token of the Prometheus sample named
// `name` carrying exactly `labels`, or "" when the series is absent (a zero
// count that was never incremented). It reads the public exposition handler so a
// test can assert a labeled metric without touching the metrics package's
// private registry. Prometheus emits labels in sorted key order, so the expected
// series string is rebuilt with the label keys sorted.
func scrapeLabeledMetric(t *testing.T, m *observability.Metrics, name string, labels map[string]string) string {
	t.Helper()
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(name)
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%s=%q", k, labels[k])
	}
	b.WriteByte('}')
	prefix := b.String() + " "

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	for _, line := range strings.Split(rr.Body.String(), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, prefix) {
			fields := strings.Fields(line)
			return fields[len(fields)-1]
		}
	}
	return ""
}

// TestManagedApplyFinalizerHistoryDisposition proves the terminal finalizer
// emits the bounded jul_managed_apply_history_total{operation,result} counter
// with the correct snapshot disposition for each terminal outcome, through the
// real production Finalize path (WS06 §7.9):
//
//   - a committed apply with a non-empty prior configuration is "recorded";
//   - a committed apply with no prior configuration to snapshot is "skipped";
//   - a committed apply whose trusted history write fails is "failed".
//
// The metric is scraped from the public exposition handler; every label is a
// fixed low-cardinality value (operation + result) — never an apply ID, actor,
// or version.
func TestManagedApplyFinalizerHistoryDisposition(t *testing.T) {
	metrics := observability.NewMetrics()
	registry := admin.NewManagedApplyRegistry(0, 0)

	// A history-enabled admin server backs the recorded/skipped cases.
	goodAdmin := admin.New(config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:0", HistoryDir: filepath.Join(t.TempDir(), "history"), HistoryKeep: 50,
	}, nil, admin.Deps{ManagedApplies: registry})
	if goodAdmin == nil {
		t.Fatal("admin.New (good) returned nil")
	}
	finGood := &managedApplyFinalizer{registry: registry, admin: goodAdmin, metrics: metrics}

	// A finalizer whose configured history directory is actually a regular file
	// forces the trusted snapshot write to fail deterministically, exercising the
	// "failed" disposition without weakening any assertion.
	brokenPath := filepath.Join(t.TempDir(), "history-is-a-file")
	if err := os.WriteFile(brokenPath, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed broken history path: %v", err)
	}
	brokenAdmin := admin.New(config.AdminConfig{
		Enabled: true, Listen: "127.0.0.1:0", HistoryDir: brokenPath, HistoryKeep: 50,
	}, nil, admin.Deps{ManagedApplies: registry})
	if brokenAdmin == nil {
		t.Fatal("admin.New (broken) returned nil")
	}
	finBroken := &managedApplyFinalizer{registry: registry, admin: brokenAdmin, metrics: metrics}

	committed := func(id string) admin.ConfigApplyResult {
		return admin.ConfigApplyResult{
			ApplyID: id, OK: true, Mode: "hot",
			Reload: &server.ReloadResult{Outcome: server.ReloadAppliedLive},
		}
	}
	reqCtx := admin.ApplyRequestContext{Operation: admin.ApplyOperationConfigApply}

	// recorded: committed apply with a prior configuration to snapshot.
	if fin := finGood.Finalize(admin.ManagedApplyCompletion{
		Context: reqCtx, Result: committed("rl_101"), PreviousRaw: []byte("listen = \":8080\"\n"),
	}); fin.HistorySnapshotID == "" || fin.HistoryError != "" {
		t.Fatalf("recorded case: id=%q err=%q, want a snapshot id and no error", fin.HistorySnapshotID, fin.HistoryError)
	}

	// skipped: committed apply with no prior configuration (nothing to record).
	if fin := finGood.Finalize(admin.ManagedApplyCompletion{
		Context: reqCtx, Result: committed("rl_102"), PreviousRaw: nil,
	}); fin.HistorySnapshotID != "" || fin.HistoryError != "" {
		t.Fatalf("skipped case: id=%q err=%q, want neither a snapshot id nor an error", fin.HistorySnapshotID, fin.HistoryError)
	}

	// failed: committed apply whose trusted history write fails.
	if fin := finBroken.Finalize(admin.ManagedApplyCompletion{
		Context: reqCtx, Result: committed("rl_103"), PreviousRaw: []byte("listen = \":8080\"\n"),
	}); fin.HistoryError == "" {
		t.Fatal("failed case: expected a history error when the snapshot write fails")
	}

	op := string(admin.ApplyOperationConfigApply)
	for _, tc := range []struct{ result, want string }{
		{"recorded", "1"},
		{"skipped", "1"},
		{"failed", "1"},
	} {
		got := scrapeLabeledMetric(t, metrics, "jul_managed_apply_history_total",
			map[string]string{"operation": op, "result": tc.result})
		if got != tc.want {
			t.Errorf("jul_managed_apply_history_total{operation=%q,result=%q} = %q, want %q", op, tc.result, got, tc.want)
		}
	}
}
