// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/resilience"
)

var errAttempt = errors.New("attempt failed")

// TestRetryableEligibilityMatrix walks every condition in ADR 0017's
// eligibility rule, and — more importantly — pins the precedence between them.
// A rule that returns the right yes/no but the wrong reason sends an operator
// to tune a control that was never involved.
func TestRetryableEligibilityMatrix(t *testing.T) {
	// base is eligible in every respect, so each case changes exactly one thing
	// and the failure message names that thing.
	base := Decision{
		Err:            errAttempt,
		Attempts:       1,
		MaxAttempts:    3,
		Replayable:     true,
		Remaining:      time.Second,
		UntriedBackend: true,
		BudgetAllows:   true,
	}
	mut := func(f func(*Decision)) Decision {
		d := base
		f(&d)
		return d
	}

	cases := []struct {
		name string
		in   Decision
		want StopReason
	}{
		{"all conditions hold", base, StopNone},
		{"no error is success", mut(func(d *Decision) { d.Err = nil }), StopSuccess},
		{"response started", mut(func(d *Decision) { d.ResponseStarted = true }), StopResponseStarted},
		{"not replayable", mut(func(d *Decision) { d.Replayable = false }), StopNotRetryable},
		{"terminal error", mut(func(d *Decision) { d.Terminal = true }), StopTerminalError},
		{"client cancelled", mut(func(d *Decision) { d.Err = context.Canceled }), StopCancelled},
		{"wrapped cancellation still cancelled", mut(func(d *Decision) {
			d.Err = fmt.Errorf("dial: %w", context.Canceled)
		}), StopCancelled},
		{"attempts exhausted", mut(func(d *Decision) { d.Attempts = 3 }), StopAttempts},
		{"attempts beyond cap", mut(func(d *Decision) { d.Attempts = 9 }), StopAttempts},
		{"zero cap is not exhaustion", mut(func(d *Decision) { d.MaxAttempts = 0; d.Attempts = 99 }), StopNone},
		{"deadline spent", mut(func(d *Decision) { d.Remaining = 0 }), StopDeadline},
		{"deadline overshot", mut(func(d *Decision) { d.Remaining = -time.Second }), StopDeadline},
		{"no untried backend", mut(func(d *Decision) { d.UntriedBackend = false }), StopNoBackend},
		{"budget spent", mut(func(d *Decision) { d.BudgetAllows = false }), StopBudget},

		// Precedence. Each of these has two reasons to stop; the rule must
		// report the one the operator can act on.
		{"response started outranks budget", mut(func(d *Decision) {
			d.ResponseStarted = true
			d.BudgetAllows = false
		}), StopResponseStarted},
		{"response started outranks attempts", mut(func(d *Decision) {
			d.ResponseStarted = true
			d.Attempts = 3
		}), StopResponseStarted},
		{"not replayable outranks deadline", mut(func(d *Decision) {
			d.Replayable = false
			d.Remaining = 0
		}), StopNotRetryable},
		{"terminal outranks no backend", mut(func(d *Decision) {
			d.Terminal = true
			d.UntriedBackend = false
		}), StopTerminalError},
		{"cancellation outranks attempts", mut(func(d *Decision) {
			d.Err = context.Canceled
			d.Attempts = 3
		}), StopCancelled},
		{"no backend outranks budget", mut(func(d *Decision) {
			d.UntriedBackend = false
			d.BudgetAllows = false
		}), StopNoBackend},
		{"success outranks everything", mut(func(d *Decision) {
			d.Err = nil
			d.ResponseStarted = true
			d.Replayable = false
			d.BudgetAllows = false
		}), StopSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Retryable(tc.in); got != tc.want {
				t.Fatalf("Retryable = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBudgetFloorAllowsRetryWithoutTraffic pins the first of the four
// documented points: with no primaries the ratio term is zero, so only the free
// floor applies. Without it the first request after an idle period would be
// unretryable, which is exactly when a stale pooled connection is most likely.
func TestBudgetFloorAllowsRetryWithoutTraffic(t *testing.T) {
	b := NewBudget(10)
	for i := range minFreeRetries {
		if !b.Allow() {
			t.Fatalf("retry %d denied while inside the free floor", i)
		}
	}
	if b.Allow() {
		t.Fatal("retry allowed past the free floor with no primaries to earn it")
	}
}

// TestBudgetRatioIsExact pins the second and third points: the boundary is
// floor(primaries*percent/100)+minFreeRetries, allowed strictly below it.
func TestBudgetRatioIsExact(t *testing.T) {
	for _, tc := range []struct {
		primaries, percent, want int
	}{
		{100, 10, 10 + minFreeRetries},
		{100, 0, 0}, // 0 is unbudgeted, not "floor only"
		{95, 10, 9 + minFreeRetries},
		{9, 10, 0 + minFreeRetries},
		{100, 100, 100 + minFreeRetries},
	} {
		t.Run(fmt.Sprintf("p%d_%d%%", tc.primaries, tc.percent), func(t *testing.T) {
			b := NewBudget(tc.percent)
			for range tc.primaries {
				b.Primary()
			}
			if tc.percent == 0 {
				// Unbudgeted must never deny, which is what makes the default
				// reproduce today's behaviour.
				for range 1000 {
					if !b.Allow() {
						t.Fatal("unbudgeted policy denied a retry")
					}
				}
				return
			}
			allowed := 0
			for range tc.want + 10 {
				if b.Allow() {
					allowed++
				}
			}
			if allowed != tc.want {
				t.Fatalf("allowed %d retries, want exactly %d", allowed, tc.want)
			}
		})
	}
}

// TestBudgetWindowSlides pins the fourth point: accounting rotates, an old
// window eventually stops counting, and a gap longer than one window carries
// nothing forward because there is no recent load to take a ratio of.
func TestBudgetWindowSlides(t *testing.T) {
	now := time.Unix(0, 0)
	b := NewBudget(10)
	b.now = func() time.Time { return now }
	b.win.Store(&budgetWindow{epoch: now.UnixNano() / int64(retryBudgetWindow)})

	for range 100 {
		b.Primary()
	}
	spend := func() int {
		n := 0
		for b.Allow() {
			n++
		}
		return n
	}
	if got := spend(); got != 10+minFreeRetries {
		t.Fatalf("first window allowed %d, want %d", got, 10+minFreeRetries)
	}

	// One window on, the trailing window still holds both the primaries and the
	// retries already spent against them, so rotation must not hand out a
	// second allowance. This is the anti-burst property: the window slides, it
	// does not reset.
	now = now.Add(retryBudgetWindow)
	if got := spend(); got != 0 {
		t.Fatalf("rotation handed out %d extra retries; the trailing window still holds the ones already spent", got)
	}

	// Fresh traffic in the new window earns fresh allowance, proportionally.
	for range 100 {
		b.Primary()
	}
	if got := spend(); got != 10 {
		t.Fatalf("new primaries earned %d retries, want 10 (the floor was already consumed in this window)", got)
	}

	// Two windows on with no traffic: nothing is carried, so only the floor
	// remains.
	now = now.Add(3 * retryBudgetWindow)
	if got := spend(); got != minFreeRetries {
		t.Fatalf("after an idle gap allowed %d, want the free floor %d", got, minFreeRetries)
	}
}

// TestBudgetSurvivesPolicySwap is the property that stops a reload from
// granting a fresh burst of retries. It is the whole reason the counters live
// on the pool rather than on the policy.
func TestBudgetSurvivesPolicySwap(t *testing.T) {
	b := NewBudget(10)
	for range 100 {
		b.Primary()
	}
	for range 10 + minFreeRetries {
		if !b.Allow() {
			t.Fatal("budget denied a retry it had earned")
		}
	}
	if b.Allow() {
		t.Fatal("budget over-spent before the swap")
	}
	b.SetPercent(10)
	if b.Allow() {
		t.Fatal("re-applying the same percentage refilled the window: a reload during an incident would forgive the retry load that caused it")
	}
}

// TestBudgetAllowIsExactUnderConcurrency pins the CAS claim. A racing
// read-then-increment would let two goroutines spend the same last allowance,
// and the overshoot would be invisible in production.
func TestBudgetAllowIsExactUnderConcurrency(t *testing.T) {
	const primaries, percent, goroutines = 1000, 10, 64
	b := NewBudget(percent)
	for range primaries {
		b.Primary()
	}
	want := primaries*percent/100 + minFreeRetries

	var mu sync.Mutex
	allowed := 0
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := 0
			for range want {
				if b.Allow() {
					local++
				}
			}
			mu.Lock()
			allowed += local
			mu.Unlock()
		}()
	}
	wg.Wait()
	if allowed != want {
		t.Fatalf("allowed %d retries under concurrency, want exactly %d", allowed, want)
	}
}

// TestBackoffIsBoundedAndJittered pins the jitter range and the ×2 growth. Full
// jitter is the point: the delay must be able to land anywhere in [0, d), so
// clients of a backend that died together do not return together.
func TestBackoffIsBoundedAndJittered(t *testing.T) {
	const initial, max = 10 * time.Millisecond, 80 * time.Millisecond
	// ceilings are the un-jittered interval per attempt: 10, 20, 40, 80, 80.
	ceilings := []time.Duration{initial, 2 * initial, 4 * initial, max, max}
	for n, ceiling := range ceilings {
		low, high := false, false
		for range 200 {
			d, ok := backoffFor(n+1, initial, max, time.Hour, fullJitter)
			if !ok {
				t.Fatalf("attempt %d: backoff refused with an hour remaining", n+1)
			}
			if d < 0 || d >= ceiling {
				t.Fatalf("attempt %d: delay %s outside [0, %s)", n+1, d, ceiling)
			}
			if d < ceiling/4 {
				low = true
			}
			if d > ceiling/2 {
				high = true
			}
		}
		if !low || !high {
			t.Fatalf("attempt %d: jitter did not span the interval (low=%v high=%v)", n+1, low, high)
		}
	}
}

// TestBackoffStopsRatherThanSleepingIntoTheDeadline pins the ADR's rule that a
// clamp leaving no room for an attempt must stop the loop. Sleeping to the
// deadline only converts a failure into a slower failure.
func TestBackoffStopsRatherThanSleepingIntoTheDeadline(t *testing.T) {
	if _, ok := backoffFor(1, 100*time.Millisecond, time.Second, 50*time.Millisecond, func(d time.Duration) time.Duration { return d }); ok {
		t.Fatal("backoff accepted a delay longer than the remaining deadline")
	}
	if _, ok := backoffFor(1, 100*time.Millisecond, time.Second, 100*time.Millisecond, func(d time.Duration) time.Duration { return d }); ok {
		t.Fatal("backoff accepted a delay that exactly consumes the deadline, leaving no room to attempt")
	}
	if _, ok := backoffFor(1, 10*time.Millisecond, time.Second, time.Second, func(d time.Duration) time.Duration { return d }); !ok {
		t.Fatal("backoff refused a delay that comfortably fits")
	}
	// No backoff configured: the sequence continues while any deadline remains.
	if _, ok := backoffFor(1, 0, 0, time.Nanosecond, fullJitter); !ok {
		t.Fatal("zero backoff refused while the deadline still had room")
	}
	if _, ok := backoffFor(1, 0, 0, 0, fullJitter); ok {
		t.Fatal("zero backoff accepted with the deadline spent")
	}
}

func BenchmarkRetryBudgetAllow(b *testing.B) {
	bd := NewBudget(10)
	for range 1 << 20 {
		bd.Primary()
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		bd.Allow()
	}
}

func BenchmarkRetryable(b *testing.B) {
	d := Decision{Err: errAttempt, Attempts: 1, MaxAttempts: 3, Replayable: true, Remaining: time.Second, UntriedBackend: true, BudgetAllows: true}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		_ = Retryable(d)
	}
}

// FuzzBudgetWindowRotation drives rotation with arbitrary time jumps,
// including backwards ones, against the invariants that matter: the accounting
// never goes negative, and no sequence of jumps ever lets a caller spend more
// than the window it has actually earned.
//
// Rotation is the part of a budget most likely to be wrong in a way no unit
// test notices, because the bug only appears at a bucket boundary under load.
func FuzzBudgetWindowRotation(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5}, uint8(10))
	f.Add([]byte{0, 0, 0}, uint8(100))
	f.Add([]byte{255, 128, 1, 0, 7, 9}, uint8(1))

	f.Fuzz(func(t *testing.T, ops []byte, percent uint8) {
		if percent == 0 {
			percent = 1
		}
		now := time.Unix(0, 0)
		b := NewBudget(int(percent))
		b.now = func() time.Time { return now }
		b.win.Store(&budgetWindow{epoch: now.UnixNano() / int64(retryBudgetWindow)})

		for _, op := range ops {
			switch op % 4 {
			case 0:
				b.Primary()
			case 1:
				// The contract is about the moment of consumption: a granted
				// retry must have satisfied the bound when it was granted. The
				// state has to be read from the same window Allow will use, so
				// rotation is forced first — reading the stale window would
				// compare against a bound that no longer applies.
				w := b.window()
				spent := w.retries.Load() + w.prevRetries
				limit := (w.primaries.Load()+w.prevPrimaries)*int64(percent)/100 + minFreeRetries
				if b.Allow() && spent >= limit {
					t.Fatalf("granted a retry with %d already spent against a limit of %d", spent, limit)
				}
			case 2:
				now = now.Add(time.Duration(op) * retryBudgetWindow / 8)
			case 3:
				// Backwards. A clock stepping back must not reset accounting,
				// or an NTP correction would hand out a free retry burst.
				now = now.Add(-time.Duration(op) * retryBudgetWindow / 8)
			}
			w := b.win.Load()
			if got := w.primaries.Load(); got < 0 {
				t.Fatalf("primaries went negative: %d", got)
			}
			if got := w.retries.Load(); got < 0 {
				t.Fatalf("retries went negative: %d", got)
			}
			if w.prevPrimaries < 0 || w.prevRetries < 0 {
				t.Fatalf("carried counters went negative: %d/%d", w.prevPrimaries, w.prevRetries)
			}
		}
	})
}

