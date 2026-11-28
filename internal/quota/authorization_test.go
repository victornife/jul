// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package quota

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthorizationChangeFencesPausedAdmission(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 1, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 2, &now)
	old := c.AuthorizationVersion()
	authenticated := make(chan struct{})
	release := make(chan struct{})
	var dispatched atomic.Int32

	handler := func(version uint64, paused bool) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if paused {
				close(authenticated)
				<-release
			}
			result, err := c.AdmitForAuthorizationVersion("public", "alice", version)
			if err != nil {
				t.Errorf("admit: %v", err)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			switch {
			case result.Reason == ReasonStaleAuthorization:
				w.WriteHeader(http.StatusServiceUnavailable)
			case !result.Allowed:
				w.WriteHeader(http.StatusTooManyRequests)
			default:
				dispatched.Add(1)
				w.WriteHeader(http.StatusOK)
			}
		})
	}

	oldResult := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		handler(old, true).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		oldResult <- rec.Code
	}()
	<-authenticated
	current, err := c.AdvanceAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := <-oldResult; got != http.StatusServiceUnavailable || dispatched.Load() != 0 {
		t.Fatalf("stale request status=%d dispatches=%d", got, dispatched.Load())
	}
	rec := httptest.NewRecorder()
	handler(current, false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK || dispatched.Load() != 1 {
		t.Fatalf("fresh request status=%d dispatches=%d", rec.Code, dispatched.Load())
	}
	rec = httptest.NewRecorder()
	handler(current, false).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("fresh request did not consume the existing budget: %d", rec.Code)
	}

	rollback, err := c.AdvanceAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	if rollback == old || rollback == current {
		t.Fatal("rollback reused an earlier authorization version")
	}
	if got, err := c.AdmitForAuthorizationVersion("public", "alice", old); err != nil || got.Reason != ReasonStaleAuthorization {
		t.Fatalf("old version became eligible again: %+v, %v", got, err)
	}
}

func TestAuthorizationChangeDuringCapacityScan(t *testing.T) {
	for _, tt := range []struct {
		name    string
		elapsed time.Duration
	}{
		{"no eviction candidate", 0},
		{"refilled eviction candidate", time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			now := time.Unix(100, 0)
			c := testCoordinator(t, testPolicy(), 1, &now)
			version := c.AuthorizationVersion()
			if got, err := c.AdmitForAuthorizationVersion("public", "alice", version); err != nil || !got.Allowed {
				t.Fatalf("initial admission: %+v, %v", got, err)
			}
			scanning := make(chan struct{})
			release := make(chan struct{})
			c.now = func() time.Time {
				close(scanning)
				<-release
				return now.Add(tt.elapsed)
			}
			result := make(chan Decision, 1)
			go func() {
				decision, err := c.AdmitForAuthorizationVersion("public", "bob", version)
				if err != nil {
					t.Errorf("admit: %v", err)
				}
				result <- decision
			}()
			<-scanning
			if _, err := c.AdvanceAuthorization(); err != nil {
				t.Fatal(err)
			}
			close(release)
			if got := <-result; got.Reason != ReasonStaleAuthorization {
				t.Fatalf("capacity scan returned an outdated decision: %+v", got)
			}
			callers := c.classes["public"].callers
			if len(callers) != 1 || callers[sha256.Sum256([]byte("alice"))] == nil {
				t.Fatal("stale admission changed caller state")
			}
		})
	}
}

func TestAuthorizationVersionExhaustionDoesNotWrap(t *testing.T) {
	now := time.Unix(100, 0)
	c := testCoordinator(t, testPolicy(), 1, &now)
	c.authorizationVersion.Store(^uint64(0))
	if _, err := c.AdvanceAuthorization(); err == nil {
		t.Fatal("version wrapped")
	}
	if got := c.AuthorizationVersion(); got != ^uint64(0) {
		t.Fatalf("version changed after exhaustion: %d", got)
	}
}

