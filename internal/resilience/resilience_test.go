// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package resilience

import (
	"testing"
	"time"
)

// TestResolveEmptyOptionsSharesDefault pins the compatibility path and the
// allocation property that goes with it: an upstream with no resilience block
// must resolve to the shared unlimited policy rather than a fresh one.
func TestResolveEmptyOptionsSharesDefault(t *testing.T) {
	p, err := Resolve(Options{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if p != Default {
		t.Fatal("an empty block did not resolve to the shared default policy")
	}
	if p.Bounded() {
		t.Fatal("the default policy reports Bounded")
	}
	if p.MaxActiveRequests() != 0 || p.MaxActivePerBackend() != 0 || p.MaxPendingRequests() != 0 || p.PendingTimeout() != 0 {
		t.Fatalf("the default policy is not unlimited: %+v", p)
	}
}

func TestResolveAccepts(t *testing.T) {
	o := Options{
		MaxActiveRequests:   1000,
		MaxActivePerBackend: 250,
		MaxPendingRequests:  100,
		PendingTimeout:      2 * time.Second,
	}
	p, err := Resolve(o)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := p.MaxActiveRequests(); got != 1000 {
		t.Fatalf("MaxActiveRequests = %d, want 1000", got)
	}
	if got := p.MaxActivePerBackend(); got != 250 {
		t.Fatalf("MaxActivePerBackend = %d, want 250", got)
	}
	if got := p.MaxPendingRequests(); got != 100 {
		t.Fatalf("MaxPendingRequests = %d, want 100", got)
	}
	if got := p.PendingTimeout(); got != 2*time.Second {
		t.Fatalf("PendingTimeout = %s, want 2s", got)
	}
	if !p.Bounded() {
		t.Fatal("a policy with an admission limit does not report Bounded")
	}
}

func TestResolveRejects(t *testing.T) {
	cases := []struct {
		name string
		opts Options
	}{
		{"negative admission limit", Options{MaxActiveRequests: -1}},
		{"admission limit above ceiling", Options{MaxActiveRequests: MaxActiveRequestsCeiling + 1}},
		{"negative per-backend limit", Options{MaxActivePerBackend: -1}},
		{"per-backend limit above ceiling", Options{MaxActivePerBackend: MaxActivePerBackendCeiling + 1}},
		{"negative queue", Options{MaxPendingRequests: -1}},
		{"queue above ceiling", Options{MaxActiveRequests: 1, MaxPendingRequests: MaxPendingRequestsCeiling + 1}},
		{"negative pending timeout", Options{MaxActiveRequests: 1, MaxPendingRequests: 1, PendingTimeout: -time.Second}},
		{"pending timeout above ceiling", Options{MaxActiveRequests: 1, MaxPendingRequests: 1, PendingTimeout: PendingTimeoutCeiling + time.Second}},
		// A queue with no admission limit can never fill, because nothing ever
		// waits. Accepting it would mean shipping a control that silently does
		// nothing.
		{"queue without an admission limit", Options{MaxPendingRequests: 10}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Resolve(tc.opts); err == nil {
				t.Fatalf("Resolve accepted %+v", tc.opts)
			}
		})
	}
}

// TestPolicyBoundedIgnoresQueue pins that the queue alone does not make a policy
// bounded: the queue only exists behind an admission limit, so a consumer
// checking Bounded is asking about limits, not about waiting.
func TestPolicyBoundedIgnoresQueue(t *testing.T) {
	p, err := Resolve(Options{MaxActivePerBackend: 5})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !p.Bounded() {
		t.Fatal("a per-backend limit alone does not report Bounded")
	}
}
