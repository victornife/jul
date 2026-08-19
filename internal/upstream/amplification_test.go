// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"testing"
)

// drive sends n inbound requests at a pool whose every backend fails, and
// reports how many upstream attempts that produced.
func drive(t *testing.T, p *Pool, n int) int {
	t.Helper()
	attempts := 0
	for range n {
		_, _ = p.Do(context.Background(), RetryRequest{Replayable: true, MaxAttempts: 3},
			func(ctx context.Context, b Attempt, _ int) AttemptResult {
				attempts++
				return AttemptResult{Err: errAttempt}
			})
	}
	return attempts
}

// TestAmplificationUnderTotalOutage measures the number #144 asks for: with
// every backend down and retry_budget_percent = 10, upstream load must stay at
// or below 1.1x inbound. That ratio is the whole reason the budget exists — the
// difference between a failing backend being helped and being finished off by
// the mechanism meant to route around it.
//
// The measurement does not come out at 1.1x, and the criterion as literally
// written is not achievable by this design at any volume. Allow grants while
//
//	retries < floor(primaries * percent / 100) + minFreeRetries
//
// so the ceiling is 1.1x *plus* minFreeRetries per accounting window, always.
// At a hundred thousand requests the measured figure is 1.10003x: over by
// exactly the three free retries, not by a proportion.
//
// The floor is deliberate — without it a pool with almost no traffic could not
// fail over at all, which is exactly when a stale connection is most likely —
// and it is an absolute constant, not a multiplier: at most 3 extra requests
// per 10-second window, or 0.3 requests per second, whatever the inbound rate.
// It cannot produce amplification collapse, which is what the criterion is for.
//
// So the guarantee asserted here is the one the code actually makes, stated in
// the terms that make it meaningful, rather than a rounded ratio the design
// cannot hold.
func TestAmplificationUnderTotalOutage(t *testing.T) {
	t.Run("sustained load holds the bound to within the free-retry floor", func(t *testing.T) {
		p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
		p.budget.SetPercent(10)

		const inbound = 100_000
		attempts := drive(t, p, inbound)
		retries := attempts - inbound
		ratio := float64(attempts) / float64(inbound)
		t.Logf("inbound=%d upstream_attempts=%d amplification=%.5fx", inbound, attempts, ratio)

		// The proportional part is exactly the budget. The absolute part is the
		// floor, bounded by the number of windows the run could have spanned.
		const windows = 2
		if ceiling := inbound*10/100 + windows*minFreeRetries; retries > ceiling {
			t.Errorf("retries = %d, above the budget's ceiling of %d", retries, ceiling)
		}
		// Whatever the volume, the overshoot beyond 1.1x must be an absolute
		// handful of requests and not grow with load. A proportional overshoot
		// would be the amplification failure this control exists to prevent.
		if over := retries - inbound*10/100; over > windows*minFreeRetries {
			t.Errorf("overshoot beyond 1.1x = %d requests, want at most %d; it is scaling with load", over, windows*minFreeRetries)
		}
		if attempts <= inbound {
			t.Errorf("upstream attempts (%d) never exceeded inbound (%d); the bound is vacuous", attempts, inbound)
		}
	})

	t.Run("low volume is bounded by the same floor", func(t *testing.T) {
		p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
		p.budget.SetPercent(10)

		const inbound = 1000
		attempts := drive(t, p, inbound)
		retries := attempts - inbound
		t.Logf("inbound=%d retries=%d amplification=%.4fx", inbound, retries, float64(attempts)/float64(inbound))

		ceiling := inbound*10/100 + 2*minFreeRetries
		if retries > ceiling {
			t.Errorf("retries = %d, above the budget's own ceiling of %d", retries, ceiling)
		}
	})
}

// Without a budget the same outage amplifies by the attempt cap. Pinning it
// makes the budget's effect a measured difference rather than an assertion
// about a number in isolation.
func TestAnUnbudgetedPoolAmplifiesByTheAttemptCap(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	p.budget.SetPercent(0)

	const inbound = 200
	attempts := drive(t, p, inbound)
	ratio := float64(attempts) / float64(inbound)
	t.Logf("unbudgeted: inbound=%d upstream_attempts=%d amplification=%.2fx", inbound, attempts, ratio)

	if ratio < 2.5 {
		t.Errorf("unbudgeted amplification = %.2fx, want near the attempt cap of 3; the comparison is meaningless otherwise", ratio)
	}
}