func TestReconfigurePreservesTokensAndClampsBurst(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 10, Burst: 10}},
		},
	}
	c := testCoordinator(t, policy, 1, &now)
	for i := 0; i < 10; i++ {
		if got, err := c.Admit("public", "alice"); err != nil || !got.Allowed {
			t.Fatalf("admission %d = %+v, %v", i, got, err)
		}
	}
	oldVersion := c.AuthorizationVersion()
	reduced := Policy{
		Application: Limit{Rate: 1, Burst: 10},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 10}, PerCaller: Limit{Rate: 1, Burst: 10}},
		},
	}
	version, err := c.Reconfigure(reduced, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if version != oldVersion {
		t.Fatalf("equivalent security policy changed version: %d != %d", version, oldVersion)
	}
	if got, err := c.Admit("public", "alice"); err != nil || got.Allowed || got.Reason != ReasonApplication || got.RetryAfter < 999*time.Millisecond {
		t.Fatalf("rate reduction minted refill state: %+v, %v", got, err)
	}

	now = now.Add(time.Second)
	clamped := Policy{
		Application: Limit{Rate: 1, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	if _, err := c.Reconfigure(clamped, 1, false); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Admit("public", "alice"); err != nil || !got.Allowed {
		t.Fatalf("reduced burst did not clamp usable state: %+v, %v", got, err)
	}
	if got, err := c.Admit("public", "alice"); err != nil || got.Allowed {
		t.Fatalf("reduced burst allowed more than one token: %+v, %v", got, err)
	}
}

func TestReconfigureEnforcesReducedCallerCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 10, Burst: 10},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 10, Burst: 10}},
		},
	}
	c := testCoordinator(t, policy, 4, &now)
	for _, caller := range []string{"alice", "bob"} {
		if got, err := c.Admit("public", caller); err != nil || !got.Allowed {
			t.Fatalf("admission for %s = %+v, %v", caller, got, err)
		}
	}
	if _, err := c.Reconfigure(policy, 1, false); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Admit("public", "carol"); err != nil || got.Reason != ReasonCallerCapacity {
		t.Fatalf("admission exceeded reduced caller capacity: %+v, %v", got, err)
	}
}