// TestDoDeadlineDominatesEveryAttempt pins the ADR's "the overall deadline
// dominates" rule: the retry deadline bounds the whole sequence, not each
// attempt, so a slow backend cannot multiply it by the attempt count.
func TestDoDeadlineDominatesEveryAttempt(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4")
	start := time.Now()
	attempts := 0
	reason, err := p.Do(context.Background(), RetryRequest{Deadline: 120 * time.Millisecond, Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			attempts++
			select {
			case <-ctx.Done():
			case <-time.After(80 * time.Millisecond):
			}
			return AttemptResult{Err: errAttempt}
		})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected the sequence to fail")
	}
	if reason != StopDeadline {
		t.Fatalf("stopped with %q, want %q", reason, StopDeadline)
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("sequence took %s; the deadline bounded each attempt instead of the whole sequence", elapsed)
	}
	if attempts < 2 {
		t.Fatalf("made %d attempts; the deadline should have allowed more than one", attempts)
	}
}

// TestDoStopsAtAttemptCap pins that retry_attempts caps attempts even while
// untried backends remain.
func TestDoStopsAtAttemptCap(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4")
	attempts := 0
	reason, _ := p.Do(context.Background(), RetryRequest{MaxAttempts: 2, Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			attempts++
			return AttemptResult{Err: errAttempt}
		})
	if attempts != 2 {
		t.Fatalf("made %d attempts, want the configured cap of 2", attempts)
	}
	if reason != StopAttempts {
		t.Fatalf("stopped with %q, want %q", reason, StopAttempts)
	}
}

