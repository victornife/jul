// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/lifecycle"
	"jul/internal/router"
	"jul/internal/upstream"
)

func TestInspectionLimitsPublishWithReloadAndPreserveProcessGovernor(t *testing.T) {
	initialGOMAXPROCS := runtime.GOMAXPROCS(0)
	t.Cleanup(func() { runtime.GOMAXPROCS(initialGOMAXPROCS) })
	addr := freePort(t)
	initial := cfgWith(addr)
	initial.Global.WorkerThreads = "1"
	initial.Global.Inspection.MaxConcurrentEvaluations = 0
	initial.Global.Inspection.MaxBufferedBytes = sizePtr(32 << 20)
	initialLimits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(initialLimits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	lease, _, owner, err := governor.AcquireLease(context.Background())
	if err != nil || !owner {
		t.Fatalf("initial lease = (%v, %v, %v)", lease, owner, err)
	}
	if _, err := lease.Charge(inspection.BufferBytes, 4096); err != nil {
		t.Fatal(err)
	}

	var generation atomic.Uint64
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		id := generation.Add(1)
		return map[string]http.Handler{addr: http.NotFoundHandler()}, id,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, initial, lifecycle.ComputeFingerprint(initial), quietLogger(), factory, nil, nil, nil)
	srv.Inspection = governor

	invalid, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	invalid.Global.Inspection.Mode = "unknown"
	invalidCandidate, err := config.NewCandidate(invalid)
	if err != nil {
		t.Fatal(err)
	}
	failedPlan := srv.newReloadPlan(context.Background(), invalidCandidate.Raw, invalidCandidate, nil)
	if err := failedPlan.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := failedPlan.Lifecycle(); err != nil {
		t.Fatal(err)
	}
	if err := failedPlan.Prepare(); err == nil {
		t.Fatal("invalid inspection candidate unexpectedly prepared")
	}
	failedPlan.Abort()
	if got := governor.Snapshot(); got.Limits != initialLimits || got.Usage[inspection.BufferBytes] != 4096 {
		t.Fatalf("failed Prepare mutated live governor: %+v", got)
	}

	candidate, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	candidate.Global.WorkerThreads = "2"
	candidate.Global.Inspection.MaxConcurrentEvaluations = 0
	candidate.Global.Inspection.MaxBufferedBytes = sizePtr(64 << 20)
	candidate.Global.Inspection.MaxResponseHeaderBytes = sizePtr(128 << 10)
	resolved, err := config.NewCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	plan := srv.newReloadPlan(context.Background(), resolved.Raw, resolved, nil)
	if err := plan.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Lifecycle(); err != nil {
		t.Fatal(err)
	}
	if err := plan.AssessServingChange(); err != nil {
		t.Fatal(err)
	}
	gomaxprocsBeforePrepare := runtime.GOMAXPROCS(0)
	if err := plan.Prepare(); err != nil {
		t.Fatal(err)
	}
	if got := runtime.GOMAXPROCS(0); got != gomaxprocsBeforePrepare {
		t.Fatalf("Prepare changed GOMAXPROCS from %d to %d", gomaxprocsBeforePrepare, got)
	}
	if err := plan.StageListeners(); err != nil {
		t.Fatal(err)
	}
	if got := governor.Snapshot(); got.Limits != initialLimits || got.Usage[inspection.BufferBytes] != 4096 {
		t.Fatalf("Prepare changed the live governor before Publish: %+v", got)
	}
	if _, err := plan.Publish(); err != nil {
		t.Fatal(err)
	}
	plan.FinalizeRuntimeState()
	if got := governor.Snapshot(); got.Limits.ConfiguredEvaluationSlots != 0 ||
		got.Limits.EvaluationSlots != 2 || got.Limits.MaxBytes != 64<<20 ||
		got.Limits.MaxResponseHeaderBytes != 128<<10 ||
		got.Usage[inspection.BufferBytes] != 4096 || got.ActiveLeases != 1 {
		t.Fatalf("published limits or existing lease state = %+v", got)
	}
	if srv.Inspection != governor {
		t.Fatal("reload replaced the process-owned governor")
	}

	rollbackCandidate, err := config.NewCandidate(initial)
	if err != nil {
		t.Fatal(err)
	}
	rollback := srv.newReloadPlan(context.Background(), rollbackCandidate.Raw, rollbackCandidate, nil)
	if err := rollback.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Lifecycle(); err != nil {
		t.Fatal(err)
	}
	if err := rollback.AssessServingChange(); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := rollback.StageListeners(); err != nil {
		t.Fatal(err)
	}
	if _, err := rollback.Publish(); err != nil {
		t.Fatal(err)
	}
	rollback.FinalizeRuntimeState()
	if got := governor.Snapshot(); got.Limits != initialLimits || got.Usage[inspection.BufferBytes] != 4096 {
		t.Fatalf("rollback did not restore limits while preserving usage: %+v", got)
	}

	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := governor.Snapshot(); got.ActiveLeases != 0 || got.Usage != ([len(got.Usage)]uint64{}) {
		t.Fatalf("lease did not drain after reload: %+v", got)
	}
}

