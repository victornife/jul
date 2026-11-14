// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testLimits(mode Mode, slots int, queueTimeout time.Duration, maxBytes, baseBytes uint64) Limits {
	return Limits{
		Mode:                      mode,
		MaxBytes:                  maxBytes,
		MaxResponseHeaderBytes:    1,
		ConfiguredEvaluationSlots: slots,
		EvaluationSlots:           slots,
		EvaluationQueueTimeout:    queueTimeout,
		BaseChargeBytes:           baseBytes,
	}
}

func newTestGovernor(t *testing.T, limits Limits, hooks Hooks) *Governor {
	t.Helper()
	g, err := New(limits, hooks)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func acquireTestLease(t *testing.T, g *Governor) *Lease {
	t.Helper()
	lease, _, owner, err := g.AcquireLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !owner {
		t.Fatal("expected a new lease owner")
	}
	return lease
}

func TestNewRejectsInvalidLimits(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Limits)
	}{
		{name: "invalid mode", change: func(l *Limits) { l.Mode = "invalid" }},
		{name: "zero byte limit", change: func(l *Limits) { l.MaxBytes = 0 }},
		{name: "zero response-header limit", change: func(l *Limits) { l.MaxResponseHeaderBytes = 0 }},
		{name: "negative configured slots", change: func(l *Limits) { l.ConfiguredEvaluationSlots = -1 }},
		{name: "too many configured slots", change: func(l *Limits) { l.ConfiguredEvaluationSlots = 1025 }},
		{name: "zero effective slots", change: func(l *Limits) { l.EvaluationSlots = 0 }},
		{name: "negative queue timeout", change: func(l *Limits) { l.EvaluationQueueTimeout = -1 }},
		{name: "queue timeout above maximum", change: func(l *Limits) { l.EvaluationQueueTimeout = 10*time.Second + 1 }},
		{name: "negative body timeout", change: func(l *Limits) { l.BodyTimeout = -1 }},
		{name: "zero base charge", change: func(l *Limits) { l.BaseChargeBytes = 0 }},
		{name: "R3 estimate overflow", change: func(l *Limits) { l.MaxResponseHeaderBytes = math.MaxUint64 }},
		{name: "below route minimum", change: func(l *Limits) { l.MinimumBytes = 101 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limits := testLimits(Enforce, 1, 0, 100, 10)
			tt.change(&limits)
			if _, err := New(limits, Hooks{}); !errors.Is(err, ErrInvalidLimits) {
				t.Fatalf("New() error = %v, want ErrInvalidLimits", err)
			}
		})
	}
}

func waitForWaiters(t *testing.T, g *Governor, count uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if g.Snapshot().WaitingEvaluations == count {
			return
		}
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("waiting evaluations did not reach %d: %+v", count, g.Snapshot())
}

func TestLeaseAccountingAndNestedOwnership(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 10), Hooks{})
	lease, ctx, owner, err := g.AcquireLease(context.Background())
	if err != nil || !owner {
		t.Fatalf("AcquireLease() = (%v, %v, %v)", lease, owner, err)
	}
	nested, nestedCtx, nestedOwner, err := g.AcquireLease(ctx)
	if err != nil || nested != lease || nestedOwner || nestedCtx != ctx {
		t.Fatalf("nested AcquireLease() = (%v, %v, %v)", nested, nestedOwner, err)
	}
	other := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 10), Hooks{})
	if _, _, _, err := other.AcquireLease(ctx); !errors.Is(err, ErrForeignLease) {
		t.Fatal("expected cross-governor context to be rejected")
	}

	buffer, err := lease.Charge(BufferBytes, 30)
	if err != nil {
		t.Fatal(err)
	}
	waf, err := lease.Charge(WAFStateBytes, 20)
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().Usage; got != [byteClassCount]uint64{10, 30, 20} {
		t.Fatalf("usage after charges = %v", got)
	}
	if err := buffer.Resize(15); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().Usage; got != [byteClassCount]uint64{10, 15, 20} {
		t.Fatalf("usage after resize = %v", got)
	}
	if err := waf.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := g.Snapshot(); got.ActiveLeases != 0 || got.Usage != [byteClassCount]uint64{} {
		t.Fatalf("lease did not release its charges: %+v", got)
	}
	if _, err := lease.Charge(BufferBytes, 1); !IsOutcome(err, LeaseClosed) {
		t.Fatalf("Charge on closed lease = %v, want LeaseClosed", err)
	}
}

