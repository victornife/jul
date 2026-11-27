// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package quota

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
		{"empty operation name", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}, Operations: map[string]Operation{"": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}}, 2},
		{"invalid operation shared", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}, Operations: map[string]Operation{"op": {PerCaller: Limit{Rate: 1, Burst: 1}}}}, 2},
		{"invalid operation caller", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"x": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}, Operations: map[string]Operation{"op": {Shared: Limit{Rate: 1, Burst: 1}}}}, 2},
		{"too many active states", Policy{Application: Limit{Rate: 1, Burst: 1}, Classes: map[string]Class{"a": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}, "b": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}, Operations: map[string]Operation{"op": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}}}}, maxCallerSlots},
		{"caller state registry bound", testPolicy(), maxCallerSlots},
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

func TestAuthorizationVersionFencesAdmissions(t *testing.T) {
	now := time.Unix(100, 0)
	policy := testPolicy()
	policy.Operations = map[string]Operation{
		"export": {Shared: Limit{Rate: 2, Burst: 2}, PerCaller: Limit{Rate: 1, Burst: 1}},
	}
	c := testCoordinator(t, policy, 10, &now)
	initial := c.AuthorizationVersion()
	if got, err := c.AdmitForAuthorizationVersion("public", "alice", initial); err != nil || !got.Allowed {
		t.Fatalf("current version admission = %+v, %v", got, err)
	}
	if got, err := c.AdmitOperationForAuthorizationVersion("private", "export", "alice", initial); err != nil || !got.Allowed {
		t.Fatalf("current operation admission = %+v, %v", got, err)
	}
	if got, err := c.AdmitForAuthorizationVersion("public", "bob", initial-1); err != nil ||
		got.Allowed || got.Reason != ReasonStaleAuthorization {
		t.Fatalf("stale authorization admission = %+v, %v", got, err)
	}

	next, err := c.AdvanceAuthorization()
	if err != nil || next != initial+1 {
		t.Fatalf("advance authorization = %d, %v; want %d", next, err, initial+1)
	}
	if got, err := c.AdmitForAuthorizationVersion("public", "bob", next); err != nil || !got.Allowed {
		t.Fatalf("admission at advanced version = %+v, %v", got, err)
	}

	c.authorizationVersion.Store(^uint64(0))
	if _, err := c.AdvanceAuthorization(); err == nil || !strings.Contains(err.Error(), "version exhausted") {
		t.Fatalf("advance exhausted version error = %v", err)
	}
	if _, err := c.Reconfigure(policy, 10, true); err == nil || !strings.Contains(err.Error(), "version exhausted") {
		t.Fatalf("reconfigure exhausted version error = %v", err)
	}
}

