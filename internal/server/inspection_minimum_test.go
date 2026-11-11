// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/lifecycle"
	"jul/internal/upstream"
)

// The route-aware minimum published by the factory for a generation is
// attached to the limits installed at startup and refused at reload Prepare
// in enforce mode (ADR 0032 §7); observe installs it without refusing.
func TestRouteMinimumAppliesAtStartupAndReload(t *testing.T) {
	addr := freePort(t)
	initial := cfgWith(addr)
	initial.Global.Inspection.MaxBufferedBytes = sizePtr(4 << 20)
	limits, err := inspection.LimitsFromConfig(initial, lifecycle.InitialGOMAXPROCS())
	if err != nil {
		t.Fatal(err)
	}
	governor, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	var generation atomic.Uint64
	generation.Store(1) // generation 1 is the startup build below
	factory := func(_ context.Context, _ *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		id := generation.Add(1)
		return map[string]http.Handler{addr: http.NotFoundHandler()}, id,
			func() (upstream.SnapshotMap, func()) { return nil, nil }, func() {}, nil
	}
	srv := New(initial, initial, lifecycle.ComputeFingerprint(initial), quietLogger(), factory, nil, nil, nil)
	srv.Inspection = governor
	minimums := map[uint64]uint64{1: 3 << 20, 2: 8 << 20, 3: 8 << 20}
	srv.InspectionRequirement = func(genID uint64) (uint64, string, bool) {
		m, ok := minimums[genID]
		return m, "servers[0].locations[0]", ok
	}

	if err := srv.publishStartupInspectionLimits(1); err != nil {
		t.Fatalf("startup: %v", err)
	}
	if got := governor.Snapshot().Limits; got.MinimumBytes != 3<<20 || got.MinimumRoute != "servers[0].locations[0]" {
		t.Fatalf("startup limits = %+v", got)
	}
	if _, err := srv.withRouteMinimum(limits, 2); err == nil || !strings.Contains(err.Error(), "servers[0].locations[0]") {
		t.Fatalf("enforce minimum above budget accepted: %v", err)
	}

	prepare := func(mode string) error {
		t.Helper()
		next, err := initial.Clone()
		if err != nil {
			t.Fatal(err)
		}
		next.Global.Inspection.Mode = mode
		cand, err := config.NewCandidate(next)
		if err != nil {
			t.Fatal(err)
		}
		plan := srv.newReloadPlan(context.Background(), cand.Raw, cand, nil)
		defer plan.Abort()
		if err := plan.Resolve(); err != nil {
			t.Fatal(err)
		}
		if err := plan.Lifecycle(); err != nil {
			t.Fatal(err)
		}
		return plan.Prepare()
	}
	if err := prepare("enforce"); err == nil || !strings.Contains(err.Error(), "prepare inspection limits") {
		t.Fatalf("enforce reload Prepare err = %v", err)
	}
	if err := prepare("observe"); err != nil {
		t.Fatalf("observe reload Prepare: %v", err)
	}
	if got := governor.Snapshot().Limits; got.MinimumBytes != 3<<20 {
		t.Fatalf("unpublished reloads changed live limits: %+v", got)
	}
}