func TestLeaseRetainsChargesUntilEvaluationFinishes(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 10), Hooks{})
	lease := acquireTestLease(t, g)
	if _, err := lease.Charge(BufferBytes, 20); err != nil {
		t.Fatal(err)
	}
	evaluation, outcome, err := g.AcquireEvaluation(context.Background(), lease)
	if err != nil || outcome != Acquired {
		t.Fatalf("AcquireEvaluation() = (%v, %v, %v)", evaluation, outcome, err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot := g.Snapshot()
	if snapshot.ActiveLeases != 0 || snapshot.ActiveEvaluations != 1 || snapshot.Usage[BufferBytes] != 20 {
		t.Fatalf("close released work still in progress: %+v", snapshot)
	}
	if err := evaluation.MarkAbandoned(); err != nil {
		t.Fatal(err)
	}
	if err := evaluation.Release(); err != nil {
		t.Fatal(err)
	}
	snapshot = g.Snapshot()
	if snapshot.ActiveEvaluations != 0 || snapshot.AbandonedEvaluations != 0 || snapshot.Usage != [byteClassCount]uint64{} {
		t.Fatalf("evaluation completion did not release ownership: %+v", snapshot)
	}
}

func TestEnforceRejectsBytesAndObserveOnlyRecordsWouldReject(t *testing.T) {
	var mu sync.Mutex
	var rejections []Outcome
	hooks := Hooks{OnRejection: func(reason Outcome, mode Mode) {
		if mode != Observe {
			t.Errorf("rejection hook mode = %s", mode)
		}
		mu.Lock()
		rejections = append(rejections, reason)
		mu.Unlock()
	}}
	g := newTestGovernor(t, testLimits(Observe, 1, 0, 20, 10), hooks)
	leases := []*Lease{acquireTestLease(t, g), acquireTestLease(t, g)}
	for _, lease := range leases {
		if _, err := lease.Charge(BufferBytes, 20); err != nil {
			t.Fatalf("observe charge: %v", err)
		}
	}
	evaluations := make([]*Evaluation, 0, 2)
	for i := 0; i < 2; i++ {
		evaluation, outcome, err := g.AcquireEvaluation(context.Background(), leases[0])
		if err != nil || outcome == Unslotted {
			t.Fatalf("observe AcquireEvaluation() = (%v, %v, %v)", evaluation, outcome, err)
		}
		evaluations = append(evaluations, evaluation)
	}
	snapshot := g.Snapshot()
	if snapshot.ActiveEvaluations != 2 || snapshot.Rejections[0][1] != 2 ||
		snapshot.Rejections[2][1] != 1 || !snapshot.Draining {
		t.Fatalf("observe mode did not record would-reject: %+v", snapshot)
	}
	for _, evaluation := range evaluations {
		if err := evaluation.Release(); err != nil {
			t.Fatal(err)
		}
	}
	for _, lease := range leases {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(rejections) != 3 || rejections[0] != ByteSaturated ||
		rejections[1] != ByteSaturated || rejections[2] != EvaluationSaturated {
		t.Fatalf("rejection hooks = %v", rejections)
	}
}

func TestEvaluationQueueIsFIFOAndHonorsCancellation(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, time.Second, 100, 1), Hooks{})
	leases := []*Lease{acquireTestLease(t, g), acquireTestLease(t, g), acquireTestLease(t, g)}
	active, _, err := g.TryEvaluation(context.Background(), leases[0])
	if err != nil {
		t.Fatal(err)
	}

	type result struct {
		token   *Evaluation
		outcome Outcome
		err     error
	}
	first := make(chan result, 1)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	go func() {
		token, outcome, err := g.AcquireEvaluation(firstCtx, leases[1])
		first <- result{token, outcome, err}
	}()
	waitForWaiters(t, g, 1)

	second := make(chan result, 1)
	go func() {
		token, outcome, err := g.AcquireEvaluation(context.Background(), leases[2])
		second <- result{token, outcome, err}
	}()
	waitForWaiters(t, g, 2)
	cancelFirst()
	if got := <-first; got.outcome != Cancelled || !IsOutcome(got.err, Cancelled) {
		t.Fatalf("cancelled waiter = %+v", got)
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	got := <-second
	if got.err != nil || got.outcome != Acquired || got.token == nil {
		t.Fatalf("next FIFO waiter = %+v", got)
	}
	if err := got.token.Release(); err != nil {
		t.Fatal(err)
	}
	for _, lease := range leases {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestClosingLeaseCancelsQueuedEvaluation(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, time.Second, 100, 1), Hooks{})
	activeLease := acquireTestLease(t, g)
	waitingLease := acquireTestLease(t, g)
	active, _, err := g.TryEvaluation(context.Background(), activeLease)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan Outcome, 1)
	errs := make(chan error, 1)
	go func() {
		_, outcome, err := g.AcquireEvaluation(context.Background(), waitingLease)
		result <- outcome
		errs <- err
	}()
	waitForWaiters(t, g, 1)
	if err := waitingLease.Close(); err != nil {
		t.Fatal(err)
	}
	if outcome, err := <-result, <-errs; outcome != LeaseClosed || !IsOutcome(err, LeaseClosed) {
		t.Fatalf("closed-lease waiter = (%s, %v)", outcome, err)
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	if err := activeLease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestQueueTimeoutAndLimitDecrease(t *testing.T) {
	var rejectionHooks int
	g := newTestGovernor(t, testLimits(Enforce, 2, 20*time.Millisecond, 100, 1), Hooks{
		OnRejection: func(reason Outcome, mode Mode) {
			if reason != EvaluationSaturated || mode != Enforce {
				t.Errorf("timeout rejection = %s/%s", reason, mode)
			}
			rejectionHooks++
		},
	})
	leases := []*Lease{acquireTestLease(t, g), acquireTestLease(t, g), acquireTestLease(t, g)}
	active := make([]*Evaluation, 2)
	for i := range active {
		var err error
		active[i], _, err = g.TryEvaluation(context.Background(), leases[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, outcome, err := g.AcquireEvaluation(ctx, leases[2]); outcome != WaitExpired || !IsOutcome(err, WaitExpired) {
		t.Fatalf("timed out waiter = (%s, %v)", outcome, err)
	}
	if snapshot := g.Snapshot(); snapshot.Rejections[2][0] != 1 || rejectionHooks != 1 {
		t.Fatalf("timed-out evaluation was not counted exactly once: snapshot=%+v hooks=%d", snapshot, rejectionHooks)
	}
	if err := g.PublishLimits(testLimits(Enforce, 1, 0, 100, 1)); err != nil {
		t.Fatal(err)
	}
	if !g.Snapshot().Draining {
		t.Fatal("expected active evaluations above the reduced limit to drain")
	}
	if _, outcome, err := g.TryEvaluation(context.Background(), leases[2]); outcome != EvaluationSaturated || !IsOutcome(err, EvaluationSaturated) {
		t.Fatalf("acquisition above reduced limit = (%s, %v)", outcome, err)
	}
	if err := active[0].Release(); err != nil {
		t.Fatal(err)
	}
	if g.Snapshot().Draining {
		t.Fatal("expected governor to leave draining state at the new limit")
	}
	if err := active[1].Release(); err != nil {
		t.Fatal(err)
	}
	for _, lease := range leases {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRecordRejectionTracksReasonAndMode(t *testing.T) {
	type rejection struct {
		reason Outcome
		index  int
	}
	rejections := []rejection{
		{reason: ByteSaturated, index: RejectBytes},
		{reason: WAFStateSaturated, index: RejectWAFState},
		{reason: EvaluationSaturated, index: RejectEvaluation},
		{reason: AuditSaturated, index: RejectAudit},
	}
	modes := []Mode{Enforce, Observe}
	var calls int
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 10), Hooks{
		OnRejection: func(Outcome, Mode) { calls++ },
	})
	for _, mode := range modes {
		for _, rejection := range rejections {
			if err := g.RecordRejection(rejection.reason, mode); err != nil {
				t.Fatalf("RecordRejection(%s, %s): %v", rejection.reason, mode, err)
			}
		}
	}
	snapshot := g.Snapshot()
	for _, rejection := range rejections {
		for modeIndex := range modes {
			if got := snapshot.Rejections[rejection.index][modeIndex]; got != 1 {
				t.Errorf("rejection count for %s/mode %d = %d, want 1", rejection.reason, modeIndex, got)
			}
		}
	}
	if calls != len(rejections)*len(modes) {
		t.Fatalf("rejection hooks = %d, want %d", calls, len(rejections)*len(modes))
	}
}

func TestRecordRejectionRejectsInvalidInputs(t *testing.T) {
	var nilGovernor *Governor
	if err := nilGovernor.RecordRejection(ByteSaturated, Enforce); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil-governor error = %v, want invalid accounting", err)
	}
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 10), Hooks{})
	for _, tc := range []struct {
		reason Outcome
		mode   Mode
	}{
		{reason: ByteSaturated, mode: Mode("invalid")},
		{reason: Cancelled, mode: Enforce},
	} {
		if err := g.RecordRejection(tc.reason, tc.mode); !IsOutcome(err, InvalidAccounting) {
			t.Errorf("RecordRejection(%s, %s) = %v, want invalid accounting", tc.reason, tc.mode, err)
		}
	}
}

func TestQueueCapacityAndZeroWait(t *testing.T) {
	var abandoned atomic.Uint64
	g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Second, 100, 1), Hooks{
		OnQueueAbandon: func() { abandoned.Add(1) },
	})
	leases := make([]*Lease, 18)
	for i := range leases {
		leases[i] = acquireTestLease(t, g)
	}
	active, _, err := g.TryEvaluation(context.Background(), leases[0])
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan Outcome, 16)
	errs := make(chan error, 16)
	for i := 1; i <= 16; i++ {
		lease := leases[i]
		go func() {
			_, outcome, err := g.AcquireEvaluation(ctx, lease)
			results <- outcome
			errs <- err
		}()
	}
	waitForWaiters(t, g, 16)
	if _, outcome, err := g.AcquireEvaluation(context.Background(), leases[17]); outcome != QueueExhausted || !IsOutcome(err, QueueExhausted) {
		t.Fatalf("17th waiter = (%s, %v)", outcome, err)
	}
	cancel()
	for i := 0; i < 16; i++ {
		if outcome, err := <-results, <-errs; outcome != Cancelled || !IsOutcome(err, Cancelled) {
			t.Fatalf("abandoned waiter = (%s, %v)", outcome, err)
		}
	}
	if abandoned.Load() != 16 || g.Snapshot().WaitingEvaluations != 0 {
		t.Fatalf("abandonment cleanup count=%d snapshot=%+v", abandoned.Load(), g.Snapshot())
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	active, _, err = g.TryEvaluation(context.Background(), leases[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := g.PublishLimits(testLimits(Enforce, 1, 0, 100, 1)); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := g.AcquireEvaluation(context.Background(), leases[17]); outcome != EvaluationSaturated || !IsOutcome(err, EvaluationSaturated) {
		t.Fatalf("zero-timeout acquisition = (%s, %v)", outcome, err)
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	for _, lease := range leases {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIncreaseWakesWaitersAndObserveEnforceTransition(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, time.Second, 60, 10), Hooks{})
	first := acquireTestLease(t, g)
	second := acquireTestLease(t, g)
	active, _, err := g.TryEvaluation(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *Evaluation, 1)
	go func() {
		token, _, _ := g.AcquireEvaluation(context.Background(), second)
		result <- token
	}()
	waitForWaiters(t, g, 1)
	if err := g.PublishLimits(testLimits(Enforce, 2, time.Second, 60, 10)); err != nil {
		t.Fatal(err)
	}
	waiting := <-result
	if waiting == nil || g.Snapshot().WaitingEvaluations != 0 {
		t.Fatalf("limit increase did not grant waiter: token=%v snapshot=%+v", waiting, g.Snapshot())
	}
	if err := waiting.Release(); err != nil {
		t.Fatal(err)
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}

	if err := g.PublishLimits(testLimits(Observe, 1, 0, 58, 10)); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Charge(BufferBytes, 50); err != nil {
		t.Fatal(err)
	}
	if !g.Snapshot().Draining {
		t.Fatal("observe mode should report over-budget usage")
	}
	if err := g.PublishLimits(testLimits(Enforce, 1, 0, 58, 10)); err != nil {
		t.Fatal(err)
	}
	if !g.Snapshot().Draining {
		t.Fatal("enforce transition must preserve and report over-budget usage")
	}
	if _, err := first.Charge(BufferBytes, 1); !IsOutcome(err, ByteSaturated) {
		t.Fatalf("growth above lowered budget = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCanceledContextAndUnslottedLogging(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, outcome, err := g.AcquireEvaluation(ctx, lease); outcome != Cancelled || !IsOutcome(err, Cancelled) {
		t.Fatalf("pre-canceled acquisition = (%s, %v)", outcome, err)
	}
	active, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if token, outcome, err := g.AcquireLoggingEvaluation(context.Background(), lease, LoggingTry); token != nil || outcome != Unslotted || err != nil {
		t.Fatalf("phase-5 fallback = (%v, %s, %v)", token, outcome, err)
	}
	if g.Snapshot().UnslottedLogging != 1 {
		t.Fatalf("unslotted logging count = %d", g.Snapshot().UnslottedLogging)
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckedAccountingRejectsOverflowWithoutCorruptingState(t *testing.T) {
	belowRoute := testLimits(Enforce, 1, 0, 10, 11)
	belowRoute.MinimumBytes = 11
	if _, err := New(belowRoute, Hooks{}); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("invalid base budget error = %v", err)
	}
	g := newTestGovernor(t, testLimits(Observe, 1, 0, math.MaxUint64, 1), Hooks{})
	lease := acquireTestLease(t, g)
	if _, err := lease.Charge(BufferBytes, math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Charge(BufferBytes, 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("overflowing charge = %v", err)
	}
	if got := g.Snapshot().Usage[BufferBytes]; got != math.MaxUint64 {
		t.Fatalf("overflow changed usage to %d", got)
	}
	if !g.Snapshot().Draining {
		t.Fatal("overflowing aggregate usage must be reported as draining")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLimitsOutcomesAndInvalidAccountingPaths(t *testing.T) {
	for _, limits := range []Limits{
		{},
		{Mode: Enforce, MaxBytes: 1, ConfiguredEvaluationSlots: -1, EvaluationSlots: 1, BaseChargeBytes: 1},
		{Mode: Enforce, MaxBytes: 1, ConfiguredEvaluationSlots: 1025, EvaluationSlots: 1, BaseChargeBytes: 1},
		{Mode: Enforce, MaxBytes: 1, EvaluationSlots: 0, BaseChargeBytes: 1},
		{Mode: Enforce, MaxBytes: 1, EvaluationSlots: 1, EvaluationQueueTimeout: 11 * time.Second, BaseChargeBytes: 1},
		{Mode: Enforce, MaxBytes: 1, EvaluationSlots: 1, BodyTimeout: -time.Second, BaseChargeBytes: 1},
		{Mode: Enforce, MaxBytes: 1, EvaluationSlots: 1, BaseChargeBytes: 0},
	} {
		if _, err := New(limits, Hooks{}); !errors.Is(err, ErrInvalidLimits) {
			t.Errorf("New(%+v) error = %v", limits, err)
		}
	}
	if (ByteClass(255)).String() != "unknown" || BaseBytes.String() != "base" ||
		BufferBytes.String() != "buffer" || WAFStateBytes.String() != "waf_state" {
		t.Fatal("byte class names are not stable")
	}
	if (&AdmissionError{Outcome: ByteSaturated}).Error() != "inspection admission: bytes" {
		t.Fatal("admission error string is not descriptive")
	}
	if IsOutcome(errors.New("other error"), ByteSaturated) {
		t.Fatal("IsOutcome matched an unrelated error")
	}

	var rejected []Outcome
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 58, 10), Hooks{
		OnRejection: func(reason Outcome, mode Mode) {
			if mode != Enforce {
				t.Errorf("rejection mode = %s", mode)
			}
			rejected = append(rejected, reason)
		},
	})
	lease, ctx, _, err := g.AcquireLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if nested, _, owner, err := g.AcquireLease(ctx); err != nil || nested != lease || owner {
		t.Fatalf("nested lease = (%p, %v, %v)", nested, owner, err)
	}
	buffer, err := lease.Charge(BufferBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := buffer.Resize(2); err != nil {
		t.Fatal(err)
	}
	waf, err := lease.Charge(WAFStateBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	padding, err := lease.Charge(BufferBytes, 45)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.AcquireLease(context.Background()); !IsOutcome(err, ByteSaturated) {
		t.Fatalf("base saturation = %v", err)
	}
	if _, _, _, err := g.AcquireLease(ctx); err != nil {
		t.Fatalf("nested lease should remain reusable: %v", err)
	}
	if _, err := lease.Charge(BaseBytes, 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("base class charge = %v", err)
	}
	if _, err := lease.Charge(ByteClass(255), 1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("unknown class charge = %v", err)
	}
	if err := buffer.Resize(4); !IsOutcome(err, ByteSaturated) {
		t.Fatalf("buffer growth saturation = %v", err)
	}
	if err := waf.Resize(2); !IsOutcome(err, WAFStateSaturated) {
		t.Fatalf("WAF-state growth saturation = %v", err)
	}
	if err := padding.Release(); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Release(); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Release(); err != nil {
		t.Fatalf("repeated charge release = %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := g.AcquireLease(ctx); !IsOutcome(err, LeaseClosed) {
		t.Fatalf("closed nested lease = %v", err)
	}
	if err := (*Lease)(nil).Close(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil lease close = %v", err)
	}
	if err := (*Charge)(nil).Resize(1); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil charge resize = %v", err)
	}
	if err := (*Charge)(nil).Release(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil charge release = %v", err)
	}
	if len(rejected) != 3 || rejected[0] != ByteSaturated || rejected[1] != ByteSaturated || rejected[2] != WAFStateSaturated {
		t.Fatalf("rejection hook sequence = %v", rejected)
	}
}

func TestObserveBaseOverflowAndEvaluationOutcomeAccounting(t *testing.T) {
	var observedRejections []Outcome
	observeBelowBase := newTestGovernor(t, testLimits(Observe, 1, 0, 1, 10), Hooks{
		OnRejection: func(reason Outcome, mode Mode) {
			if mode != Observe {
				t.Errorf("observe rejection hook mode = %s", mode)
			}
			observedRejections = append(observedRejections, reason)
		},
	})
	overBudgetLease := acquireTestLease(t, observeBelowBase)
	buffer, err := overBudgetLease.Charge(BufferBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	waf, err := overBudgetLease.Charge(WAFStateBytes, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := waf.Resize(2); err != nil {
		t.Fatal(err)
	}
	if snapshot := observeBelowBase.Snapshot(); snapshot.Usage[BaseBytes] != 10 || !snapshot.Draining {
		t.Fatalf("observe mode did not retain and report a base charge above budget: %+v", snapshot)
	} else if snapshot.Rejections[0][1] != 2 || snapshot.Rejections[1][1] != 2 {
		t.Fatalf("observe mode did not count byte-class would-reject decisions: %+v", snapshot.Rejections)
	}
	if len(observedRejections) != 4 ||
		observedRejections[0] != ByteSaturated ||
		observedRejections[1] != ByteSaturated ||
		observedRejections[2] != WAFStateSaturated ||
		observedRejections[3] != WAFStateSaturated {
		t.Fatalf("observe rejection hooks = %v", observedRejections)
	}
	if err := buffer.Release(); err != nil {
		t.Fatal(err)
	}

	tooSmall := testLimits(Enforce, 1, 0, 1, 10)
	tooSmall.MinimumBytes, tooSmall.MinimumRoute = 58, "servers[0].locations[0]"
	if _, err := New(tooSmall, Hooks{}); err == nil ||
		!errors.Is(err, ErrInvalidLimits) ||
		!strings.Contains(err.Error(), "global.inspection.max_buffered_bytes is 1 B") ||
		!strings.Contains(err.Error(), "requires at least 58 B for route servers[0].locations[0]") {
		t.Fatalf("enforce mode accepted base charge above budget: %v", err)
	}
	if err := overBudgetLease.Close(); err != nil {
		t.Fatal(err)
	}

	g := newTestGovernor(t, testLimits(Observe, 1, 0, math.MaxUint64, math.MaxUint64), Hooks{})
	first := acquireTestLease(t, g)
	if _, _, _, err := g.AcquireLease(context.Background()); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("base counter overflow = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	var rejections []Outcome
	g = newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{
		OnRejection: func(reason Outcome, _ Mode) { rejections = append(rejections, reason) },
	})
	lease := acquireTestLease(t, g)
	active, outcome, err := g.TryEvaluation(context.Background(), lease)
	if err != nil || outcome != Acquired {
		t.Fatalf("initial evaluation = (%v, %s, %v)", active, outcome, err)
	}
	if token, outcome, err := g.TryEvaluation(context.Background(), lease); token != nil ||
		outcome != EvaluationSaturated || !IsOutcome(err, EvaluationSaturated) {
		t.Fatalf("saturated try = (%v, %s, %v)", token, outcome, err)
	}
	if err := active.MarkAbandoned(); err != nil {
		t.Fatal(err)
	}
	if err := active.MarkAbandoned(); err != nil || g.Snapshot().AbandonedEvaluations != 1 {
		t.Fatalf("idempotent abandonment = %v snapshot=%+v", err, g.Snapshot())
	}
	if err := active.Release(); err != nil {
		t.Fatal(err)
	}
	if err := active.MarkAbandoned(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("abandon released evaluation = %v", err)
	}
	if err := (*Evaluation)(nil).MarkAbandoned(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil evaluation abandonment = %v", err)
	}
	if len(rejections) != 1 || rejections[0] != EvaluationSaturated {
		t.Fatalf("evaluation rejection hooks = %v", rejections)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := (*Evaluation)(nil).Release(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil evaluation release = %v", err)
	}
}

func TestObserveMakesDeterministicWouldRejectDecisionWithoutWaiting(t *testing.T) {
	newObserved := func() (*Governor, *Lease, *Evaluation) {
		g := newTestGovernor(t, testLimits(Observe, 1, time.Second, 1024, 10), Hooks{})
		lease := acquireTestLease(t, g)
		active, outcome, err := g.TryEvaluation(context.Background(), lease)
		if err != nil || outcome != Acquired {
			t.Fatalf("seed evaluation = %v, %v", outcome, err)
		}
		return g, lease, active
	}

	g1, lease1, active1 := newObserved()
	g2, lease2, active2 := newObserved()
	second1, outcome1, err1 := g1.AcquireEvaluation(context.Background(), lease1)
	second2, outcome2, err2 := g2.AcquireEvaluation(context.Background(), lease2)
	if err1 != nil || err2 != nil || outcome1 != WouldReject || outcome2 != outcome1 {
		t.Fatalf("identical observe states decided %v/%v with errors %v/%v", outcome1, outcome2, err1, err2)
	}
	s1, s2 := g1.Snapshot(), g2.Snapshot()
	if s1.ActiveEvaluations != 2 || s2.ActiveEvaluations != s1.ActiveEvaluations ||
		s1.WaitingEvaluations != 0 || s2.WaitingEvaluations != 0 ||
		s1.Rejections != s2.Rejections || s1.Rejections[2][1] != 1 || s2.Rejections[2][1] != 1 {
		t.Fatalf("observe accounting differs or waited: %+v / %+v", s1, s2)
	}
	for _, token := range []*Evaluation{active1, active2, second1, second2} {
		if err := token.Release(); err != nil {
			t.Fatal(err)
		}
	}
	for _, lease := range []*Lease{lease1, lease2} {
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoggingFallbackAndEvaluationInvalidInputs(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	if _, outcome, err := g.AcquireLoggingEvaluation(context.Background(), nil, LoggingWait); outcome != InvalidAccounting || !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("invalid logging lease = (%s, %v)", outcome, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Phase 5 survives client cancellation: a free slot is still taken.
	token, outcome, err := g.AcquireLoggingEvaluation(ctx, lease, LoggingWait)
	if token == nil || outcome != Acquired || err != nil {
		t.Fatalf("cancelled logging evaluation = (%v, %s, %v)", token, outcome, err)
	}
	if err := token.Release(); err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := g.AcquireEvaluation(context.Background(), nil); outcome != InvalidAccounting || !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("nil evaluation lease = (%s, %v)", outcome, err)
	}
	other := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	otherLease := acquireTestLease(t, other)
	if _, outcome, err := g.AcquireEvaluation(context.Background(), otherLease); outcome != InvalidAccounting || !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("foreign evaluation lease = (%s, %v)", outcome, err)
	}
	if err := otherLease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWaiterGrantAndAbandonmentAccountingEdges(t *testing.T) {
	g := newTestGovernor(t, testLimits(Observe, 1, time.Second, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	g.mu.Lock()
	g.active = math.MaxUint64
	g.mu.Unlock()
	if _, outcome, err := g.AcquireEvaluation(context.Background(), lease); outcome != InvalidAccounting || !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("active evaluation overflow = (%s, %v)", outcome, err)
	}
	g.mu.Lock()
	g.active = 0
	lease.evaluations = math.MaxUint64
	g.mu.Unlock()
	if _, outcome, err := g.AcquireEvaluation(context.Background(), lease); outcome != InvalidAccounting || !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("lease evaluation overflow = (%s, %v)", outcome, err)
	}
	closedLease := &Lease{g: g, closed: true}
	ready := make(chan struct{})
	g.mu.Lock()
	lease.evaluations = 0
	g.waiters = []*evaluationWaiter{{ready: ready, lease: closedLease}}
	g.grantWaitersLocked()
	if len(g.waiters) != 0 {
		g.mu.Unlock()
		t.Fatal("closed lease waiter was not removed")
	}
	g.mu.Unlock()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("closed lease waiter was not awakened")
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDefensiveAccountingAndWaiterRaceBranches(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	before := g.Snapshot()
	if err := g.PublishLimits(Limits{}); !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("invalid PublishLimits() = %v", err)
	}
	if err := g.PublishLimits(before.Limits); err != nil || g.Snapshot() != before {
		t.Fatalf("no-op PublishLimits() = %v snapshot=%+v", err, g.Snapshot())
	}

	lease := acquireTestLease(t, g)
	buffer, err := lease.Charge(BufferBytes, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := buffer.Resize(5); err != nil {
		t.Fatal(err)
	}
	if err := buffer.Resize(5); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	released := lease.releaseChargesLocked()
	g.mu.Unlock()
	if !released {
		t.Fatal("released lease charge cleanup was not idempotent")
	}
	if err := buffer.Resize(1); !IsOutcome(err, LeaseClosed) {
		t.Fatalf("resize after close = %v", err)
	}
	if err := buffer.Release(); err != nil {
		t.Fatalf("release after lease close = %v", err)
	}

	badClose := acquireTestLease(t, g)
	g.mu.Lock()
	g.leases = 0
	g.mu.Unlock()
	if err := badClose.Close(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("close with corrupt lease counter = %v", err)
	}
	g.mu.Lock()
	g.leases = 1
	g.mu.Unlock()
	if err := badClose.Close(); err != nil {
		t.Fatal(err)
	}

	g = newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	lease = acquireTestLease(t, g)
	evaluation, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.usage[BaseBytes] = 0
	g.mu.Unlock()
	if err := evaluation.Release(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("release with corrupt retained charges = %v", err)
	}
	if g.Snapshot().ActiveEvaluations != 1 {
		t.Fatal("failed evaluation release changed active accounting")
	}
	g.mu.Lock()
	g.usage[BaseBytes] = 1
	g.mu.Unlock()
	if err := evaluation.Release(); err != nil {
		t.Fatal(err)
	}
	if err := evaluation.Release(); err != nil {
		t.Fatalf("idempotent evaluation release = %v", err)
	}
	if err := (&Evaluation{g: g}).Release(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("invalid evaluation accounting = %v", err)
	}

	g = newTestGovernor(t, testLimits(Observe, 1, 0, 100, 1), Hooks{})
	lease = acquireTestLease(t, g)
	g.mu.Lock()
	g.abandoned = math.MaxUint64
	g.mu.Unlock()
	if err := (&Evaluation{g: g, lease: lease}).MarkAbandoned(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("abandoned count overflow = %v", err)
	}
	g.mu.Lock()
	g.abandoned = 0
	g.mu.Unlock()

	ready := make(chan struct{})
	w := &evaluationWaiter{ready: ready, outcome: LeaseClosed}
	if _, outcome, err := g.abandonWaiter(w, Cancelled, false); outcome != LeaseClosed || !IsOutcome(err, LeaseClosed) {
		t.Fatalf("already-completed waiter = (%s, %v)", outcome, err)
	}
	close(ready)

	grantedLease := acquireTestLease(t, g)
	g.mu.Lock()
	g.active = 1
	grantedLease.evaluations = 1
	granted := &Evaluation{g: g, lease: grantedLease}
	w = &evaluationWaiter{ready: make(chan struct{}), granted: granted}
	g.mu.Unlock()
	if token, outcome, err := g.abandonWaiter(w, WaitExpired, false); token != nil || outcome != WaitExpired || !IsOutcome(err, WaitExpired) {
		t.Fatalf("grant/timeout race resolution = (%v, %s, %v)", token, outcome, err)
	}
	if g.Snapshot().ActiveEvaluations != 0 {
		t.Fatal("losing timeout race leaked a granted evaluation slot")
	}
	if err := grantedLease.Close(); err != nil {
		t.Fatal(err)
	}

	waiterLease := acquireTestLease(t, g)
	g.mu.Lock()
	g.active = math.MaxUint64
	ready = make(chan struct{})
	g.waiters = []*evaluationWaiter{{ready: ready, lease: waiterLease}}
	g.grantWaitersLocked()
	if len(g.waiters) != 0 {
		g.mu.Unlock()
		t.Fatal("overflowing active counter left a waiter queued")
	}
	g.active = 0
	waiterLease.evaluations = math.MaxUint64
	ready = make(chan struct{})
	g.waiters = []*evaluationWaiter{{ready: ready, lease: waiterLease}}
	g.grantWaitersLocked()
	g.mu.Unlock()
	select {
	case <-ready:
	default:
		t.Fatal("overflowing lease evaluation count did not wake waiter")
	}
	if err := waiterLease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLeaseCloseDoesNotPartiallyReleaseCorruptedAccounting(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	if _, err := lease.Charge(BufferBytes, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Charge(WAFStateBytes, 10); err != nil {
		t.Fatal(err)
	}
	g.mu.Lock()
	g.usage[WAFStateBytes] = 0
	g.mu.Unlock()
	before := g.Snapshot()
	if err := lease.Close(); !IsOutcome(err, InvalidAccounting) {
		t.Fatalf("Close() = %v, want InvalidAccounting", err)
	}
	after := g.Snapshot()
	if after.Usage != before.Usage || after.ActiveLeases != before.ActiveLeases {
		t.Fatalf("failed close partially mutated accounting: before=%+v after=%+v", before, after)
	}
}

func TestLoggingNeverRefusedAfterCancellation(t *testing.T) {
	for _, policy := range []LoggingPolicy{LoggingTry, LoggingWait} {
		var unslottedHook, rejectionHook int
		g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Second, 100, 1), Hooks{
			OnUnslotted: func() { unslottedHook++ },
			OnRejection: func(Outcome, Mode) { rejectionHook++ },
		})
		lease := acquireTestLease(t, g)
		holder, _, err := g.TryEvaluation(context.Background(), lease)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		token, outcome, err := g.AcquireLoggingEvaluation(ctx, lease, policy)
		if token != nil || outcome != Unslotted || err != nil {
			t.Fatalf("policy %d: cancelled saturated logging = (%v, %s, %v)", policy, token, outcome, err)
		}
		snap := g.Snapshot()
		if snap.UnslottedLogging != 1 || unslottedHook != 1 || snap.WaitingEvaluations != 0 {
			t.Fatalf("policy %d: unslotted=%d hook=%d waiting=%d", policy, snap.UnslottedLogging, unslottedHook, snap.WaitingEvaluations)
		}
		if snap.Rejections != ([rejectionReasons][2]uint64{}) || rejectionHook != 0 {
			t.Fatalf("policy %d: phase-5 overflow recorded as rejection: %v hook=%d", policy, snap.Rejections, rejectionHook)
		}
		if err := holder.Release(); err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLoggingTryDoesNotWait(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Second, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	holder, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan Outcome, 1)
	go func() {
		_, outcome, _ := g.AcquireLoggingEvaluation(context.Background(), lease, LoggingTry)
		done <- outcome
	}()
	select {
	case outcome := <-done:
		if outcome != Unslotted {
			t.Fatalf("try logging outcome = %s", outcome)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("LoggingTry waited for a slot while unread request-body data may remain")
	}
	if g.Snapshot().UnslottedLogging != 1 {
		t.Fatalf("unslotted = %d", g.Snapshot().UnslottedLogging)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggingWaitGetsReleasedSlot(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Second, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	holder, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		token   *Evaluation
		outcome Outcome
		err     error
	}
	done := make(chan result, 1)
	go func() {
		token, outcome, err := g.AcquireLoggingEvaluation(context.Background(), lease, LoggingWait)
		done <- result{token, outcome, err}
	}()
	waitForWaiters(t, g, 1)
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	got := <-done
	if got.token == nil || got.outcome != Acquired || got.err != nil {
		t.Fatalf("waiting logging = (%v, %s, %v)", got.token, got.outcome, got.err)
	}
	if g.Snapshot().UnslottedLogging != 0 {
		t.Fatal("slotted logging counted as unslotted")
	}
	if err := got.token.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggingWaitCancelledMidWaitRunsUnslotted(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Second, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	holder, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		token, outcome, err := g.AcquireLoggingEvaluation(ctx, lease, LoggingWait)
		if err == nil && (token != nil || outcome != Unslotted) {
			err = fmt.Errorf("mid-wait cancellation = (%v, %s)", token, outcome)
		}
		done <- err
	}()
	waitForWaiters(t, g, 1)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	snap := g.Snapshot()
	if snap.UnslottedLogging != 1 || snap.WaitingEvaluations != 0 || snap.Rejections != ([rejectionReasons][2]uint64{}) {
		t.Fatalf("snapshot after cancelled logging wait = %+v", snap)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggingWaitExpiryRunsUnslottedWithoutRejection(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 10*time.Millisecond, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	holder, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	token, outcome, err := g.AcquireLoggingEvaluation(context.Background(), lease, LoggingWait)
	if token != nil || outcome != Unslotted || err != nil {
		t.Fatalf("expired logging wait = (%v, %s, %v)", token, outcome, err)
	}
	if snap := g.Snapshot(); snap.Rejections != ([rejectionReasons][2]uint64{}) || snap.UnslottedLogging != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestLoggingObserveNeverWaitsOrRecordsWouldReject(t *testing.T) {
	g := newTestGovernor(t, testLimits(Observe, 1, 10*time.Second, 100, 1), Hooks{})
	lease := acquireTestLease(t, g)
	holder, _, err := g.TryEvaluation(context.Background(), lease)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range []context.Context{context.Background(), ctx} {
		token, outcome, err := g.AcquireLoggingEvaluation(c, lease, LoggingWait)
		if token != nil || outcome != Unslotted || err != nil {
			t.Fatalf("observe logging = (%v, %s, %v)", token, outcome, err)
		}
	}
	if snap := g.Snapshot(); snap.Rejections != ([rejectionReasons][2]uint64{}) || snap.UnslottedLogging != 2 || snap.ActiveEvaluations != 1 {
		t.Fatalf("observe snapshot = %+v", snap)
	}
	if err := holder.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestObserveLoggingParityWithEnforce is the ADR 0032 observe-parity case: with
// slots = 1, a phase-5 overflow followed by phase 1–4 work reports the same
// decisions in observe as in enforce.
func TestObserveLoggingParityWithEnforce(t *testing.T) {
	type decision struct {
		logging, later Outcome
		rejections     [rejectionReasons][2]uint64
	}
	run := func(mode Mode) decision {
		g := newTestGovernor(t, testLimits(mode, 1, 0, 100, 1), Hooks{})
		lease := acquireTestLease(t, g)
		holder, _, err := g.TryEvaluation(context.Background(), lease)
		if err != nil {
			t.Fatal(err)
		}
		logging, loggingOutcome, err := g.AcquireLoggingEvaluation(context.Background(), lease, LoggingTry)
		if err != nil {
			t.Fatal(err)
		}
		if err := holder.Release(); err != nil {
			t.Fatal(err)
		}
		later, laterOutcome, err := g.TryEvaluation(context.Background(), lease)
		if err != nil {
			t.Fatalf("%s: later phase = %s, %v", mode, laterOutcome, err)
		}
		snap := g.Snapshot()
		if logging != nil {
			_ = logging.Release()
		}
		if err := later.Release(); err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		var rejections [rejectionReasons][2]uint64
		for reason := range snap.Rejections {
			rejections[reason][0] = snap.Rejections[reason][0] + snap.Rejections[reason][1]
		}
		return decision{loggingOutcome, laterOutcome, rejections}
	}
	enforce, observe := run(Enforce), run(Observe)
	if enforce != observe || observe.logging != Unslotted || observe.later != Acquired {
		t.Fatalf("enforce %+v != observe %+v", enforce, observe)
	}
}

func TestReleasedChargesDoNotAccumulate(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 1<<20, 16384), Hooks{})
	lease := acquireTestLease(t, g)
	kept, err := lease.Charge(BufferBytes, 7)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100000; i++ {
		c, err := lease.Charge(BufferBytes, 1)
		if err != nil {
			t.Fatal(err)
		}
		if err := c.Release(); err != nil {
			t.Fatal(err)
		}
		if err := c.Release(); err != nil {
			t.Fatalf("second release: %v", err)
		}
	}
	g.mu.Lock()
	records, capacity := len(lease.charges), cap(lease.charges)
	g.mu.Unlock()
	if records != 2 || capacity > 64 {
		t.Fatalf("retained %d charge records (cap %d), want base + 1 live", records, capacity)
	}
	if snap := g.Snapshot(); snap.Usage[BufferBytes] != 7 || snap.Usage[BaseBytes] != 16384 {
		t.Fatalf("usage = %v", snap.Usage)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if err := kept.Release(); err != nil {
		t.Fatalf("release after close of already-refunded charge: %v", err)
	}
	if snap := g.Snapshot(); snap.Usage != ([byteClassCount]uint64{}) {
		t.Fatalf("usage after close = %v", snap.Usage)
	}
}

func TestChargeReleaseOrderKeepsIndexes(t *testing.T) {
	g := newTestGovernor(t, testLimits(Enforce, 1, 0, 1<<20, 1), Hooks{})
	lease := acquireTestLease(t, g)
	var charges []*Charge
	for i := 1; i <= 200; i++ {
		c, err := lease.Charge(WAFStateBytes, uint64(i))
		if err != nil {
			t.Fatal(err)
		}
		charges = append(charges, c)
	}
	// Release in an interleaved order to exercise swap-delete and shrinking.
	for _, i := range []int{0, 199, 50, 1, 198} {
		if err := charges[i].Release(); err != nil {
			t.Fatal(err)
		}
	}
	for i := 2; i < 198; i += 2 {
		if i == 50 {
			continue
		}
		if err := charges[i].Release(); err != nil {
			t.Fatal(err)
		}
	}
	var want uint64
	for i := 3; i < 198; i += 2 {
		want += uint64(i + 1)
	}
	g.mu.Lock()
	for i, c := range lease.charges {
		if c.index != i || c.released {
			g.mu.Unlock()
			t.Fatalf("charge record %d has index %d released=%v", i, c.index, c.released)
		}
	}
	live := len(lease.charges)
	g.mu.Unlock()
	if live != 1+98 {
		t.Fatalf("live records = %d", live)
	}
	if got := g.Snapshot().Usage[WAFStateBytes]; got != want {
		t.Fatalf("waf usage = %d want %d", got, want)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if snap := g.Snapshot(); snap.Usage != ([byteClassCount]uint64{}) {
		t.Fatalf("usage after close = %v", snap.Usage)
	}
}
