// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"testing"

	"jul/internal/config"
	"jul/internal/plugins"
	"jul/internal/server"
)

func TestFactoryPluginModuleChangeLedger(t *testing.T) {
	f := &HandlerFactory{}
	a := plugins.ModuleIdentity{Source: plugins.SourcePath, Digest: "sha256:aa"}
	b := plugins.ModuleIdentity{Source: plugins.SourcePath, Digest: "sha256:bb"}

	f.publishPluginModules(1, map[string]plugins.ModuleIdentity{"keep": a, "swap": a, "drop": a})
	if got := f.PluginModuleChanges(1); len(got) != 3 {
		t.Fatalf("initial publish changes = %+v, want three additions", got)
	}
	f.publishPluginModules(2, map[string]plugins.ModuleIdentity{"keep": a, "swap": b, "add": b})
	if got := f.PluginModuleChanges(1); got != nil {
		t.Fatalf("a stale generation must not claim another generation's changes: %+v", got)
	}
	want := []server.PluginModuleChange{
		{Name: "add", After: "sha256:bb"},
		{Name: "drop", Before: "sha256:aa"},
		{Name: "swap", Before: "sha256:aa", After: "sha256:bb"},
	}
	got := f.PluginModuleChanges(2)
	if len(got) != len(want) {
		t.Fatalf("changes = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("changes = %+v, want %+v", got, want)
		}
	}
	if again := f.PluginModuleChanges(2); again != nil {
		t.Fatalf("changes must be returned once, got %+v again", again)
	}

	live := f.PluginModules()
	live["keep"] = b
	if f.PluginModules()["keep"] != a {
		t.Fatal("PluginModules must return a copy")
	}
	if f.PluginModulesUnchanged(map[string]config.PluginConfig{"keep": {Path: "/nonexistent.wasm"}}) {
		t.Fatal("an unreadable module cannot prove a no-op")
	}
}
