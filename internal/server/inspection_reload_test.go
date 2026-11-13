// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/lifecycle"
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

func TestInspectionLimitsComponentCommitAndAbort(t *testing.T) {
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
	component := &inspectionLimitsComponent{governor: governor, limits: limits}
	if got := component.component(); got != ComponentInspectionLimits {
		t.Fatalf("component() = %q, want %q", got, ComponentInspectionLimits)
	}
	if retirement := component.commit(); retirement != nil {
		t.Fatalf("commit() returned retirement %v, want nil", retirement)
	}
	component.abort()

	invalid := *component
	invalid.limits.Mode = inspection.Mode("invalid")
	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("commit() did not panic when prepared limits became invalid")
			}
		}()
		invalid.commit()
	}()
}

func sizePtr(value int64) *config.Size {
	s := config.Size(value)
	return &s
}
