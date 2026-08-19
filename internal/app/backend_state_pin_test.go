/**
 * Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
 * SPDX-License-Identifier: agpl
 */

package app

import (
	"testing"

	"jul/internal/admin"
	"jul/internal/upstream"
)

// The admin package never imports the runtime: it receives backend state as a
// plain string through AdaptUpstreams. That keeps the console decoupled, but it
// also means the two sides can drift without the compiler noticing. This file
// is the seam. It lives in internal/app because that is the only package that
// imports both.

func TestAdminBackendStateLiteralMatchesTheUpstreamEnum(t *testing.T) {
	if admin.BackendStateAvailable != string(upstream.StateAvailable) {
		t.Fatalf("admin.BackendStateAvailable = %q, upstream.StateAvailable = %q; "+
			"the console would report every backend as out of rotation",
			admin.BackendStateAvailable, upstream.StateAvailable)
	}
}

// AdaptUpstreams must pass the enum through verbatim. If it ever normalised,
// title-cased or abbreviated the value, the pin above would still hold while
// the console silently stopped recognising any state.
func TestAdaptUpstreamsPassesEveryBackendStateThroughVerbatim(t *testing.T) {
	states := []upstream.BackendState{
		upstream.StateAvailable,
		upstream.StateCircuitOpen,
		upstream.StateCircuitHalfOpen,
		upstream.StateHealthUnhealthy,
		upstream.StateAtCapacity,
	}
	backends := make([]upstream.BackendStatus, 0, len(states))
	for i, st := range states {
		backends = append(backends, upstream.BackendStatus{
			Address: string(st) + ":80",
			Weight:  i + 1,
			State:   st,
		})
	}

	got := AdaptUpstreams([]upstream.PoolStatus{{Name: "api", Backends: backends}})
	if len(got) != 1 || len(got[0].Backends) != len(states) {
		t.Fatalf("AdaptUpstreams reshaped the pool: %+v", got)
	}
	for i, b := range got[0].Backends {
		if want := string(states[i]); b.State != want {
			t.Errorf("backend %d: state = %q, want %q", i, b.State, want)
		}
	}
}
