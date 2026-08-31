// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"strings"
	"testing"
)

// TestBootIDIsTheInstanceEmbeddedInApplyIDs is the property that makes
// `boot_id` usable: a client records it alongside the apply_id it is polling
// and treats a change as "my replay window is gone" (ADR 0019 §27.2). If the
// published boot_id were a second, independently minted identity, that
// correlation would be silently wrong — the client would compare a value that
// has nothing to do with the ids it holds.
func TestBootIDIsTheInstanceEmbeddedInApplyIDs(t *testing.T) {
	c := &ConfigApplyCoordinator{}

	boot := c.BootID()
	if boot == "" {
		t.Fatal("BootID is empty")
	}
	if len(boot) != 12 {
		t.Fatalf("BootID = %q (%d chars); the apply-id parser requires exactly 12 hex characters",
			boot, len(boot))
	}

	id := c.nextID()
	if !strings.HasPrefix(id, "rl_"+boot+"_") {
		t.Fatalf("apply id %q does not embed the published boot id %q", id, boot)
	}
}

// TestBootIDIsStableForTheProcess. It delimits the ledger, so a value that
// changed between two reads would tell a client its replay window had been
// discarded when nothing had happened.
func TestBootIDIsStableForTheProcess(t *testing.T) {
	c := &ConfigApplyCoordinator{}
	first := c.BootID()
	for range 5 {
		if got := c.BootID(); got != first {
			t.Fatalf("BootID changed within one process: %q then %q", first, got)
		}
	}
	// Two coordinators are two boot scopes, which is what makes a restart
	// detectable at all.
	if other := (&ConfigApplyCoordinator{}).BootID(); other == first {
		t.Fatal("two coordinators minted the same boot id; a restart would be undetectable")
	}
}
