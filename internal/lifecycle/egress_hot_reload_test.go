// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import "testing"

func TestEgressPolicyIsGenerationCorrectHotReload(t *testing.T) {
	for _, path := range []string{"egress.enabled", "egress.allow"} {
		entry, ok := Lookup(path)
		if !ok {
			t.Fatalf("Lookup(%q) missing", path)
		}
		if entry.Class != HotReloadClass {
			t.Fatalf("%s class = %s, want %s", path, entry.Class, HotReloadClass)
		}
		if entry.StartupConsumed {
			t.Fatalf("%s remains startup-consumed after #94 promotion", path)
		}
	}
}
