// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"regexp"
	"testing"
)

// bootScopedIDPattern matches the target format rl_<12 hex>_<sequence> with a
// canonical (no leading-zero) decimal sequence.
var bootScopedIDPattern = regexp.MustCompile(`^rl_[0-9a-f]{12}_[1-9][0-9]*$`)

// TestNextIDIsBootScopedAndMonotonic proves nextID emits the boot-scoped
// rl_<boot-id>_<sequence> format with a stable per-process boot id and a
// strictly increasing sequence (WS01 Slice 1, Step 1).
func TestNextIDIsBootScopedAndMonotonic(t *testing.T) {
	c := &ConfigApplyCoordinator{}

	first := c.nextID()
	if !bootScopedIDPattern.MatchString(first) {
		t.Fatalf("first ID %q does not match boot-scoped format", first)
	}

	instance := c.managedApplyInstanceID()
	if len(instance) != 12 {
		t.Fatalf("boot instance %q length = %d, want 12", instance, len(instance))
	}

	// The first sequence must be 1 (seq.Add(1) from zero).
	if want := "rl_" + instance + "_1"; first != want {
		t.Fatalf("first ID = %q, want %q", first, want)
	}

	second := c.nextID()
	if want := "rl_" + instance + "_2"; second != want {
		t.Fatalf("second ID = %q, want %q", second, want)
	}
}

// TestBootInstanceDiffersAcrossCoordinators proves each process/coordinator gets
// an independent boot id, preventing apply-ID reuse after a restart (defect 4).
func TestBootInstanceDiffersAcrossCoordinators(t *testing.T) {
	a := &ConfigApplyCoordinator{}
	b := &ConfigApplyCoordinator{}
	if a.managedApplyInstanceID() == b.managedApplyInstanceID() {
		t.Fatal("two coordinators produced the same boot instance id; restart reuse is not prevented")
	}
}

// TestNewManagedApplyInstanceIDFormat proves the generator returns 12 lowercase
// hex characters.
func TestNewManagedApplyInstanceIDFormat(t *testing.T) {
	id := newManagedApplyInstanceID()
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("instance id %q is not 12 lowercase hex characters", id)
	}
}

// TestParseReloadSeqAcceptsBootScopedAndLegacy proves the sequence guard's
// parser reads the trailing sequence from both the boot-scoped and legacy
// formats and rejects malformed input.
func TestParseReloadSeqAcceptsBootScopedAndLegacy(t *testing.T) {
	cases := []struct {
		id   string
		want uint64
	}{
		{"rl_9f01c0b451d2_42", 42},
		{"rl_000000000000_1", 1},
		{"rl_5", 5},    // legacy
		{"rl_0", 0},    // legacy zero
		{"", 0},        // not an ID
		{"nope", 0},    // wrong prefix
		{"rl_", 0},     // empty rest
		{"rl_x", 0},    // non-numeric legacy
		{"rl_ab_x", 0}, // non-numeric boot sequence
	}
	for _, tc := range cases {
		if got := parseReloadSeq(tc.id); got != tc.want {
			t.Errorf("parseReloadSeq(%q) = %d, want %d", tc.id, got, tc.want)
		}
	}
}
