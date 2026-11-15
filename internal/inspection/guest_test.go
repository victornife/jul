// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func TestGuestStateNames(t *testing.T) {
	want := []string{"starting", "idle", "busy", "closing", "failed_retained"}
	got := GuestStateNames()
	if len(got) != len(want) {
		t.Fatalf("guest state names = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("guest state names = %v, want %v", got, want)
		}
	}
}

func TestCheckGuestCapacityMinimum(t *testing.T) {
	plugins := map[string]config.PluginConfig{
		"one": {MemoryLimit: config.Size(config.PluginMemoryPageSize)},
		"two": {MemoryLimit: config.Size(2 * config.PluginMemoryPageSize)},
	}
	limits := testLimits(Enforce, 1, 0, 100, 1)
	limits.MaxGuestCapacityBytes = 3 * config.PluginMemoryPageSize
	if warning, err := CheckGuestCapacityMinimum(limits, plugins); warning != nil || err != nil {
		t.Fatalf("exact minimum = warning %v, error %v", warning, err)
	}

	limits.MaxGuestCapacityBytes--
	limits.Mode = Observe
	if warning, err := CheckGuestCapacityMinimum(limits, plugins); warning == nil || err != nil {
		t.Fatalf("observe shortfall = warning %v, error %v", warning, err)
	}
	limits.Mode = Enforce
	if warning, err := CheckGuestCapacityMinimum(limits, plugins); warning != nil || err == nil {
		t.Fatalf("enforce shortfall = warning %v, error %v", warning, err)
	}

	limits.MaxGuestCapacityBytes = 0
	if warning, err := CheckGuestCapacityMinimum(limits, plugins); warning != nil || err != nil {
		t.Fatalf("unlimited capacity = warning %v, error %v", warning, err)
	}
	plugins["invalid"] = config.PluginConfig{MemoryLimit: -1}
	if _, err := CheckGuestCapacityMinimum(limits, plugins); err == nil {
		t.Fatal("invalid plugin memory limit was accepted")
	}
}

func TestGuestTicketResolveTeardown(t *testing.T) {
	var nilTicket *GuestTicket
	if err := nilTicket.ResolveTeardown(nil); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil ticket teardown = %v", err)
	}
	if err := nilTicket.Transition(GuestIdle); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil ticket transition = %v", err)
	}

	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	orphan := &GuestTicket{g: g, generation: 1, amount: 1, state: GuestStarting}
	if err := orphan.ResolveTeardown(nil); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("untracked ticket teardown = %v", err)
	}
	succeeded, err := g.ReserveGuestCapacity(1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := succeeded.ResolveTeardown(nil); err != nil {
		t.Fatal(err)
	}
	if err := succeeded.ResolveTeardown(nil); err != nil {
		t.Fatalf("duplicate confirmed teardown: %v", err)
	}

	failed, err := g.ReserveGuestCapacity(2, 20)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("runtime close failed")
	if err := failed.ResolveTeardown(closeErr); err != nil {
		t.Fatal(err)
	}
	if err := failed.ResolveTeardown(closeErr); err != nil {
		t.Fatalf("retained teardown retry: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 20 || got.Instances[GuestFailedRetained] != 1 {
		t.Fatalf("failed teardown debt = %+v", got)
	}
	if err := failed.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := failed.ResolveTeardown(nil); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 0 || got.Instances[GuestFailedRetained] != 0 {
		t.Fatalf("confirmed retry did not release capacity: %+v", got)
	}
}

func TestReserveGuestCapacityRejectsInvalidAndOverflow(t *testing.T) {
	var nilGovernor *Governor
	if _, err := nilGovernor.ReserveGuestCapacity(1, 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil governor reservation = %v", err)
	}

	var rejections atomic.Int64
	g := newTestGovernor(t, testLimits(Observe, 1, 0, 100, 1), Hooks{
		OnRejection: func(reason Outcome, mode Mode) {
			if reason == GuestCapacitySaturated && mode == Observe {
				rejections.Add(1)
			}
		},
	})
	for _, args := range [][2]uint64{{0, 1}, {1, 0}} {
		if _, err := g.ReserveGuestCapacity(args[0], args[1]); !IsOutcome(err, InvalidAccounting) {
			t.Fatalf("invalid reservation %v = %v", args, err)
		}
	}

	ticket, err := g.ReserveGuestCapacity(1, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := g.Snapshot()
	limits := snapshot.Limits
	limits.MaxGuestCapacityBytes = 1
	if err := g.PublishLimitsIfVersion(snapshot.LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveGuestCapacity(2, 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("overflowing observe reservation = %v", err)
	}
	if rejections.Load() != 1 {
		t.Fatalf("observe rejection hooks = %d, want 1", rejections.Load())
	}
	if err := ticket.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestClosed); err != nil {
		t.Fatal(err)
	}

	ticket, err = g.ReserveGuestCapacity(3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestState(255)); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("invalid guest transition = %v", err)
	}
	if err := ticket.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestClosed); err != nil {
		t.Fatal(err)
	}
}

