// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import "testing"

func TestIssue99TracingLifecycleDispositions(t *testing.T) {
	t.Parallel()

	hot, ok := Lookup("observability.tracing.sample_ratio")
	if !ok {
		t.Fatal("sample_ratio lifecycle entry missing")
	}
	if hot.Class != HotReloadClass {
		t.Fatalf("sample_ratio class=%s, want hot_reload", hot.Class)
	}
	if hot.StartupConsumed {
		t.Fatal("sample_ratio must not remain startup-consumed after #99")
	}

	for _, path := range []string{
		"observability.tracing.enabled",
		"observability.tracing.endpoint",
		"observability.tracing.exporter",
		"observability.tracing.insecure",
		"observability.tracing.service_name",
	} {
		t.Run(path, func(t *testing.T) {
			entry, ok := Lookup(path)
			if !ok {
				t.Fatalf("%s lifecycle entry missing", path)
			}
			if entry.Class != RestartRequiredClass {
				t.Fatalf("%s class=%s, want restart_required", path, entry.Class)
			}
			if !entry.StartupConsumed {
				t.Fatalf("%s must remain startup-consumed", path)
			}
		})
	}
}
