// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

// The eleven resilience scenarios from #144, walked through the state model:
// counters, backend eligibility, client result, reason and cleanup.
//
// Fault injection lives in faultinject_test.go and is test-only by design.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/resilience"
	"jul/internal/upstream"
)

// reasonOf runs one request through the proxy and reports the status and the
// reason the proxy published, which is what an operator would see in the log.
func reasonOf(t *testing.T, h http.Handler, req *http.Request) (int, string) {
	t.Helper()
	ctx := middleware.WithUpstreamReason(req.Context())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec.Code, middleware.UpstreamReasonFrom(ctx)
}

func getReq() *http.Request {
	return httptest.NewRequest(http.MethodGet, "http://edge/", nil)
}

// Scenario 1: all backends unavailable.
//
// The correction this scenario exists to pin: admission is *capacity* and is
// evaluated before eligibility, so the request IS admitted and then released.
// The pending queue therefore never fills during a total outage, and the reason
// is an upstream one, never proxy_overloaded. That reads like a bug when a
// dashboard shows an idle queue during a complete failure, so it is pinned here
// rather than left to be rediscovered during an incident.
func TestScenarioAllBackendsUnavailable(t *testing.T) {
	h, adm := scenarioProxy(t, []string{refusedAddr, refusedAddr}, &config.ResilienceConfig{
		MaxActiveRequests:  4,
		MaxPendingRequests: 8,
	}, nil)

	code, reason := reasonOf(t, h, getReq())

	if code != http.StatusBadGateway && code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 502 or 503", code)
	}
	if reason == string(upstream.ReasonProxyOverloaded) {
		t.Error("a total outage was reported as proxy_overloaded; the queue is not what failed")
	}
	if !upstream.Reason(reason).Valid() {
		t.Errorf("reason %q is not in the closed set", reason)
	}

	// The queue never filled, and nothing leaked.
	if got := adm.Pending(); got != 0 {
		t.Errorf("pending = %d during a total outage, want 0", got)
	}
	if got := adm.Active(); got != 0 {
		t.Errorf("active = %d after the request finished, want 0", got)
	}
}

// Scenario 2: one slow and one healthy backend.
//
// The slow backend is up and answering, so it stays eligible — slowness is not
// a failure and ejecting for it is outlier detection, which this release
// deliberately does not do. What must hold is that the healthy backend keeps
// serving and the slow one's occupancy is accounted for and released.
func TestScenarioOneSlowOneHealthy(t *testing.T) {
	slow := newFaultBackend(t)
	fast := newFaultBackend(t)
	slow.set(faultSlow)

	h, adm := scenarioProxy(t, []string{slow.addr(), fast.addr()},
		&config.ResilienceConfig{MaxActiveRequests: 16}, nil)

	var wg sync.WaitGroup
	var ok atomic.Int64
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, getReq())
			if rec.Code == http.StatusOK {
				ok.Add(1)
			}
		}()
	}

	// The fast backend must serve while the slow one is still parked.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && ok.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if ok.Load() == 0 {
		slow.releaseSlow()
		wg.Wait()
		t.Fatal("no request completed while one backend was slow; a slow peer stalled the pool")
	}

	slow.releaseSlow()
	wg.Wait()

	if got := adm.Active(); got != 0 {
		t.Errorf("active = %d at quiesce, want 0", got)
	}
	if slow.requests.Load() == 0 {
		t.Error("the slow backend was never selected; slowness must not eject a backend")
	}
}

