// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"net"
	"testing"
)

// TestDynamicConnLimiterDefensiveAccessors covers the deliberately defensive
// nil and negative-limit paths without changing the runtime contract. Negative
// limits normalize to the documented unlimited value, and nil receivers remain
// safe for optional listener wiring and cleanup paths.
func TestDynamicConnLimiterDefensiveAccessors(t *testing.T) {
	var nilLimiter *dynamicConnLimiter
	nilLimiter.SetLimit(5)
	if got := nilLimiter.Limit(); got != 0 {
		t.Fatalf("nil limiter limit=%d want 0", got)
	}
	if got := nilLimiter.Active(); got != 0 {
		t.Fatalf("nil limiter active=%d want 0", got)
	}
	if err := nilLimiter.Close(); err != nil {
		t.Fatalf("nil limiter close: %v", err)
	}

	raw, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	l := newDynamicConnLimiter(raw, -1)
	defer l.Close()
	if got := l.Limit(); got != 0 {
		t.Fatalf("negative constructor limit=%d want unlimited", got)
	}

	l.SetLimit(-7)
	if got := l.Limit(); got != 0 {
		t.Fatalf("negative live limit=%d want unlimited", got)
	}
}
