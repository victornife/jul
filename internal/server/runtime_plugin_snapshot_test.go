// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"jul/internal/config"
)

func TestLiveSnapshotPluginIdentityBelongsToGeneration(t *testing.T) {
	s := &Server{}
	a, b := &config.Config{}, &config.Config{}
	s.runtimeState.Store(&runtimeState{EffectiveConfig: a, Generation: 1, PluginModules: map[string]string{"page": "digest-A"}})
	first := s.LiveSnapshot()
	first.PluginModules["page"] = "tampered"
	if got := s.LiveSnapshot(); got.PluginModules["page"] != "digest-A" || got.EffectiveConfig != a || got.Generation != 1 {
		t.Fatalf("snapshot copy changed live identity: %+v", got)
	}
	s.runtimeState.Store(&runtimeState{EffectiveConfig: b, Generation: 2, PluginModules: map[string]string{"page": "digest-B"}})
	if got := s.LiveSnapshot(); got.PluginModules["page"] != "digest-B" || got.EffectiveConfig != b || got.Generation != 2 {
		t.Fatalf("new snapshot mixed generations: %+v", got)
	}
	if first.EffectiveConfig != a || first.Generation != 1 {
		t.Fatal("later publication mutated captured baseline")
	}
}