// TestDoTriesEveryDistinctBackendOnceByDefault pins the zero-value behaviour:
// with no cap the backend supply is the bound, which is what Jul does today.
func TestDoTriesEveryDistinctBackendOnceByDefault(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	seen := map[BackendIdentity]int{}
	reason, _ := p.Do(context.Background(), RetryRequest{Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			seen[b.Identity()]++
			return AttemptResult{Err: errAttempt}
		})
	if len(seen) != 3 {
		t.Fatalf("tried %d distinct backends, want 3", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("backend %v tried %d times, want once", id, n)
		}
	}
	if reason != StopNoBackend {
		t.Fatalf("stopped with %q, want %q", reason, StopNoBackend)
	}
}

// TestDoDoesNotRetryUnreplayableRequests pins that a request that cannot be
// replayed makes exactly one attempt, whatever the backend supply.
func TestDoDoesNotRetryUnreplayableRequests(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	attempts := 0
	reason, _ := p.Do(context.Background(), RetryRequest{Replayable: false},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			attempts++
			return AttemptResult{Err: errAttempt}
		})
	if attempts != 1 {
		t.Fatalf("made %d attempts on an unreplayable request, want 1", attempts)
	}
	if reason != StopNotRetryable {
		t.Fatalf("stopped with %q, want %q", reason, StopNotRetryable)
	}
}

