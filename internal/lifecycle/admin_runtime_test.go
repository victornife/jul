// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import "testing"

func TestAdminRuntimeLifecycleBoundary157(t *testing.T) {
	hot := []string{
		"admin.console",
		"admin.plugin_upload_enabled",
		"admin.plugin_upload_max_size",
		"admin.plugin_upload_dir",
	}
	for _, path := range hot {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("Lookup(%q) missing", path)
		}
		if e.Class != HotReloadClass || e.StartupConsumed {
			t.Fatalf("%s = class %s startup=%v, want hot/reload and startup=false", path, e.Class, e.StartupConsumed)
		}
	}

	for _, path := range []string{"admin.enabled", "admin.listen"} {
		e, ok := Lookup(path)
		if !ok {
			t.Fatalf("Lookup(%q) missing", path)
		}
		if e.Class != RestartRequiredClass || !e.StartupConsumed {
			t.Fatalf("%s = class %s startup=%v, want restart-required/startup=true", path, e.Class, e.StartupConsumed)
		}
	}
}

func TestAdminRuntimeLifecycleMixedCandidateStillRestartBound(t *testing.T) {
	// #157 promotes only the operational policy. A mixed change including the
	// structural admin listener fields must remain restart-bound, which is what
	// the startup fingerprint gate consumes before Publish.
	for _, path := range []string{"admin.enabled", "admin.listen"} {
		e, ok := Lookup(path)
		if !ok || !e.StartupConsumed || e.Class != RestartRequiredClass {
			t.Fatalf("mixed-candidate structural guard missing for %s: %#v ok=%v", path, e, ok)
		}
	}
	for _, path := range []string{"admin.console", "admin.plugin_upload_enabled", "admin.plugin_upload_max_size", "admin.plugin_upload_dir"} {
		e, ok := Lookup(path)
		if !ok || e.StartupConsumed || e.Class != HotReloadClass {
			t.Fatalf("mixed-candidate hot field incorrectly startup-bound for %s: %#v ok=%v", path, e, ok)
		}
	}
}