func TestGuestCapacityObserveDebtAndLeaseIndependence(t *testing.T) {
	limits := testLimits(Observe, 1, 0, 100, 1)
	limits.MaxGuestCapacityBytes = 100
	var wouldReject atomic.Int64
	g := newTestGovernor(t, limits, Hooks{OnRejection: func(reason Outcome, mode Mode) {
		if reason == GuestCapacitySaturated && mode == Observe {
			wouldReject.Add(1)
		}
	}})
	lease := acquireTestLease(t, g)
	first, err := g.ReserveGuestCapacity(1, 80)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.ReserveGuestCapacity(2, 50)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 130 || got.Instances[GuestStarting] != 2 || wouldReject.Load() != 1 {
		t.Fatalf("observe guest capacity = %+v, would reject = %d", got, wouldReject.Load())
	}

	limits.Mode = Enforce
	if err := g.PublishLimitsIfVersion(g.Snapshot().LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	if !g.Snapshot().GuestCapacity.Draining {
		t.Fatal("observe debt did not enter enforce draining")
	}
	if _, err := g.ReserveGuestCapacity(3, 1); !IsOutcome(err, GuestCapacitySaturated) || !IsOverload(err) {
		t.Fatalf("over-capacity enforce admission = %v", err)
	}
	if err := first.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := first.Transition(GuestClosed); err != nil {
		t.Fatal(err)
	}
	third, err := g.ReserveGuestCapacity(3, 50)
	if err != nil {
		t.Fatalf("admission at exact limit: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 100 || got.Draining {
		t.Fatalf("capacity after debt drained = %+v", got)
	}
	for _, ticket := range []*GuestTicket{second, third} {
		if err := ticket.Transition(GuestClosing); err != nil {
			t.Fatal(err)
		}
		if err := ticket.Transition(GuestClosed); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuestCapacityRetainsFailedClose(t *testing.T) {
	limits := testLimits(Enforce, 1, 0, 100, 1)
	limits.MaxGuestCapacityBytes = 64
	g := newTestGovernor(t, limits, Hooks{})
	ticket, err := g.ReserveGuestCapacity(7, 64)
	if err != nil {
		t.Fatal(err)
	}
	if ticket.Generation() != 7 {
		t.Fatalf("ticket generation = %d", ticket.Generation())
	}
	for _, state := range []GuestState{GuestIdle, GuestBusy, GuestClosing, GuestFailedRetained} {
		if err := ticket.Transition(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := ticket.Transition(GuestClosed); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("closed failed instance without retry: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64 || got.Instances[GuestFailedRetained] != 1 {
		t.Fatalf("failed close did not retain capacity: %+v", got)
	}
	if _, err := g.ReserveGuestCapacity(8, 1); !IsOutcome(err, GuestCapacitySaturated) {
		t.Fatalf("admission with retained debt = %v", err)
	}
	if err := ticket.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestClosed); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestClosed); err != nil {
		t.Fatalf("duplicate close: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 0 || got.Instances[GuestFailedRetained] != 0 {
		t.Fatalf("capacity after confirmed close = %+v", got)
	}
}

func TestGuestCapacityConcurrentAdmission(t *testing.T) {
	limits := testLimits(Enforce, 1, 0, 100, 1)
	limits.MaxGuestCapacityBytes = 8
	g := newTestGovernor(t, limits, Hooks{})
	const attempts = 64
	tickets := make(chan *GuestTicket, attempts)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			ticket, err := g.ReserveGuestCapacity(1, 1)
			if err == nil {
				tickets <- ticket
			} else if !IsOutcome(err, GuestCapacitySaturated) {
				t.Errorf("reserve: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(tickets)
	snap := g.Snapshot()
	if got := snap.GuestCapacity; got.ReservedBytes != 8 || got.Instances[GuestStarting] != 8 ||
		snap.Rejections[RejectGuestCapacity][0] != attempts-8 {
		t.Fatalf("concurrent admission = %+v, refusals = %d", got, snap.Rejections[RejectGuestCapacity][0])
	}
	for ticket := range tickets {
		if err := ticket.Transition(GuestClosing); err != nil {
			t.Fatal(err)
		}
		if err := ticket.Transition(GuestClosed); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGuestCapacityWithoutLimitTracksWithoutAdmission(t *testing.T) {
	limits := testLimits(Observe, 1, 0, 100, 1)
	g := newTestGovernor(t, limits, Hooks{})
	first, err := g.ReserveGuestCapacity(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := g.ReserveGuestCapacity(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 2 || got.Draining {
		t.Fatalf("unlimited tracking = %+v", got)
	}
	for _, ticket := range []*GuestTicket{first, second} {
		if err := ticket.Transition(GuestClosing); err != nil {
			t.Fatal(err)
		}
		if err := ticket.Transition(GuestClosed); err != nil {
			t.Fatal(err)
		}
	}

	limits.MaxGuestCapacityBytes = math.MaxUint64
	if err := g.PublishLimitsIfVersion(g.Snapshot().LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	ticket, err := g.ReserveGuestCapacity(1, math.MaxUint64)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveGuestCapacity(1, 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("overflow admission = %v", err)
	}
	limits.Mode = Enforce
	if err := g.PublishLimitsIfVersion(g.Snapshot().LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ReserveGuestCapacity(1, 1); !IsOutcome(err, GuestCapacitySaturated) {
		t.Fatalf("enforce admission above full capacity = %v", err)
	}
	if err := ticket.Transition(GuestClosing); err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(GuestClosed); err != nil {
		t.Fatal(err)
	}
}

func TestPublishLimitsUsesVersionedCompareAndSwap(t *testing.T) {
	limits := testLimits(Observe, 1, 0, 100, 1)
	g := newTestGovernor(t, limits, Hooks{})
	initial := g.Snapshot()
	limits.MaxGuestCapacityBytes = 10
	if err := g.PublishLimitsIfVersion(initial.LimitsVersion, limits); err != nil {
		t.Fatal(err)
	}
	published := g.Snapshot()
	if published.LimitsVersion != initial.LimitsVersion+1 || published.Limits.MaxGuestCapacityBytes != 10 {
		t.Fatalf("published limits/version = %+v/%d", published.Limits, published.LimitsVersion)
	}
	if err := g.PublishLimitsIfVersion(initial.LimitsVersion, testLimits(Enforce, 2, 0, 100, 1)); !errors.Is(err, ErrLimitsVersionConflict) {
		t.Fatalf("stale publication = %v, want version conflict", err)
	}
	if got := g.Snapshot(); got.Limits != published.Limits || got.LimitsVersion != published.LimitsVersion {
		t.Fatalf("stale publication changed current state: %+v", got)
	}
	if err := g.PublishLimitsIfVersion(published.LimitsVersion, published.Limits); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().LimitsVersion; got != published.LimitsVersion {
		t.Fatalf("no-op publication changed version to %d", got)
	}
}
