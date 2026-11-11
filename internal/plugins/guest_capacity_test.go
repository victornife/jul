// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import "testing"

func TestGuestCapacityStatsIncludesLiveGenerations(t *testing.T) {
	first := &plugin{name: "auth", memoryLimitBytes: 16 << 20, slots: make(chan struct{}, 2)}
	first.slots <- struct{}{}
	second := &plugin{name: "auth", memoryLimitBytes: 8 << 20, slots: make(chan struct{}, 3)}
	second.slots <- struct{}{}
	second.slots <- struct{}{}
	manager := &Manager{live: map[*plugin]struct{}{first: {}, second: {}}}

	got, err := manager.GuestCapacityStats()
	if err != nil {
		t.Fatalf("GuestCapacityStats: %v", err)
	}
	if got.MaxPermittedBytes != 56<<20 || got.LiveInstances != 3 || got.MaxInstances != 5 {
		t.Fatalf("GuestCapacityStats = %+v, want 56 MiB permitted, 3 live and 5 maximum instances", got)
	}
}

func TestGuestCapacityStatsRejectsOverflow(t *testing.T) {
	p := &plugin{name: "large", memoryLimitBytes: ^uint64(0)/2 + 1, slots: make(chan struct{}, 2)}
	manager := &Manager{live: map[*plugin]struct{}{p: {}}}

	if _, err := manager.GuestCapacityStats(); err == nil {
		t.Fatal("GuestCapacityStats accepted an overflowing capacity")
	}
}
