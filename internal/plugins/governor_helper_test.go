// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"testing"

	"jul/internal/inspection"
)

// testLimits is a roomy enforce budget with the inspection body timer
// disabled, so tests that serve through httptest recorders (which have no
// read deadline) exercise the plugin paths they target. Deadline and overload
// tests publish their own limits.
func testLimits() inspection.Limits {
	return inspection.Limits{
		Mode:                   inspection.Enforce,
		MaxBytes:               1 << 30,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		BaseChargeBytes:        16 << 10,
	}
}

func testGovernor(tb testing.TB) *inspection.Governor {
	tb.Helper()
	g, err := inspection.New(testLimits(), inspection.Hooks{})
	if err != nil {
		tb.Fatalf("inspection.New: %v", err)
	}
	return g
}

// testLease acquires a request lease from a fresh test governor, closed when
// the test ends.
func testLease(tb testing.TB) *inspection.Lease {
	tb.Helper()
	lease, _, _, err := testGovernor(tb).AcquireLease(context.Background())
	if err != nil {
		tb.Fatalf("AcquireLease: %v", err)
	}
	tb.Cleanup(func() { _ = lease.Close() })
	return lease
}

// leaseCtx returns ctx carrying a test request lease, as an outer inspecting
// layer would.
func leaseCtx(tb testing.TB, ctx context.Context) context.Context {
	tb.Helper()
	lease, ctx, _, err := testGovernor(tb).AcquireLease(ctx)
	if err != nil {
		tb.Fatalf("AcquireLease: %v", err)
	}
	tb.Cleanup(func() { _ = lease.Close() })
	return ctx
}

// freeCharge is an unlimited chargeFunc for tests about sizing, not budget.
func freeCharge(n uint64) (byteCharge, error) { return &fakeCharge{n: n}, nil }

// fakeCharge is a byteCharge recording its current size.
type fakeCharge struct {
	n        uint64
	released bool
}

func (c *fakeCharge) Resize(n uint64) error { c.n = n; return nil }
func (c *fakeCharge) Release() error        { c.released = true; c.n = 0; return nil }

// invocationOr returns the invocation carried by ctx, or a fresh one holding
// a test lease.
func invocationOr(tb testing.TB, ctx context.Context) *invocation {
	if inv := invocationFrom(ctx); inv != nil {
		return inv
	}
	return &invocation{lease: testLease(tb)}
}