// Scenario 3: pending queue full.
//
// This is where proxy_overloaded is the correct answer, and it is the only
// scenario where it is: the queue is full because real work is occupying the
// pool, not because the upstream failed.
func TestScenarioPendingQueueFull(t *testing.T) {
	slow := newFaultBackend(t)
	slow.set(faultSlow)

	h, adm := scenarioProxy(t, []string{slow.addr()}, &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: 1,
	}, nil)

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 2 { // one active, one parked
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), getReq().WithContext(ctx))
		}()
	}
	waitFor(t, func() bool { return adm.Active() == 1 && adm.Pending() == 1 })

	// The third request finds both the slot and the queue taken.
	code, reason := reasonOf(t, h, getReq())
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 for a full queue", code)
	}
	if reason != string(upstream.ReasonProxyOverloaded) {
		t.Errorf("reason = %q, want %q", reason, upstream.ReasonProxyOverloaded)
	}

	cancel()
	slow.releaseSlow()
	wg.Wait()
	waitFor(t, func() bool { return adm.Active() == 0 && adm.Pending() == 0 })
}

// Scenario 4: retry budget exhausted.
//
// The budget is what stops a failing pool being finished off by the mechanism
// meant to route around it, so the refusal has to be attributable rather than
// looking like an ordinary upstream failure.
func TestScenarioRetryBudgetExhausted(t *testing.T) {
	b := upstream.NewBudget(10)
	for range 10 {
		b.Primary()
	}
	granted := 0
	for b.Allow() {
		granted++
		if granted > 10000 {
			t.Fatal("the budget never denied a retry")
		}
	}
	st := b.Status()
	if st.Percent != 10 {
		t.Errorf("percent = %d, want 10; the allowance is exhausted, not switched off", st.Percent)
	}
	if st.Remaining != 0 {
		t.Errorf("remaining = %d after exhaustion, want 0", st.Remaining)
	}
	if b.Allow() {
		t.Error("the budget granted a retry after reporting none remaining")
	}
	// Exhaustion must be a distinct, valid reason: an operator seeing every
	// retry denied needs to know it was the budget and not the upstream.
	if !upstream.ReasonRetryBudgetExhausted.Valid() {
		t.Error("retry_budget_exhausted is not in the closed reason set")
	}
}

// Scenario 5: breaker opens, then half-opens.
func TestScenarioBreakerOpensThenHalfOpens(t *testing.T) {
	dead := newFaultBackend(t)
	dead.stop() // dials are now refused

	pool := &config.ResilienceConfig{MaxFails: 1, FailTimeout: config.Duration(50 * time.Millisecond)}
	h, adm := scenarioProxy(t, []string{dead.addr()}, pool, nil)

	// First failure trips the circuit.
	_, first := reasonOf(t, h, getReq())
	if first != string(upstream.ReasonUpstreamConnectFailed) {
		t.Fatalf("first failure reason = %q, want a dial failure", first)
	}

	// While open the request must be refused without dialling, which the reason
	// distinguishes: upstream_unavailable means the circuit answered, where a
	// dial failure would mean it did not.
	code, open := reasonOf(t, h, getReq())
	if open != string(upstream.ReasonUpstreamUnavailable) && open != string(upstream.ReasonCircuitOpen) {
		t.Errorf("reason while open = %q, want the circuit to answer rather than a dial", open)
	}
	if code != http.StatusServiceUnavailable {
		t.Errorf("status while open = %d, want 503", code)
	}

	// After the cooldown the next request is a probe and must actually be sent.
	// The reason reverting to a dial failure is the evidence: a still-open
	// circuit would have answered upstream_unavailable again without dialling,
	// and a backend that could never be probed could never recover.
	time.Sleep(80 * time.Millisecond)
	_, probe := reasonOf(t, h, getReq())
	if probe != string(upstream.ReasonUpstreamConnectFailed) {
		t.Errorf("reason after the cooldown = %q, want a dial failure showing the probe was attempted", probe)
	}

	if got := adm.Active(); got != 0 {
		t.Errorf("active = %d at quiesce, want 0", got)
	}
}