func TestReconfigureChecksAndRestoresRetiredClassAndOperationState(t *testing.T) {
	now := time.Unix(100, 0)
	policy := testPolicy()
	policy.Operations = map[string]Operation{
		"export": {Shared: Limit{Rate: 2, Burst: 2}, PerCaller: Limit{Rate: 1, Burst: 1}},
	}
	c := testCoordinator(t, policy, 10, &now)
	if got, err := c.AdmitOperation("private", "export", "alice"); err != nil || !got.Allowed {
		t.Fatalf("initial operation admission = %+v, %v", got, err)
	}
	privateState := c.classes["private"]
	operationState := c.operations["export"]
	version := c.AuthorizationVersion()

	reduced := Policy{
		Application: policy.Application,
		Classes:     map[string]Class{"public": policy.Classes["public"]},
	}
	if err := c.CheckReconfigure(reduced, 10); err != nil {
		t.Fatalf("check reduced policy: %v", err)
	}
	if c.classes["private"] != privateState || c.operations["export"] != operationState ||
		c.AuthorizationVersion() != version {
		t.Fatal("CheckReconfigure mutated active state")
	}
	if got, err := c.Reconfigure(reduced, 10, true); err != nil || got != version+1 {
		t.Fatalf("remove class and operation = %d, %v", got, err)
	}
	if c.retiredClasses["private"] != privateState || c.retiredOperations["export"] != operationState {
		t.Fatal("depleted class or operation state was not retained")
	}
	if full, wait := c.RefillStatus(); full || wait <= 0 {
		t.Fatalf("depleted coordinator refill status = %v, %s", full, wait)
	}

	if _, err := c.Reconfigure(policy, 10, true); err != nil {
		t.Fatalf("restore retired class and operation: %v", err)
	}
	if c.classes["private"] != privateState || c.operations["export"] != operationState {
		t.Fatal("restoring a policy replaced its depleted class or operation state")
	}
	if _, ok := c.retiredClasses["private"]; ok {
		t.Fatal("restored class remains retired")
	}
	if _, ok := c.retiredOperations["export"]; ok {
		t.Fatal("restored operation remains retired")
	}

	now = now.Add(time.Second)
	if full, wait := c.RefillStatus(); !full || wait != 0 {
		t.Fatalf("refilled coordinator status = %v, %s", full, wait)
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

func TestOperationDenialDoesNotSpendOtherBudgets(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public":  {Shared: Limit{Rate: 5, Burst: 5}, PerCaller: Limit{Rate: 5, Burst: 5}},
			"trusted": {Shared: Limit{Rate: 5, Burst: 5}, PerCaller: Limit{Rate: 5, Burst: 5}},
		},
		Operations: map[string]Operation{
			"export": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 10, &now)
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || !got.Allowed {
		t.Fatalf("first operation admission = %+v, %v", got, err)
	}
	if got, err := c.AdmitOperation("trusted", "export", "bob"); err != nil || got.Allowed || got.Reason != ReasonOperation {
		t.Fatalf("shared operation ceiling = %+v, %v", got, err)
	}
	if got := admit(t, c, "trusted", "bob"); !got.Allowed {
		t.Fatalf("operation denial spent application/class/caller budgets: %+v", got)
	}
	if got, err := c.AdmitOperation("trusted", "unknown", "carol"); err == nil || got.Allowed {
		t.Fatalf("unknown operation did not fail explicitly: %+v, %v", got, err)
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

func TestDeniedAttemptsDoNotChangeRefill(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 1, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	control := testCoordinator(t, policy, 1, &now)
	attempted := testCoordinator(t, policy, 1, &now)
	for _, c := range []*Coordinator{control, attempted} {
		if !admit(t, c, "public", "alice").Allowed {
			t.Fatal("initial request denied")
		}
	}
	start := now
	for _, elapsed := range []time.Duration{10 * time.Millisecond, 420 * time.Millisecond} {
		now = start.Add(elapsed)
		if got := admit(t, attempted, "public", "alice"); got.Allowed {
			t.Fatalf("premature admission at %s: %+v", elapsed, got)
		}
	}
	now = start.Add(time.Second)
	for name, c := range map[string]*Coordinator{"control": control, "attempted": attempted} {
		if got := admit(t, c, "public", "alice"); !got.Allowed {
			t.Errorf("%s denied at the exact refill boundary: %+v", name, got)
		}
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

func TestFractionalRefillRetry(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 3, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 3, Burst: 1}, PerCaller: Limit{Rate: 3, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 1, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("first request denied")
	}
	now = now.Add(333_333_333 * time.Nanosecond)
	if got := admit(t, c, "public", "alice"); got.Allowed || got.RetryAfter != time.Nanosecond {
		t.Fatalf("fractional refill decision = %+v", got)
	}
	now = now.Add(time.Nanosecond)
	if got := admit(t, c, "public", "alice"); !got.Allowed {
		t.Fatalf("request denied after retry interval: %+v", got)
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

func TestCallerCapacityFindsRefilledEntryBeyondOldest(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 1, Burst: 2}},
		},
	}
	c := testCoordinator(t, policy, 2, &now)
	for i := 0; i < 2; i++ {
		if !admit(t, c, "public", "alice").Allowed {
			t.Fatal("initial alice request denied")
		}
	}
	now = now.Add(500 * time.Millisecond)
	if !admit(t, c, "public", "bob").Allowed {
		t.Fatal("initial bob request denied")
	}
	now = now.Add(time.Second)
	if got := admit(t, c, "public", "carol"); !got.Allowed {
		t.Fatalf("new caller denied despite a fully refilled candidate: %+v", got)
	}
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("partially refilled oldest caller lost its remaining token")
	}
	if got := admit(t, c, "public", "alice"); got.Allowed || got.Reason != ReasonCaller {
		t.Fatalf("partially refilled oldest caller was reset: %+v", got)
	}
}

func TestCallerCapacityDecreaseReclaimsEnoughRefilledEntries(t *testing.T) {
	now := time.Unix(100, 0)
	policy := testPolicy()
	policy.Operations = map[string]Operation{
		"export": {
			Shared:    Limit{Rate: 10, Burst: 10},
			PerCaller: Limit{Rate: 1, Burst: 1},
		},
	}
	c := testCoordinator(t, policy, 4, &now)
	for _, caller := range []string{"alice", "bob", "carol", "david"} {
		if got, err := c.AdmitOperation("public", "export", caller); err != nil || !got.Allowed {
			t.Fatalf("initial admission for %s = %+v, %v", caller, got, err)
		}
	}
	if _, err := c.Reconfigure(policy, 2, false); err != nil {
		t.Fatalf("lower caller cap: %v", err)
	}
	if got, err := c.AdmitOperation("public", "export", "erin"); err != nil ||
		got.Allowed || got.Reason != ReasonCallerCapacity {
		t.Fatalf("over-cap store admitted a caller without safe candidates: %+v, %v", got, err)
	}
	if len(c.classes["public"].callers) != 4 || len(c.operations["export"].callers) != 4 {
		t.Fatal("denied admission changed over-cap caller stores")
	}

	now = now.Add(time.Second)
	if got, err := c.AdmitOperation("public", "export", "erin"); err != nil || !got.Allowed {
		t.Fatalf("admission after buckets refilled = %+v, %v", got, err)
	}
	if len(c.classes["public"].callers) != 2 || len(c.operations["export"].callers) != 2 {
		t.Fatalf("caller stores after downsize admission = class:%d operation:%d, want 2 each",
			len(c.classes["public"].callers), len(c.operations["export"].callers))
	}
	if got, err := c.AdmitOperation("public", "export", "frank"); err != nil || !got.Allowed {
		t.Fatalf("admission at downsized capacity = %+v, %v", got, err)
	}
	if len(c.classes["public"].callers) != 2 || len(c.operations["export"].callers) != 2 {
		t.Fatalf("caller stores grew beyond downsized cap: class:%d operation:%d",
			len(c.classes["public"].callers), len(c.operations["export"].callers))
	}
}

func TestSharedDenialPreservesEvictionCandidate(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 1, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 2, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 1, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("initial request denied")
	}
	now = now.Add(500 * time.Millisecond)
	if got := admit(t, c, "public", "bob"); got.Allowed || got.Reason != ReasonApplication {
		t.Fatalf("expected shared-budget denial: %+v", got)
	}
	if len(c.classes["public"].callers) != 1 || c.classes["public"].order.Len() != 1 {
		t.Fatal("shared-budget denial changed the caller store")
	}
	if c.classes["public"].callers[sha256.Sum256([]byte("alice"))] == nil {
		t.Fatal("shared-budget denial removed the eviction candidate")
	}
	if got := admit(t, c, "public", "carol"); got.Allowed || got.Reason != ReasonApplication {
		t.Fatalf("refilled candidate lost after denied admission: %+v", got)
	}
	now = now.Add(500 * time.Millisecond)
	if got := admit(t, c, "public", "bob"); !got.Allowed {
		t.Fatalf("refilled candidate was not available for later admission: %+v", got)
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
	if _, err := c.Admit("public", strings.Repeat("x", maxCallerBytes+1)); err == nil {
		t.Fatal("oversized identity accepted")
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

func TestFullStoreScanDoesNotBlockAnotherClass(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 1, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("initial public request denied")
	}

	var calls atomic.Int32
	scanStarted := make(chan struct{})
	releaseScan := make(chan struct{})
	c.now = func() time.Time {
		if calls.Add(1) == 1 {
			close(scanStarted)
			<-releaseScan
		}
		return now
	}
	type result struct {
		decision Decision
		err      error
	}
	public := make(chan result, 1)
	go func() {
		decision, err := c.Admit("public", "bob")
		public <- result{decision, err}
	}()
	<-scanStarted
	protected := make(chan result, 1)
	go func() {
		decision, err := c.Admit("private", "carol")
		protected <- result{decision, err}
	}()
	select {
	case got := <-protected:
		if got.err != nil || !got.decision.Allowed {
			t.Errorf("protected admission: %+v, %v", got.decision, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Error("protected admission blocked by another class's scan")
	}
	close(releaseScan)
	if got := <-public; got.err != nil || got.decision.Reason != ReasonCallerCapacity {
		t.Errorf("public admission: %+v, %v", got.decision, got.err)
	}
}

func TestEvictionCandidateRecheckedWhenClockMovesBackwards(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	policy := Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 1, Burst: 2}},
		},
	}
	c := testCoordinator(t, policy, 2, &now)
	for i := 0; i < 2; i++ {
		if !admit(t, c, "public", "alice").Allowed {
			t.Fatal("initial alice request denied")
		}
	}
	now = start.Add(500 * time.Millisecond)
	if !admit(t, c, "public", "bob").Allowed {
		t.Fatal("initial bob request denied")
	}
	var calls int
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start.Add(2 * time.Second)
		}
		return start.Add(1500 * time.Millisecond)
	}
	if got := admit(t, c, "public", "carol"); !got.Allowed {
		t.Fatalf("new caller denied despite a safe candidate: %+v", got)
	}
	state := c.classes["public"]
	if state.callers[sha256.Sum256([]byte("alice"))] == nil {
		t.Fatal("partially refilled alice was evicted after clock moved backwards")
	}
	if state.callers[sha256.Sum256([]byte("bob"))] != nil {
		t.Fatal("fully refilled bob was not evicted")
	}
}

