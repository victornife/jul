// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"io"
	"net/http"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/upstream"
)

func pluginNoopPlan(t *testing.T, plugins map[string]config.PluginConfig, hook func(map[string]config.PluginConfig) bool) *ReloadPlan {
	t.Helper()
	cfg := cfgWithReturn("127.0.0.1:1", http.StatusOK)
	cfg.Plugins = plugins
	candidate := normalizedCandidate(t, cfg)
	srv := &Server{cfg: candidate.Effective, rawCfg: candidate.Raw, listeners: make(map[string]*listenerEntry)}
	srv.runtimeState.Store(&runtimeState{EffectiveConfig: candidate.Effective, RawConfig: candidate.Raw, Listeners: map[string]BoundListenerInfo{}})
	srv.handlers.Store(&handlerGen{})
	srv.PluginModulesUnchanged = hook
	plan := srv.newReloadPlan(context.Background(), candidate.Raw, candidate, nil)
	if err := plan.AssessServingChange(); err != nil {
		t.Fatalf("AssessServingChange: %v", err)
	}
	return plan
}

// TestServingChangeUsesPluginModuleIdentity pins #429 in the semantic no-op
// proof: an unchanged declaration is a no-op only when the module bytes are
// proven unchanged, so same-path changed bytes always take the reload path.
func TestServingChangeUsesPluginModuleIdentity(t *testing.T) {
	path := map[string]config.PluginConfig{"p": {Path: "plugin.wasm"}}
	inline := map[string]config.PluginConfig{"p": {Inline: "AGFzbQEAAAA="}}

	var seen map[string]config.PluginConfig
	same := func(p map[string]config.PluginConfig) bool { seen = p; return true }
	if plan := pluginNoopPlan(t, path, same); !plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingEffectiveEqual {
		t.Fatalf("same bytes: assessment = %+v, want a real no-op", plan.ServingChange)
	}
	if seen["p"].Path != "plugin.wasm" {
		t.Fatalf("proof hook saw %+v, want the candidate plugins", seen)
	}

	changed := func(map[string]config.PluginConfig) bool { return false }
	if plan := pluginNoopPlan(t, path, changed); plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingRuntimeInputChanged {
		t.Fatalf("same path, changed bytes: assessment = %+v, want runtime_resource_changed", plan.ServingChange)
	}
	if plan := pluginNoopPlan(t, inline, changed); plan.ServingChange.NoChange {
		t.Fatalf("inline with failed proof: assessment = %+v, want reload", plan.ServingChange)
	}

	if plan := pluginNoopPlan(t, path, nil); plan.ServingChange.NoChange || plan.ServingChange.Evidence != ServingUnknownExternalInput {
		t.Fatalf("no proof hook, path module: assessment = %+v, want unknown_external_input", plan.ServingChange)
	}
	if plan := pluginNoopPlan(t, inline, nil); !plan.ServingChange.NoChange {
		t.Fatalf("no proof hook, inline module: assessment = %+v, want config-equality no-op", plan.ServingChange)
	}
	if plan := pluginNoopPlan(t, nil, changed); !plan.ServingChange.NoChange {
		t.Fatalf("no plugins: assessment = %+v, the proof hook must not be consulted", plan.ServingChange)
	}
}

// TestReloadResultReportsPluginModuleChanges proves a published reload carries
// the module identity changes of exactly its own generation.
func TestReloadResultReportsPluginModuleChanges(t *testing.T) {
	addr := freePort(t)
	factory := func(_ context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		m := map[string]http.Handler{}
		for _, srv := range c.Servers {
			m[srv.Listen] = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") })
		}
		return m, 7, func() (upstream.SnapshotMap, func()) { return nil, func() {} }, func() {}, nil
	}
	src := &stubSource{}
	src.set(cfgWith(addr), nil)
	srv := New(cfgWith(addr), nil, lifecycle.Fingerprint{}, quietLogger(), factory, src, func(context.Context, *config.Config) error { return nil })
	change := PluginModuleChange{Name: "p", Before: "sha256:aa", After: "sha256:bb"}
	var asked []uint64
	srv.PluginModuleChanges = func(genID uint64) []PluginModuleChange {
		asked = append(asked, genID)
		return []PluginModuleChange{change}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reload := make(chan ReloadRequest)
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	waitForServe(t, "http://"+addr+"/", "ok")

	src.set(cfgWithReturn(addr, http.StatusTeapot), nil)
	result := make(chan ReloadResult, 1)
	reload <- ReloadRequest{Source: ReloadSourceSIGHUP, Result: result}
	rr := <-result
	if !rr.Published || len(rr.PluginModules) != 1 || rr.PluginModules[0] != change {
		t.Fatalf("result = %+v", rr)
	}
	if len(asked) != 1 || asked[0] != 7 {
		t.Fatalf("changes requested for generations %v, want [7]", asked)
	}
	cancel()
	<-done
}