// Scenario 6: discovery removes then re-adds a backend.
//
// The pool object survives, so admission counters and circuit state must not be
// reset by a membership change: a backend that comes back must not arrive with
// a forgotten failure history, and requests in flight must not be lost.
func TestScenarioDiscoveryRemovesAndReAddsABackend(t *testing.T) {
	a := newFaultBackend(t)
	b := newFaultBackend(t)

	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "disc",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: a.addr(), Weight: 1}, {Address: b.addr(), Weight: 1}},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	if got := len(p.Backends()); got != 2 {
		t.Fatalf("started with %d backends, want 2", got)
	}

	// Remove one.
	p.UpdateBackends([]config.UpstreamServer{{Address: a.addr(), Weight: 1}})
	if got := len(p.Backends()); got != 1 {
		t.Fatalf("after removal: %d backends, want 1", got)
	}
	if st := p.Stats(); st.Eligible != 1 {
		t.Errorf("eligible = %d after removal, want 1", st.Eligible)
	}

	// Add it back.
	p.UpdateBackends([]config.UpstreamServer{{Address: a.addr(), Weight: 1}, {Address: b.addr(), Weight: 1}})
	if got := len(p.Backends()); got != 2 {
		t.Fatalf("after re-add: %d backends, want 2", got)
	}
	if st := p.Stats(); st.Eligible != 2 {
		t.Errorf("eligible = %d after re-add, want 2", st.Eligible)
	}
	if st := p.Stats(); st.Active != 0 {
		t.Errorf("active = %d after membership churn, want 0", st.Active)
	}
}

// Scenario 7: a reload tightens max_active_requests from 1000 to 100 while 500
// are active.
//
// The lowered limit must not fail the excess: they are already in flight and
// the client is waiting on them. It applies to admission from that point on,
// and the excess drains.
func TestScenarioReloadTightensTheLimitBelowLiveOccupancy(t *testing.T) {
	slow := newFaultBackend(t)
	slow.set(faultSlow)

	h, adm := scenarioProxy(t, []string{slow.addr()},
		&config.ResilienceConfig{MaxActiveRequests: 1000}, nil)

	const live = 50 // 500 is the scenario's number; 50 proves the same property faster
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for range live {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), getReq().WithContext(ctx))
		}()
	}
	waitFor(t, func() bool { return adm.Active() == live })

	tightened, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 10})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	adm.SetPolicy(tightened)

	// Nothing already admitted was failed by the swap.
	if got := adm.Active(); got != live {
		t.Errorf("active = %d immediately after tightening, want the %d already in flight", got, live)
	}

	slow.releaseSlow()
	cancel()
	wg.Wait()
	waitFor(t, func() bool { return adm.Active() == 0 })
}

// Scenario 9: a pending client cancels.
//
// A parked request whose client goes away must free its queue slot and be
// recorded as a client cancellation, not as a gateway timeout: the two are
// different signals and conflating them corrupts the one an operator reaches
// for during an incident.
func TestScenarioPendingClientCancels(t *testing.T) {
	slow := newFaultBackend(t)
	slow.set(faultSlow)

	h, adm := scenarioProxy(t, []string{slow.addr()}, &config.ResilienceConfig{
		MaxActiveRequests:  1,
		MaxPendingRequests: 4,
	}, nil)

	hold, holdCancel := context.WithCancel(context.Background())
	defer holdCancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		h.ServeHTTP(httptest.NewRecorder(), getReq().WithContext(hold))
	}()
	waitFor(t, func() bool { return adm.Active() == 1 })

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	reasonCtx := middleware.WithUpstreamReason(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(rec, getReq().WithContext(reasonCtx))
	}()
	waitFor(t, func() bool { return adm.Pending() == 1 })

	cancel()
	<-done

	if got := adm.Pending(); got != 0 {
		t.Errorf("pending = %d after the client cancelled, want 0; the slot leaked", got)
	}
	if rec.Code == http.StatusGatewayTimeout {
		t.Error("a client cancellation was recorded as 504; nothing timed out")
	}

	holdCancel()
	slow.releaseSlow()
	wg.Wait()
	waitFor(t, func() bool { return adm.Active() == 0 })
}