func TestInspectionLimitsPublisherUsesExpectedVersion(t *testing.T) {
	limits := inspection.Limits{
		Mode:                   inspection.Enforce,
		MaxBytes:               32 << 20,
		MaxResponseHeaderBytes: 64 << 10,
		EvaluationSlots:        1,
		BaseChargeBytes:        inspection.BaseChargeBytes,
	}

	governor, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	component := &inspectionLimitsComponent{
		governor: governor,
		limits:   limits,
		version:  governor.Snapshot().LimitsVersion,
	}
	if err := component.publish(); err != nil {
		t.Fatalf("publish current limits: %v", err)
	}

	newer := limits
	newer.MaxGuestCapacityBytes = 1
	if err := governor.PublishLimitsIfVersion(governor.Snapshot().LimitsVersion, newer); err != nil {
		t.Fatal(err)
	}
	stale := *component
	stale.limits.Mode = inspection.Observe
	if err := stale.publish(); !errors.Is(err, inspection.ErrLimitsVersionConflict) {
		t.Fatalf("stale publisher error = %v, want version conflict", err)
	}
	if got := governor.Limits(); got != newer {
		t.Fatalf("stale publisher changed limits to %+v", got)
	}
}

func TestFullReloadRejectsStaleInspectionVersionBeforeCommit(t *testing.T) {
	addr := freePort(t)
	rawInitial := cfgWith(addr)
	rawInitial.Global.Inspection.MaxBufferedBytes = sizePtr(32 << 20)
	initialCandidate, err := config.NewCandidate(rawInitial)
	if err != nil {
		t.Fatal(err)
	}
	initial := initialCandidate.Effective
	initialLimits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(initialLimits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	var commits, aborts atomic.Int64
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return map[string]http.Handler{addr: http.NotFoundHandler()}, 2,
			func() (upstream.SnapshotMap, func()) {
				commits.Add(1)
				return nil, nil
			}, func() { aborts.Add(1) }, nil
	}
	srv := New(initial, initialCandidate.Raw, lifecycle.ComputeFingerprint(initial), quietLogger(), factory, nil, nil, nil)
	srv.Inspection = governor
	srv.handlers.Store(srv.newGeneration(map[string]http.Handler{addr: http.NotFoundHandler()}, nil, 1))
	srv.publishRuntimeStateDirect(1)

	nextCfg, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	nextCfg.Global.WorkerThreads = "3"
	nextCfg.Global.Inspection.MaxBufferedBytes = sizePtr(64 << 20)
	next, err := config.NewCandidate(nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	plan := srv.newReloadPlan(context.Background(), next.Raw, next, nil)
	for _, phase := range []func() error{plan.Resolve, plan.Validate, plan.Lifecycle, plan.AssessServingChange, plan.Prepare, plan.StageListeners} {
		if err := phase(); err != nil {
			t.Fatal(err)
		}
	}
	concurrentLimits := governor.Limits()
	concurrentLimits.BodyTimeout++
	if err := governor.PublishLimitsIfVersion(governor.Snapshot().LimitsVersion, concurrentLimits); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Publish(); !errors.Is(err, inspection.ErrLimitsVersionConflict) {
		t.Fatalf("full candidate publication = %v, want version conflict", err)
	}
	plan.Abort()
	if commits.Load() != 0 || aborts.Load() != 1 {
		t.Fatalf("stale candidate commit/abort = %d/%d, want 0/1", commits.Load(), aborts.Load())
	}
	if got := srv.LiveSnapshot().Generation; got != 1 {
		t.Fatalf("stale candidate changed serving generation to %d", got)
	}
	if srv.cfg != initial {
		t.Fatal("stale candidate changed the serving config")
	}
	if got := governor.Limits(); got != concurrentLimits {
		t.Fatalf("stale candidate changed concurrent limits: %+v", got)
	}
}

func TestLimitsOnlyPublicationAvoidsHandlerRebuild(t *testing.T) {
	addr := freePort(t)
	rawInitial := cfgWith(addr)
	rawInitial.Global.Inspection.MaxBufferedBytes = sizePtr(32 << 20)
	rawInitial.Global.Inspection.MaxGuestCapacityBytes = sizePtr(64 << 20)
	initialCandidate, err := config.NewCandidate(rawInitial)
	if err != nil {
		t.Fatal(err)
	}
	initial := initialCandidate.Effective
	initialLimits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(initialLimits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	existingGuest, err := governor.ReserveGuestCapacity(42, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := existingGuest.Transition(inspection.GuestIdle); err != nil {
		t.Fatal(err)
	}
	if _, err := governor.ReserveGuestCapacity(43, 16<<20); !inspection.IsOutcome(err, inspection.GuestCapacitySaturated) {
		t.Fatalf("candidate warm-up under old cap = %v, want capacity refusal", err)
	}

	var factoryCalls atomic.Int64
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		factoryCalls.Add(1)
		_, err := governor.ReserveGuestCapacity(43, 16<<20)
		return nil, 0, nil, nil, err
	}
	srv := New(initial, initialCandidate.Raw, lifecycle.ComputeFingerprint(initial), quietLogger(), factory, nil, nil, nil)
	srv.Inspection = governor
	const servingGeneration = 42
	srv.handlers.Store(srv.newGeneration(map[string]http.Handler{addr: http.NotFoundHandler()}, nil, servingGeneration))
	srv.publishRuntimeStateDirect(servingGeneration)

	candidateCfg, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	candidateCfg.Global.Inspection.MaxGuestCapacityBytes = sizePtr(128 << 20)
	candidate, err := config.NewCandidate(candidateCfg)
	if err != nil {
		t.Fatal(err)
	}
	plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
	for _, phase := range []func() error{plan.Resolve, plan.Validate, plan.Lifecycle, plan.AssessServingChange} {
		if err := phase(); err != nil {
			t.Fatal(err)
		}
	}
	if !plan.inspectionLimitsOnly {
		t.Fatalf("assessment = %+v, want genuine limits-only publication", plan.ServingChange)
	}
	if err := plan.PublishInspectionLimitsOnly(); err != nil {
		t.Fatal(err)
	}
	if got := governor.Snapshot(); got.Limits.MaxGuestCapacityBytes != 128<<20 {
		t.Fatalf("published guest capacity = %d, want 128 MiB", got.Limits.MaxGuestCapacityBytes)
	}
	if got := srv.LiveSnapshot().Generation; got != servingGeneration {
		t.Fatalf("serving generation = %d, want unchanged %d", got, servingGeneration)
	}
	if got := factoryCalls.Load(); got != 0 {
		t.Fatalf("handler factory called %d times for limits-only publication", got)
	}
	if srv.LiveSnapshot().EffectiveVersion != srv.versions.Config(candidate.Effective) {
		t.Fatal("limits-only publication did not update the effective runtime snapshot")
	}
	newGuest, err := governor.ReserveGuestCapacity(43, 16<<20)
	if err != nil {
		t.Fatalf("candidate instance still refused after cap increase: %v", err)
	}
	if err := newGuest.Transition(inspection.GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := newGuest.Transition(inspection.GuestClosed); err != nil {
		t.Fatal(err)
	}

	staleCfg, err := srv.cfg.Clone()
	if err != nil {
		t.Fatal(err)
	}
	staleCfg.Global.Inspection.MaxGuestCapacityBytes = sizePtr(256 << 20)
	staleCandidate, err := config.NewCandidate(staleCfg)
	if err != nil {
		t.Fatal(err)
	}
	stalePlan := srv.newReloadPlan(context.Background(), staleCandidate.Raw, staleCandidate, nil)
	for _, phase := range []func() error{stalePlan.Resolve, stalePlan.Validate, stalePlan.Lifecycle, stalePlan.AssessServingChange} {
		if err := phase(); err != nil {
			t.Fatal(err)
		}
	}
	concurrentLimits := governor.Limits()
	concurrentLimits.MaxResponseHeaderBytes *= 2
	if err := governor.PublishLimitsIfVersion(governor.Snapshot().LimitsVersion, concurrentLimits); err != nil {
		t.Fatal(err)
	}
	if err := stalePlan.PublishInspectionLimitsOnly(); !errors.Is(err, inspection.ErrLimitsVersionConflict) {
		t.Fatalf("stale limits-only publication = %v, want version conflict", err)
	}
	stalePlan.Abort()
	if got := srv.LiveSnapshot().EffectiveVersion; got != srv.versions.Config(candidate.Effective) {
		t.Fatalf("stale publication changed effective config version to %s", got)
	}
	if got := governor.Limits().MaxGuestCapacityBytes; got != 128<<20 {
		t.Fatalf("stale publication replaced guest capacity with %d", got)
	}

	if err := existingGuest.Transition(inspection.GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := existingGuest.Transition(inspection.GuestClosed); err != nil {
		t.Fatal(err)
	}
}

func TestGuestCapacityMinimumValidatesReloadsBeforePublication(t *testing.T) {
	addr := freePort(t)
	initial := cfgWith(addr)
	initial.Global.Inspection.MaxGuestCapacityBytes = sizePtr(64 << 20)
	initial.Plugins = map[string]config.PluginConfig{
		"first":  {Inline: "AGFzbQEAAAA="},
		"second": {Inline: "AGFzbQEAAAA=", MemoryLimit: config.Size(32 << 20)},
	}
	initialCandidate, err := config.NewCandidate(initial)
	if err != nil {
		t.Fatal(err)
	}
	initial = initialCandidate.Effective
	initialLimits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(initialLimits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	var factoryCalls atomic.Int64
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		factoryCalls.Add(1)
		return nil, 0, nil, nil, errors.New("unexpected candidate warm-up")
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	srv := New(initial, initialCandidate.Raw, lifecycle.ComputeFingerprint(initial), logger, factory, nil, nil, nil)
	srv.Inspection = governor
	const generation = 42
	srv.handlers.Store(srv.newGeneration(map[string]http.Handler{addr: http.NotFoundHandler()}, nil, generation))
	srv.publishRuntimeStateDirect(generation)

	newPlan := func(cfg *config.Config) *ReloadPlan {
		t.Helper()
		candidate, err := config.NewCandidate(cfg)
		if err != nil {
			t.Fatal(err)
		}
		plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
		for _, phase := range []func() error{plan.Resolve, plan.Validate, plan.Lifecycle, plan.AssessServingChange} {
			if err := phase(); err != nil {
				t.Fatal(err)
			}
		}
		return plan
	}
	lower, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	lower.Global.Inspection.MaxGuestCapacityBytes = sizePtr(47 << 20)
	rejected := newPlan(lower)
	if !rejected.inspectionLimitsOnly {
		t.Fatalf("assessment = %+v, want limits-only candidate", rejected.ServingChange)
	}
	if err := rejected.PublishInspectionLimitsOnly(); err == nil || !strings.Contains(err.Error(), "guest") {
		t.Fatalf("limits-only publication = %v, want eager guest-capacity error", err)
	}
	rejected.Abort()
	if got := governor.Limits(); got != initialLimits || srv.LiveSnapshot().EffectiveConfig != initial {
		t.Fatal("rejected limits-only publication changed the live policy")
	}

	lower.Global.Inspection.Mode = "observe"
	observed := newPlan(lower)
	if err := observed.PublishInspectionLimitsOnly(); err != nil {
		t.Fatalf("observe publication: %v", err)
	}
	if !strings.Contains(logs.String(), "one eager instance of each plugin") {
		t.Fatalf("observe publication did not warn about guest minimum: %s", logs.String())
	}
	if got := governor.Limits(); got.Mode != inspection.Observe || got.MaxGuestCapacityBytes != 47<<20 {
		t.Fatalf("observe limits = %+v", got)
	}

	full, err := lower.Clone()
	if err != nil {
		t.Fatal(err)
	}
	full.Global.Inspection.Mode = "enforce"
	first := full.Plugins["first"]
	first.MemoryLimit = config.Size(20 << 20)
	full.Plugins["first"] = first
	fullPlan := newPlan(full)
	if fullPlan.inspectionLimitsOnly {
		t.Fatal("plugin change was misclassified as limits-only")
	}
	if err := fullPlan.Prepare(); err == nil || !strings.Contains(err.Error(), "guest") {
		t.Fatalf("full candidate preparation = %v, want eager guest-capacity error", err)
	}
	fullPlan.Abort()
	if factoryCalls.Load() != 0 || governor.Limits().Mode != inspection.Observe {
		t.Fatalf("invalid candidate warmed guests or changed live limits: calls=%d limits=%+v",
			factoryCalls.Load(), governor.Limits())
	}
}

func TestLimitsOnlyReloadDoesNotHideRouteOrderChange(t *testing.T) {
	tests := []struct {
		name      string
		locations []config.LocationConfig
		change    func([]config.LocationConfig)
	}{
		{
			name: "overlapping regex order",
			locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "regex", Path: "^/"}, Return: http.StatusOK},
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusForbidden},
			},
			change: func(locations []config.LocationConfig) {
				locations[0], locations[1] = locations[1], locations[0]
			},
		},
		{
			name: "duplicate location identity",
			locations: []config.LocationConfig{
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusOK},
				{Match: config.MatchConfig{Type: "regex", Path: "^/private/"}, Return: http.StatusForbidden},
			},
			change: func(locations []config.LocationConfig) {
				locations[0].Return = http.StatusForbidden
			},
		},
	}
	for _, tt := range tests {
		for _, changeLimit := range []bool{false, true} {
			name := tt.name + "/without limit change"
			if changeLimit {
				name = tt.name + "/with limit change"
			}
			t.Run(name, func(t *testing.T) {
				addr := freePort(t)
				rawInitial := cfgWith(addr)
				rawInitial.Global.Inspection.MaxBufferedBytes = sizePtr(32 << 20)
				rawInitial.Servers[0].Locations = tt.locations
				initial, err := config.NewCandidate(rawInitial)
				if err != nil {
					t.Fatal(err)
				}
				limits, err := inspection.LimitsFromConfig(initial.Effective, lifecycle.InitialGOMAXPROCS())
				if err != nil {
					t.Fatal(err)
				}
				governor, err := inspection.New(limits, inspection.Hooks{})
				if err != nil {
					t.Fatal(err)
				}
				var factoryCalls atomic.Int64
				factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
					r, err := router.New(cfg, nil, nil, nil, quietLogger())
					if err != nil {
						return nil, 0, nil, nil, err
					}
					generation := uint64(factoryCalls.Add(1))
					return map[string]http.Handler{addr: r.For(addr)}, generation,
						func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
				}
				source := &stubSource{cfg: initial.Raw, raw: []byte("persisted")}
				srv := New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), quietLogger(), factory, source, nil)
				srv.Inspection = governor
				ctx, cancel := context.WithCancel(context.Background())
				reloads := make(chan ReloadRequest, 1)
				done := make(chan error, 1)
				go func() { done <- srv.Run(ctx, reloads, initial.Redaction) }()
				t.Cleanup(func() {
					cancel()
					if err := <-done; err != nil {
						t.Errorf("Run returned error: %v", err)
					}
				})
				waitForInitialGeneration(t, srv)
				client := &http.Client{Timeout: 5 * time.Second}
				status := func() int {
					t.Helper()
					resp, err := client.Get("http://" + addr + "/private/data")
					if err != nil {
						t.Fatal(err)
					}
					defer resp.Body.Close()
					return resp.StatusCode
				}
				if got := status(); got != http.StatusOK {
					t.Fatalf("initial private route = %d, want 200", got)
				}
				nextCfg, err := initial.Effective.Clone()
				if err != nil {
					t.Fatal(err)
				}
				tt.change(nextCfg.Servers[0].Locations)
				if changeLimit {
					nextCfg.Global.Inspection.MaxBufferedBytes = sizePtr(64 << 20)
				}
				next, err := config.NewCandidate(nextCfg)
				if err != nil {
					t.Fatal(err)
				}
				resultCh := make(chan ReloadResult, 1)
				reloads <- ReloadRequest{
					ID:                 "limits-and-route-change",
					Source:             ReloadSourceAdmin,
					Candidate:          next,
					ExpectedGeneration: srv.LiveSnapshot().Generation,
					Result:             resultCh,
				}
				if result := <-resultCh; result.Outcome != ReloadAppliedLive || !result.Published {
					t.Fatalf("reload = %+v", result)
				}
				if got := status(); got != http.StatusForbidden {
					t.Fatalf("private route after published change = %d, want 403", got)
				}
				if got := factoryCalls.Load(); got != 2 {
					t.Fatalf("handler builds = %d, want initial and replacement", got)
				}
			})
		}
	}
}