func TestRemovedClassRestorationPreservesRefillState(t *testing.T) {
	now := time.Unix(100, 0)
	base := Policy{
		Application: Limit{Rate: 10, Burst: 11},
		Classes: map[string]Class{
			"public":  {Shared: Limit{Rate: 9, Burst: 9}, PerCaller: Limit{Rate: 5, Burst: 5}},
			"account": {Shared: Limit{Rate: 1, Burst: 2}, PerCaller: Limit{Rate: 1, Burst: 2}},
		},
	}
	c := testCoordinator(t, base, 2, &now)
	for i := 0; i < 2; i++ {
		if got, err := c.Admit("account", "alice"); err != nil || !got.Allowed {
			t.Fatalf("initial account admission %d = %+v, %v", i, got, err)
		}
	}
	withoutAccount := Policy{
		Application: base.Application,
		Classes: map[string]Class{
			"public": base.Classes["public"],
		},
	}
	if _, err := c.Reconfigure(withoutAccount, 2, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	if _, err := c.Reconfigure(base, 2, true); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Admit("account", "alice"); err != nil || got.Allowed || got.Reason != ReasonClass {
		t.Fatalf("restored class minted tokens: %+v, %v", got, err)
	}
	now = now.Add(500 * time.Millisecond)
	if got, err := c.Admit("account", "alice"); err != nil || !got.Allowed {
		t.Fatalf("restored class did not accrue while absent: %+v, %v", got, err)
	}
}

func TestRemovedClassRestorationPreservesPerCallerBucket(t *testing.T) {
	now := time.Unix(100, 0)
	base := Policy{
		Application: Limit{Rate: 30, Burst: 30},
		Classes: map[string]Class{
			"public":  {Shared: Limit{Rate: 20, Burst: 20}, PerCaller: Limit{Rate: 20, Burst: 20}},
			"account": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	c := testCoordinator(t, base, 2, &now)
	if got, err := c.Admit("account", "alice"); err != nil || !got.Allowed {
		t.Fatalf("initial account admission = %+v, %v", got, err)
	}
	withoutAccount := Policy{
		Application: base.Application,
		Classes: map[string]Class{
			"public": base.Classes["public"],
		},
	}
	if _, err := c.Reconfigure(withoutAccount, 2, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	if _, err := c.Reconfigure(base, 2, true); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Admit("account", "alice"); err != nil || got.Allowed || got.Reason != ReasonCaller {
		t.Fatalf("restored class reset per-caller tokens: %+v, %v", got, err)
	}
	now = now.Add(500 * time.Millisecond)
	if got, err := c.Admit("account", "alice"); err != nil || !got.Allowed {
		t.Fatalf("restored caller bucket did not accrue while absent: %+v, %v", got, err)
	}
}

func TestRemovedOperationRestorationPreservesRefillState(t *testing.T) {
	now := time.Unix(100, 0)
	base := Policy{
		Application: Limit{Rate: 20, Burst: 20},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 10, Burst: 10}},
		},
		Operations: map[string]Operation{
			"export": {Shared: Limit{Rate: 1, Burst: 2}, PerCaller: Limit{Rate: 1, Burst: 2}},
		},
	}
	c := testCoordinator(t, base, 2, &now)
	for i := 0; i < 2; i++ {
		if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || !got.Allowed {
			t.Fatalf("initial export admission %d = %+v, %v", i, got, err)
		}
	}
	withoutExport := base
	withoutExport.Operations = nil
	if _, err := c.Reconfigure(withoutExport, 2, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	if _, err := c.Reconfigure(base, 2, true); err != nil {
		t.Fatal(err)
	}
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || got.Allowed || got.Reason != ReasonOperation {
		t.Fatalf("restored operation minted tokens: %+v, %v", got, err)
	}
	now = now.Add(500 * time.Millisecond)
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || !got.Allowed {
		t.Fatalf("restored operation did not accrue while absent: %+v, %v", got, err)
	}
}

func TestRemovedOperationRestorationPreservesPerCallerBucket(t *testing.T) {
	now := time.Unix(100, 0)
	base := Policy{
		Application: Limit{Rate: 20, Burst: 20},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 10, Burst: 10}},
		},
		Operations: map[string]Operation{
			"export": {Shared: Limit{Rate: 10, Burst: 10}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	c := testCoordinator(t, base, 2, &now)
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || !got.Allowed {
		t.Fatalf("initial export admission = %+v, %v", got, err)
	}
	withoutExport := base
	withoutExport.Operations = nil
	if _, err := c.Reconfigure(withoutExport, 2, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(500 * time.Millisecond)
	if _, err := c.Reconfigure(base, 2, true); err != nil {
		t.Fatal(err)
	}
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || got.Allowed || got.Reason != ReasonOperationCaller {
		t.Fatalf("restored operation reset per-caller tokens: %+v, %v", got, err)
	}
	now = now.Add(500 * time.Millisecond)
	if got, err := c.AdmitOperation("public", "export", "alice"); err != nil || !got.Allowed {
		t.Fatalf("restored operation caller bucket did not accrue while absent: %+v, %v", got, err)
	}
}

func TestReconfigureRejectsDepletedTombstonePressureAndEvictsFullState(t *testing.T) {
	now := time.Unix(100, 0)
	makePolicy := func(prefix string) Policy {
		classes := make(map[string]Class, 8)
		for i := 0; i < 8; i++ {
			classes[fmt.Sprintf("%s-%d", prefix, i)] = Class{
				Shared:    Limit{Rate: 10, Burst: 10},
				PerCaller: Limit{Rate: 1, Burst: 1},
			}
		}
		return Policy{Application: Limit{Rate: 80, Burst: 80}, Classes: classes}
	}
	first := makePolicy("first")
	c := testCoordinator(t, first, 1, &now)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("first-%d", i)
		if got, err := c.Admit(name, name); err != nil || !got.Allowed {
			t.Fatalf("deplete %s = %+v, %v", name, got, err)
		}
	}
	second := makePolicy("second")
	if _, err := c.Reconfigure(second, 1, true); err != nil {
		t.Fatal(err)
	}
	now = now.Add(100 * time.Millisecond)
	if got, err := c.Admit("second-0", "growth"); err != nil || !got.Allowed {
		t.Fatalf("deplete second-0 = %+v, %v", got, err)
	}
	if err := c.CheckReconfigure(makePolicy("third"), 1); err == nil || !strings.Contains(err.Error(), "retired class") {
		t.Fatalf("depleted tombstone pressure error = %v", err)
	}
	now = now.Add(time.Second)
	if err := c.CheckReconfigure(makePolicy("third"), 1); err != nil {
		t.Fatalf("fully refilled tombstones blocked the check: %v", err)
	}
	if got := len(c.retiredClasses); got != len(first.Classes) {
		t.Fatalf("check removed retired classes: got %d, want %d", got, len(first.Classes))
	}
	if _, err := c.Reconfigure(makePolicy("third"), 1, true); err != nil {
		t.Fatalf("reconfigure after safe eviction: %v", err)
	}
	if _, ok := c.retiredClasses["first-0"]; ok {
		t.Fatal("publication did not remove the refilled retired class")
	}
}

func TestReconfigureReclaimsFullyRefilledOutgoingClassAtRegistryBound(t *testing.T) {
	now := time.Unix(100, 0)
	makePolicy := func(prefix string) Policy {
		classes := make(map[string]Class, 8)
		for i := 0; i < 8; i++ {
			classes[fmt.Sprintf("%s-%d", prefix, i)] = Class{
				Shared:    Limit{Rate: 10, Burst: 10},
				PerCaller: Limit{Rate: 1, Burst: 1},
			}
		}
		return Policy{Application: Limit{Rate: 80, Burst: 80}, Classes: classes}
	}
	first := makePolicy("first")
	c := testCoordinator(t, first, 1, &now)
	for name := range first.Classes {
		if got, err := c.Admit(name, name); err != nil || !got.Allowed {
			t.Fatalf("deplete %s = %+v, %v", name, got, err)
		}
	}
	now = now.Add(100 * time.Millisecond)
	second := makePolicy("second")
	if _, err := c.Reconfigure(second, 1, false); err != nil {
		t.Fatalf("first replacement: %v", err)
	}
	candidate := makePolicy("second")
	delete(candidate.Classes, "second-0")
	candidate.Classes["third-0"] = Class{
		Shared:    Limit{Rate: 10, Burst: 10},
		PerCaller: Limit{Rate: 1, Burst: 1},
	}
	if err := c.CheckReconfigureAt(candidate, 1, now); err != nil {
		t.Fatalf("fully refilled outgoing class blocked exact-bound replacement: %v", err)
	}
	if _, err := c.Reconfigure(candidate, 1, false); err != nil {
		t.Fatalf("publish exact-bound replacement: %v", err)
	}
	if _, ok := c.retiredClasses["second-0"]; ok {
		t.Fatal("fully refilled outgoing class was retained as a tombstone")
	}
	if len(c.classes)+len(c.retiredClasses) != maxStoredClasses {
		t.Fatalf("stored class states = %d, want exact bound %d", len(c.classes)+len(c.retiredClasses), maxStoredClasses)
	}
}

func TestReconfigureRechecksCallerGrowthWhileAdmissionsArePaused(t *testing.T) {
	now := time.Unix(100, 0)
	makePolicy := func(prefix string) Policy {
		classes := make(map[string]Class, 8)
		for i := 0; i < 8; i++ {
			classes[fmt.Sprintf("%s-%d", prefix, i)] = Class{
				Shared:    Limit{Rate: 10, Burst: 10},
				PerCaller: Limit{Rate: 1, Burst: 1},
			}
		}
		return Policy{Application: Limit{Rate: 80, Burst: 80}, Classes: classes}
	}
	first := makePolicy("first")
	c := testCoordinator(t, first, 1, &now)
	for name := range first.Classes {
		if got, err := c.Admit(name, name); err != nil || !got.Allowed {
			t.Fatalf("deplete %s = %+v, %v", name, got, err)
		}
	}
	now = now.Add(100 * time.Millisecond)
	second := makePolicy("second")
	if _, err := c.Reconfigure(second, 1, false); err != nil {
		t.Fatalf("replace initial classes: %v", err)
	}
	candidate := makePolicy("third")
	if err := c.CheckReconfigureAt(candidate, 1, now); err != nil {
		t.Fatalf("candidate should fit before traffic: %v", err)
	}
	if got, err := c.Admit("second-0", "late-caller"); err != nil || !got.Allowed {
		t.Fatalf("late admission = %+v, %v", got, err)
	}

	release := c.PauseAdmissions()
	defer release()
	version := c.AuthorizationVersion()
	if err := c.CheckReconfigureAt(candidate, 1, now); err == nil || !strings.Contains(err.Error(), "class-state registry is full") {
		t.Fatalf("publication check after caller growth = %v", err)
	}
	if c.AuthorizationVersion() != version {
		t.Fatal("failed publication check advanced authorization version")
	}
	if _, active := c.classes["second-0"]; !active {
		t.Fatal("failed publication check changed active classes")
	}
	now = now.Add(time.Second)
	if err := c.CheckReconfigureAt(candidate, 1, now); err != nil {
		t.Fatalf("refilled outgoing class still blocks replacement: %v", err)
	}
	if _, err := c.ReconfigureWhilePaused(candidate, 1, true, now); err != nil {
		t.Fatalf("reconfigure after safe refill: %v", err)
	}
}

func TestSecurityReconfigureFencesOldVersionAtomically(t *testing.T) {
	now := time.Unix(100, 0)
	policy := Policy{
		Application: Limit{Rate: 1, Burst: 1},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		},
	}
	c := testCoordinator(t, policy, 1, &now)
	old := c.AuthorizationVersion()
	next := policy
	next.Classes = map[string]Class{
		"public":  {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
		"trusted": {Shared: Limit{Rate: 1, Burst: 1}, PerCaller: Limit{Rate: 1, Burst: 1}},
	}
	next.Application = Limit{Rate: 2, Burst: 2}
	if version, err := c.Reconfigure(next, 1, true); err != nil || version != old+1 {
		t.Fatalf("security reconfigure version=%d, err=%v", version, err)
	}
	if got, err := c.AdmitForAuthorizationVersion("public", "alice", old); err != nil || got.Reason != ReasonStaleAuthorization {
		t.Fatalf("old handler remained eligible: %+v, %v", got, err)
	}
	if got, err := c.AdmitForAuthorizationVersion("public", "alice", old+1); err != nil || !got.Allowed {
		t.Fatalf("new handler denied: %+v, %v", got, err)
	}
}
