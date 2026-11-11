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

func TestGuestCapacityStatsAllowsEmptyAndNilManagers(t *testing.T) {
	for name, manager := range map[string]*Manager{
		"nil":   nil,
		"empty": {live: map[*plugin]struct{}{}},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := manager.GuestCapacityStats()
			if err != nil || got != (GuestCapacityStats{}) {
				t.Fatalf("GuestCapacityStats() = %+v, %v; want zero stats and no error", got, err)
			}
		})
	}
}

func TestGuestCapacityStatsRejectsAggregateOverflow(t *testing.T) {
	first := &plugin{name: "first", memoryLimitBytes: 1 << 63, slots: make(chan struct{}, 1)}
	second := &plugin{name: "second", memoryLimitBytes: 1 << 63, slots: make(chan struct{}, 1)}
	manager := &Manager{live: map[*plugin]struct{}{first: {}, second: {}}}

	if _, err := manager.GuestCapacityStats(); err == nil {
		t.Fatal("GuestCapacityStats accepted aggregate capacity overflow")
	}
}