// TestDoReleasesEveryFailedAttempt pins the driver's ownership contract: a
// failed attempt's slot is released by the driver, so a retry sequence cannot
// leak in-flight counts and skew least-conn balancing.
func TestDoReleasesEveryFailedAttempt(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")
	_, _ = p.Do(context.Background(), RetryRequest{Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			if got := b.inflight.Load(); got != 1 {
				t.Errorf("attempt %d: backend in-flight is %d during the attempt, want 1", n, got)
			}
			return AttemptResult{Err: errAttempt}
		})
	for _, b := range p.Backends() {
		if got := b.inflight.Load(); got != 0 {
			t.Fatalf("backend %s left with in-flight %d after the sequence", b.Address, got)
		}
	}
}

// TestDoRetainsTheSuccessfulBackend pins the other half of that contract: on a
// retained success the caller owns exactly one Release, because an HTTP
// response body outlives the round trip that produced it.
func TestDoRetainsTheSuccessfulBackend(t *testing.T) {
	p := testPool(t, "127.0.0.1:1")
	var kept Attempt
	reason, err := p.Do(context.Background(), RetryRequest{Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			kept = b
			return AttemptResult{Retain: true}
		})
	if err != nil || reason != StopSuccess {
		t.Fatalf("Do = %q, %v; want success", reason, err)
	}
	if got := kept.inflight.Load(); got != 1 {
		t.Fatalf("retained backend in-flight is %d, want the caller to still own 1", got)
	}
	p.Release(kept.Backend)
	if got := kept.inflight.Load(); got != 0 {
		t.Fatalf("after the caller released, in-flight is %d, want 0", got)
	}
}