func TestEvictionDeniedWhenClockMovesBeforeFullRefill(t *testing.T) {
	start := time.Unix(100, 0)
	now := start
	c := testCoordinator(t, testPolicy(), 1, &now)
	if !admit(t, c, "public", "alice").Allowed {
		t.Fatal("initial request denied")
	}
	var calls int
	c.now = func() time.Time {
		calls++
		if calls == 1 {
			return start.Add(time.Second)
		}
		return start.Add(500 * time.Millisecond)
	}
	if got := admit(t, c, "public", "bob"); got.Allowed || got.Reason != ReasonCallerCapacity {
		t.Fatalf("partially refilled candidate was retired: %+v", got)
	}
	if c.classes["public"].callers[sha256.Sum256([]byte("alice"))] == nil {
		t.Fatal("caller state changed after capacity denial")
	}
}

func TestConcurrentAdmissionsAcrossClasses(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 100, &now)
	const attempts = 64
	start := make(chan struct{})
	type result struct {
		class    string
		decision Decision
	}
	results := make(chan result, attempts*2)
	var wg sync.WaitGroup
	for _, class := range []string{"public", "private"} {
		for i := 0; i < attempts; i++ {
			wg.Add(1)
			go func(class string, i int) {
				defer wg.Done()
				<-start
				decision, err := c.Admit(class, fmt.Sprint(i))
				if err != nil {
					t.Errorf("Admit: %v", err)
				}
				results <- result{class, decision}
			}(class, i)
		}
	}
	close(start)
	wg.Wait()
	close(results)
	counts := make(map[string]int)
	for result := range results {
		if result.decision.Allowed {
			counts[result.class]++
		}
	}
	if counts["public"] != 4 || counts["private"] != 6 {
		t.Fatalf("admitted across classes = %v, want public 4 and private 6", counts)
	}
}