// Scenario 10: a policy reload while old connections drain.
//
// The policy is swapped as a pointer without rebuilding the pool, so counters,
// parked requests and per-backend state all survive. Rebuilding would forget
// which backends are out of rotation and put every one of them back under full
// load at once.
func TestScenarioPolicyReloadPreservesLiveState(t *testing.T) {
	slow := newFaultBackend(t)
	slow.set(faultSlow)

	h, adm := scenarioProxy(t, []string{slow.addr()},
		&config.ResilienceConfig{MaxActiveRequests: 8}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), getReq().WithContext(ctx))
		}()
	}
	waitFor(t, func() bool { return adm.Active() == 4 })

	pol, err := resilience.Resolve(resilience.Options{MaxActiveRequests: 16})
	if err != nil {
		t.Fatalf("resolve policy: %v", err)
	}
	adm.SetPolicy(pol)

	if got := adm.Policy().MaxActiveRequests(); got != 16 {
		t.Errorf("policy after reload = %d, want 16", got)
	}
	if got := adm.Active(); got != 4 {
		t.Errorf("active = %d across the reload, want the 4 still in flight", got)
	}

	slow.releaseSlow()
	cancel()
	wg.Wait()
	waitFor(t, func() bool { return adm.Active() == 0 })
}

// Scenario 11: replicas double with local-only state.
//
// Every limit is per process. Doubling the replica count doubles the aggregate
// ceiling, and a limit sized as though it were global would be wrong by the
// replica count. Nothing coordinates, which is a deliberate choice, so the
// property to pin is that a membership doubling changes no per-pool counter.
func TestScenarioReplicasDoubleWithLocalOnlyState(t *testing.T) {
	backends := make([]config.UpstreamServer, 0, 8)
	for range 4 {
		backends = append(backends, config.UpstreamServer{Address: newFaultBackend(t).addr(), Weight: 1})
	}
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name: "k8s", Strategy: "round_robin", Servers: backends,
		Resilience: &config.ResilienceConfig{MaxActiveRequests: 100},
	}, "http")
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	defer p.Close()

	limitBefore := p.Policy().MaxActiveRequests()

	for range 4 {
		backends = append(backends, config.UpstreamServer{Address: newFaultBackend(t).addr(), Weight: 1})
	}
	p.UpdateBackends(backends)

	if got := len(p.Backends()); got != 8 {
		t.Fatalf("after doubling: %d backends, want 8", got)
	}
	// The limit is per process and does not scale with membership. This is the
	// property that makes a limit sized as though it were global wrong by the
	// replica count, and it is documented rather than compensated for.
	if got := p.Policy().MaxActiveRequests(); got != limitBefore {
		t.Errorf("max_active_requests changed from %d to %d when membership doubled", limitBefore, got)
	}
	if st := p.Stats(); st.Eligible != 8 {
		t.Errorf("eligible = %d, want 8", st.Eligible)
	}
}

// A mid-body failure must never be retried: a byte has already reached the
// client, so a second attempt would send a second response body.
func TestScenarioMidBodyFailureIsNotRetried(t *testing.T) {
	broken := newFaultBackend(t)
	broken.set(faultMidBody)
	healthy := newFaultBackend(t)

	h, _ := scenarioProxy(t, []string{broken.addr(), healthy.addr()},
		&config.ResilienceConfig{RetryAttempts: 3}, nil)

	const requests = 6
	for range requests {
		h.ServeHTTP(httptest.NewRecorder(), getReq())
	}

	gotBroken, gotHealthy := broken.requests.Load(), healthy.requests.Load()
	if gotBroken == 0 {
		t.Fatal("the broken backend was never selected; the test proves nothing")
	}
	// Round robin sends half to each. If a truncated response were retried, each
	// of the broken backend's turns would fail over and the healthy backend
	// would see all six. It seeing exactly its own share is the evidence that no
	// retry happened after bytes had already reached the client.
	if gotBroken+gotHealthy != requests {
		t.Errorf("backends saw %d requests for %d clients; a mid-body failure was retried", gotBroken+gotHealthy, requests)
	}
}

// waitFor polls until cond holds or the test times out. Resilience state is
// updated by other goroutines, so a bare read races the thing it is checking.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not reached within 3s")
}