func TestLimitsOnlyReloadPublishesCapIncreaseWithoutCandidateWarmup(t *testing.T) {
	addr := freePort(t)
	rawInitial := cfgWith(addr)
	rawInitial.Global.Inspection.MaxBufferedBytes = sizePtr(32 << 20)
	rawInitial.Global.Inspection.MaxGuestCapacityBytes = sizePtr(64 << 20)
	initialCandidate, err := config.NewCandidate(rawInitial)
	if err != nil {
		t.Fatal(err)
	}
	initial := initialCandidate.Effective
	initialLimits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(initialLimits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	existingGuest, err := governor.ReserveGuestCapacity(1, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := existingGuest.Transition(inspection.GuestIdle); err != nil {
		t.Fatal(err)
	}

	var factoryCalls atomic.Int64
	factory := func(_ context.Context, cfg *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		genID := uint64(factoryCalls.Add(1))
		if cfg.Global.Inspection.MaxGuestCapacityBytes != nil &&
			cfg.Global.Inspection.MaxGuestCapacityBytes.Bytes() > 64<<20 {
			_, err := governor.ReserveGuestCapacity(genID+1, 16<<20)
			if err != nil {
				return nil, 0, nil, nil, err
			}
		}
		return map[string]http.Handler{addr: http.NotFoundHandler()}, genID,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	source := &stubSource{cfg: initialCandidate.Raw, raw: []byte("persisted")}
	srv := New(initial, initialCandidate.Raw, lifecycle.ComputeFingerprint(initial), quietLogger(), factory, source, nil)
	srv.Inspection = governor
	ctx, cancel := context.WithCancel(context.Background())
	reloads := make(chan ReloadRequest, 1)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reloads, initialCandidate.Redaction) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run returned error: %v", err)
		}
	})
	waitForInitialGeneration(t, srv)
	live := srv.LiveSnapshot()
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("startup handler builds = %d, want 1", got)
	}

	nextCfg, err := initial.Clone()
	if err != nil {
		t.Fatal(err)
	}
	nextCfg.Global.Inspection.MaxGuestCapacityBytes = sizePtr(128 << 20)
	next, err := config.NewCandidate(nextCfg)
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan ReloadResult, 1)
	reloads <- ReloadRequest{
		ID:                 "limits-only-increase",
		Source:             ReloadSourceAdmin,
		Candidate:          next,
		ExpectedGeneration: live.Generation,
		Result:             resultCh,
	}
	result := <-resultCh
	if result.Outcome != ReloadAppliedLive || !result.Published {
		t.Fatalf("limits-only reload = %+v", result)
	}
	if got := factoryCalls.Load(); got != 1 {
		t.Fatalf("handler builds after limits-only publication = %d, want unchanged 1", got)
	}
	if got := srv.LiveSnapshot().Generation; got != live.Generation {
		t.Fatalf("generation changed from %d to %d", live.Generation, got)
	}
	if got := governor.Limits().MaxGuestCapacityBytes; got != 128<<20 {
		t.Fatalf("published guest capacity = %d, want 128 MiB", got)
	}
	newGuest, err := governor.ReserveGuestCapacity(live.Generation+1, 16<<20)
	if err != nil {
		t.Fatalf("candidate guest admission after publication = %v", err)
	}
	for _, ticket := range []*inspection.GuestTicket{newGuest, existingGuest} {
		if err := ticket.Transition(inspection.GuestClosing); err != nil {
			t.Fatal(err)
		}
		if err := ticket.Transition(inspection.GuestClosed); err != nil {
			t.Fatal(err)
		}
	}
}

func sizePtr(value int64) *config.Size {
	s := config.Size(value)
	return &s
}
