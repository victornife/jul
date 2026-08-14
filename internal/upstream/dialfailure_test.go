// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"

	"jul/internal/config"
)

func TestClassifyDialError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no backend", ErrNoAvailableBackend, "no_backend"},
		{"wrapped no backend", errors.New("dial: " + ErrNoAvailableBackend.Error()), "other"},
		{"context deadline", context.DeadlineExceeded, "timeout"},
		{"net timeout", &net.OpError{Op: "dial", Err: fakeTimeoutError{}}, "timeout"},
		{"connection refused", &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, "refused"},
		{"other", errors.New("boom"), "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyDialError(tc.err); got != tc.want {
				t.Errorf("ClassifyDialError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

type fakeTimeoutError struct{}

func (fakeTimeoutError) Error() string   { return "fake timeout" }
func (fakeTimeoutError) Timeout() bool   { return true }
func (fakeTimeoutError) Temporary() bool { return true }

// TestMarkFailureReportsTripOnlyOnce pins that MarkFailure reports the trip
// exactly on the call that crosses maxFails, not on every failure, so a
// caller logging only on that return value gets one line per cooldown cycle
// regardless of maxFails (issue #275).
func TestMarkFailureReportsTripOnlyOnce(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.maxFails = 3
	b := p.Backends()[0]

	if p.MarkFailure(b) {
		t.Error("failure 1/3 reported a trip")
	}
	if p.MarkFailure(b) {
		t.Error("failure 2/3 reported a trip")
	}
	if !p.MarkFailure(b) {
		t.Error("failure 3/3 did not report a trip")
	}
	if b.Available() {
		t.Error("backend available immediately after tripping")
	}
}

// TestMarkSuccessReportsRecoveryOnlyWhenDown pins that MarkSuccess only
// reports true when clearing an actual cooldown, so ordinary successes on a
// healthy backend do not produce a "recovered" line.
func TestMarkSuccessReportsRecoveryOnlyWhenDown(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.maxFails = 1
	b := p.Backends()[0]

	if p.MarkSuccess(b) {
		t.Error("success on a never-failed backend reported a recovery")
	}
	if !p.MarkFailure(b) {
		t.Fatal("failure did not trip with maxFails=1")
	}
	if !p.MarkSuccess(b) {
		t.Error("success clearing a tripped backend did not report a recovery")
	}
	if p.MarkSuccess(b) {
		t.Error("second success in a row reported a recovery again")
	}
}

// TestHealthHookFiresOnlyOnPassiveTransition pins that the passive-health hook
// (shared with the active checker's gauge/Console history) fires exactly on a
// trip or recovery, not on every failure/success, and is a no-op when unset.
func TestHealthHookFiresOnlyOnPassiveTransition(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	p.maxFails = 2
	b := p.Backends()[0]

	// Unset hook must not panic.
	p.MarkFailure(b)

	type event struct {
		pool, backend string
		healthy       bool
	}
	var events []event
	p.SetHealthHook(func(pool, backend string, healthy bool) {
		events = append(events, event{pool, backend, healthy})
	})

	p.MarkFailure(b) // 2nd consecutive failure: trips
	if len(events) != 1 || events[0].healthy {
		t.Fatalf("events after trip = %+v, want one down event", events)
	}
	p.MarkSuccess(b) // clears the cooldown: recovers
	if len(events) != 2 || !events[1].healthy {
		t.Fatalf("events after recovery = %+v, want a second, up event", events)
	}
	if events[0].pool != "test" || events[0].backend != "a:80" {
		t.Errorf("event identity = %+v, want pool=test backend=a:80", events[0])
	}
	p.MarkSuccess(b) // already healthy: no further event
	if len(events) != 2 {
		t.Fatalf("events after redundant success = %+v, want still 2", events)
	}
}

// TestAllowDialFailureLogThrottles pins the pool-level heartbeat throttle used
// by both the stream and HTTP dial-failure log sites: it admits the first call
// and suppresses one immediately after, deferring the interval behavior itself
// to internal/logthrottle's own tests.
func TestAllowDialFailureLogThrottles(t *testing.T) {
	p := pool(t, "round_robin", config.UpstreamServer{Address: "a:80", Weight: 1})
	if !p.AllowDialFailureLog() {
		t.Fatal("first call was suppressed")
	}
	if p.AllowDialFailureLog() {
		t.Error("second call inside the interval was admitted")
	}
}
