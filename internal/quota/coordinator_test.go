// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package quota

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public":  {Shared: Limit{Rate: 4, Burst: 4}, PerCaller: Limit{Rate: 1, Burst: 1}},
			"private": {Shared: Limit{Rate: 6, Burst: 6}, PerCaller: Limit{Rate: 2, Burst: 2}},
		},
	}
}

func testCoordinator(t *testing.T, policy Policy, maxCallers int, now *time.Time) *Coordinator {
	t.Helper()
	c, err := NewCoordinator(policy, maxCallers)
	if err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return *now }
	return c
}

func admit(t *testing.T, c *Coordinator, class, caller string) Decision {
	t.Helper()
	decision, err := c.Admit(class, caller)
	if err != nil {
		t.Fatal(err)
	}
	return decision
}

func TestPolicyValidation(t *testing.T) {
	tests := []struct {
		name       string
		policy     Policy
		maxCallers int
	}{
		{"valid", testPolicy(), 2},
		{"missing application rate", Policy{Application: Limit{Burst: 1}}, 2},
		{"zero caller capacity", testPolicy(), 0},
		{"no classes", Policy{Application: Limit{Rate: 1, Burst: 1}}, 2},
		{"empty class name", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"": {}}}, 2},
		{"invalid shared", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"x": {}}}, 2},
		{"invalid caller", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 1, Burst: 1}}}}, 2},
		{"rate too large", Policy{Application: Limit{Rate: maxRateOrBurst + 1, Burst: 1}}, 2},
		{"burst too large", Policy{Application: Limit{Rate: 1, Burst: maxRateOrBurst + 1}}, 2},
		{"rate oversubscribed", Policy{Application: Limit{Rate: 1, Burst: 3}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 2, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}}, 2},
		{"burst oversubscribed", Policy{Application: Limit{Rate: 3, Burst: 1}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 1, Burst: 2}, PerCaller: Limit{Rate: 1, Burst: 1}}}}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewCoordinator(tt.policy, tt.maxCallers)
			if (err == nil) != (tt.name == "valid") {
				t.Fatalf("NewCoordinator error = %v", err)
			}
		})
	}
}

func TestDeniedCallerDoesNotSpendSharedBudgets(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 20, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("first request denied")
	}
	if got := admit(t, c, "public", "alice"); got.Allowed || got.Reason != ReasonCaller || got.RetryAfter != time.Second {
		t.Fatalf("exhausted caller decision = %+v", got)
	}
	for _, caller := range []string{"bob", "carol", "david"} {
		if got := admit(t, c, "public", caller); !got.Allowed {
			t.Fatalf("%s denied after another caller was rejected: %+v", caller, got)
		}
	}
	if got := admit(t, c, "public", "eve"); got.Allowed || got.Reason != ReasonClass || got.RetryAfter != 250*time.Millisecond {
		t.Fatalf("exhausted class decision = %+v", got)
	}
	for i := 0; i < 6; i++ {
		if got := admit(t, c, "private", fmt.Sprint(i)); !got.Allowed {
			t.Fatalf("protected class denied after public refusals: %+v", got)
		}
	}
}

func TestRefillAndRetry(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 10, &now)
	admit(t, c, "public", "alice")
	now = now.Add(500 * time.Millisecond)
	if got := admit(t, c, "public", "alice"); got.Allowed || got.RetryAfter != 500*time.Millisecond {
		t.Fatalf("partial refill decision = %+v", got)
	}
	now = now.Add(500 * time.Millisecond)
	if got := admit(t, c, "public", "alice"); !got.Allowed {
		t.Fatalf("fully refilled caller denied: %+v", got)
	}
}

func TestSubnanosecondRefillDoesNotExceedBurst(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 750_000_000, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 750_000_000, Burst: 1}, PerCaller: Limit{Rate: 750_000_000, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 1, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("first request denied")
	}
	now = now.Add(time.Nanosecond)
	if got := admit(t, c, "public", "alice"); got.Allowed || got.RetryAfter != time.Nanosecond {
		t.Fatalf("partially refilled bucket admitted: %+v", got)
	}
	now = now.Add(time.Nanosecond)
	if got := admit(t, c, "public", "alice"); !got.Allowed {
		t.Fatalf("refilled bucket denied: %+v", got)
	}
}

func TestCallerCapacityPreservesActiveBuckets(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 1, &now)
	if got := admit(t, c, "public", "alice"); !got.Allowed {
		t.Fatal(got)
	}
	if got := admit(t, c, "public", "bob"); got.Allowed || got.Reason != ReasonCallerCapacity || got.RetryAfter != 0 {
		t.Fatalf("full store decision = %+v", got)
	}
	if got := admit(t, c, "private", "carol"); !got.Allowed {
		t.Fatalf("public caller churn exhausted protected caller storage: %+v", got)
	}
	if got := admit(t, c, "public", "alice"); got.Allowed || got.Reason != ReasonCaller {
		t.Fatalf("active caller bucket reset: %+v", got)
	}
	now = now.Add(time.Second)
	if got := admit(t, c, "public", "bob"); !got.Allowed {
		t.Fatalf("fully refilled caller could not be retired: %+v", got)
	}
}

func TestCallerKeysAreClassScoped(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 10, &now)
	if !admit(t, c, "public", "same").Allowed || !admit(t, c, "private", "same").Allowed {
		t.Fatal("classes unexpectedly share caller buckets")
	}
	if got := admit(t, c, "public", "same"); got.Allowed || got.Reason != ReasonCaller {
		t.Fatalf("public caller decision = %+v", got)
	}
	if !admit(t, c, "private", "same").Allowed {
		t.Fatal("private caller lost its independent burst")
	}
}

func TestInvalidAdmission(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 1, &now)
	if _, err := c.Admit("missing", "alice"); err == nil {
		t.Fatal("unknown class accepted")
	}
	if _, err := c.Admit("public", ""); err == nil {
		t.Fatal("empty identity accepted")
	}
	if got := admit(t, c, "public", "alice"); !got.Allowed {
		t.Fatalf("invalid requests changed admission state: %+v", got)
	}
}

func TestConcurrentAdmissionsRespectSharedBurst(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 100, &now)
	const attempts = 64
	start := make(chan struct{})
	results := make(chan Decision, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			decision, err := c.Admit("public", fmt.Sprint(i))
			if err != nil {
				t.Errorf("Admit: %v", err)
			}
			results <- decision
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	allowed := 0
	for result := range results {
		if result.Allowed {
			allowed++
		}
	}
	if allowed != 4 {
		t.Fatalf("allowed %d concurrent requests, want 4", allowed)
	}
}
