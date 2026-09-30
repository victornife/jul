// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"testing"

	"jul/internal/plugins"
)

func TestProjectPluginInstanceStats(t *testing.T) {
	got := projectPluginInstanceStats([]plugins.InstanceStats{
		{Plugin: "alpha", Live: 3},
		{Plugin: "beta", Live: 1},
	})
	if len(got) != 2 {
		t.Fatalf("stats length = %d, want 2", len(got))
	}
	if got[0].Plugin != "alpha" || got[0].Live != 3 {
		t.Errorf("first stats = %+v, want alpha with 3 live instances", got[0])
	}
	if got[1].Plugin != "beta" || got[1].Live != 1 {
		t.Errorf("second stats = %+v, want beta with 1 live instance", got[1])
	}
}