// TestDoBudgetStopsAmplification is the property the budget exists for: a total
// outage must not multiply upstream load by the backend count.
func TestDoBudgetStopsAmplification(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3", "127.0.0.1:4", "127.0.0.1:5")
	p.budget.SetPercent(1)
	// Drain the free floor so the ratio, not the floor, is what is under test.
	for range minFreeRetries {
		p.budget.Allow()
	}
	attempts := 0
	reason, _ := p.Do(context.Background(), RetryRequest{Replayable: true},
		func(ctx context.Context, b Attempt, n int) AttemptResult {
			attempts++
			return AttemptResult{Err: errAttempt}
		})
	if attempts != 1 {
		t.Fatalf("made %d attempts with the budget spent, want 1", attempts)
	}
	if reason != StopBudget {
		t.Fatalf("stopped with %q, want %q", reason, StopBudget)
	}
}

// testPool builds a round-robin pool over the given addresses. Passive health
// is set to trip only after many failures so a retry test exercises the retry
// rule rather than the cooldown that would otherwise remove backends underneath
// it.
func testPool(t *testing.T, addrs ...string) *Pool {
	t.Helper()
	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	p, err := NewPool(config.UpstreamConfig{
		Name:     "retry",
		Strategy: "round_robin",
		Servers:  servers,
		MaxFails: 1000,
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

// TestRetrySafeMethod pins the one list every adapter gates on. PUT and DELETE
// are here and POST and PATCH are not, which is the line between "repeating
// this is defined to be harmless" and "repeating this may charge a card twice".
func TestRetrySafeMethod(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS", "TRACE", "PUT", "DELETE"} {
		if !RetrySafeMethod(m) {
			t.Errorf("%s should be retry-safe", m)
		}
	}
	for _, m := range []string{"POST", "PATCH", "CONNECT", "", "get", "PROPFIND"} {
		if RetrySafeMethod(m) {
			t.Errorf("%q must not be retry-safe", m)
		}
	}
}

// TestRetryRequestForMergesLocationOverPool pins the override rule that every
// adapter now shares: a set location value wins, and a zero one inherits rather
// than meaning "unlimited". One field reading its zero the other way would be a
// trap, so the rule is pinned here rather than in each adapter.
func TestRetryRequestForMergesLocationOverPool(t *testing.T) {
	p := testPool(t, "127.0.0.1:1")
	policy, err := resilience.Resolve(resilience.Options{
		RetryAttempts:       7,
		RetryDeadline:       9 * time.Second,
		RetryBackoffInitial: 30 * time.Millisecond,
		RetryBackoffMax:     900 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.SetPolicy(policy)

	for _, tc := range []struct {
		name string
		over RetryOverride
		want RetryRequest
	}{
		{
			name: "an empty override inherits every pool value",
			want: RetryRequest{MaxAttempts: 7, Deadline: 9 * time.Second, BackoffInitial: 30 * time.Millisecond, BackoffMax: 900 * time.Millisecond},
		},
		{
			name: "set values win field by field",
			over: RetryOverride{Attempts: 2, Deadline: time.Second},
			want: RetryRequest{MaxAttempts: 2, Deadline: time.Second, BackoffInitial: 30 * time.Millisecond, BackoffMax: 900 * time.Millisecond},
		},
		{
			name: "a location backoff replaces the pool's pair, not half of it",
			over: RetryOverride{BackoffInitial: 5 * time.Millisecond, BackoffMax: 50 * time.Millisecond},
			want: RetryRequest{MaxAttempts: 7, Deadline: 9 * time.Second, BackoffInitial: 5 * time.Millisecond, BackoffMax: 50 * time.Millisecond},
		},
		{
			name: "a location backoff with no ceiling takes the default, not the pool's",
			over: RetryOverride{BackoffInitial: 5 * time.Millisecond},
			want: RetryRequest{MaxAttempts: 7, Deadline: 9 * time.Second, BackoffInitial: 5 * time.Millisecond, BackoffMax: resilience.DefaultRetryBackoffMax},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := p.RetryRequestFor(tc.over, true)
			tc.want.Replayable = true
			got.OnBackoff = nil
			if got.MaxAttempts != tc.want.MaxAttempts ||
				got.Deadline != tc.want.Deadline ||
				got.BackoffInitial != tc.want.BackoffInitial ||
				got.BackoffMax != tc.want.BackoffMax ||
				got.Replayable != tc.want.Replayable {
				t.Fatalf("RetryRequestFor = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestRetryRequestForReadsThePolicyPerCall pins that a resilience reload takes
// effect without rebuilding anything: the adapters hold the override, never a
// resolved request.
func TestRetryRequestForReadsThePolicyPerCall(t *testing.T) {
	p := testPool(t, "127.0.0.1:1")
	if got := p.RetryRequestFor(RetryOverride{}, false).MaxAttempts; got != 0 {
		t.Fatalf("MaxAttempts = %d, want the unconfigured default 0", got)
	}
	policy, err := resilience.Resolve(resilience.Options{RetryAttempts: 4})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	p.SetPolicy(policy)
	if got := p.RetryRequestFor(RetryOverride{}, false).MaxAttempts; got != 4 {
		t.Fatalf("MaxAttempts = %d after the swap, want 4", got)
	}
}

// TestRetryRequestForNilPool keeps the helper usable by an adapter built before
// its pool exists, which is how a literal proxy_pass target is handled.
func TestRetryRequestForNilPool(t *testing.T) {
	var p *Pool
	got := p.RetryRequestFor(RetryOverride{Attempts: 3}, true)
	if got.MaxAttempts != 3 || !got.Replayable {
		t.Fatalf("RetryRequestFor on a nil pool = %+v, want the override verbatim", got)
	}
}

// TestOnBackoffReportsEveryWaitBeforeItHappens pins the hook that lets a caller
// annotate its span with the retry delay. The wait is the driver's to compute
// and the span is the caller's, so without this hook a backoff shows up in a
// trace as unexplained latency between attempts.
func TestOnBackoffReportsEveryWaitBeforeItHappens(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2", "127.0.0.1:3")

	type call struct {
		next int
		d    time.Duration
	}
	var calls []call
	attempts := 0

	_, _ = p.Do(context.Background(), RetryRequest{
		Replayable:     true,
		BackoffInitial: time.Millisecond,
		BackoffMax:     4 * time.Millisecond,
		OnBackoff: func(next int, d time.Duration) {
			calls = append(calls, call{next, d})
			// The hook fires before the sleep, so the attempt it names must not
			// have run yet. A hook reporting a wait after the fact would put the
			// annotation on the wrong span.
			if next != attempts+1 {
				t.Errorf("OnBackoff announced attempt %d after %d attempts had run", next, attempts)
			}
		},
	}, func(ctx context.Context, b Attempt, n int) AttemptResult {
		attempts++
		return AttemptResult{Err: errAttempt}
	})

	// Three backends, three attempts, so two gaps between them.
	if len(calls) != attempts-1 {
		t.Fatalf("OnBackoff fired %d times over %d attempts, want one per gap", len(calls), attempts)
	}
	for i, c := range calls {
		if c.next != i+2 {
			t.Errorf("call %d announced attempt %d, want %d", i, c.next, i+2)
		}
		// Full jitter draws from [0, cap), so the only bound that always holds
		// is the clamp. Asserting a specific delay here would be asserting the
		// jitter, which is deliberately random.
		if c.d < 0 || c.d > 4*time.Millisecond {
			t.Errorf("call %d delay %v outside [0, BackoffMax]", i, c.d)
		}
	}
}

// A nil hook is the common case and must not panic.
func TestOnBackoffIsOptional(t *testing.T) {
	p := testPool(t, "127.0.0.1:1", "127.0.0.1:2")
	_, _ = p.Do(context.Background(), RetryRequest{
		Replayable:     true,
		BackoffInitial: time.Millisecond,
	}, func(ctx context.Context, b Attempt, n int) AttemptResult {
		return AttemptResult{Err: errAttempt}
	})
}
