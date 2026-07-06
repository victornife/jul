// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

// allDiffEntries flattens a ConfigDiff's additions, removals, and modifications
// into a single slice for assertions.
func allDiffEntries(d ConfigDiff) []DiffEntry {
	out := make([]DiffEntry, 0, len(d.Additions)+len(d.Removals)+len(d.Modifications))
	out = append(out, d.Additions...)
	out = append(out, d.Removals...)
	out = append(out, d.Modifications...)
	return out
}

// pluginPatchConfig returns a config with one middleware plugin and one handler
// plugin declared, plus a route, for exercising the Phase 4h plugin patch ops.
func pluginPatchConfig() *config.Config {
	return &config.Config{
		Plugins: map[string]config.PluginConfig{
			"inject": {Path: "header-inject.wasm", Type: "middleware"},
			"block":  {Path: "request-block.wasm", Type: "handler"},
		},
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
				ProxyPass: "http://old",
			}},
		}},
	}
}

func TestApplyPatchPluginSetAdd(t *testing.T) {
	c := pluginPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "audit",
		PluginDef:  &pluginDef{Source: "path", Path: "audit.wasm", Type: "middleware", KV: true},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got, ok := c.Plugins["audit"]
	if !ok {
		t.Fatal("plugin not added")
	}
	if got.Path != "audit.wasm" || got.Type != "middleware" || !got.KV {
		t.Errorf("unexpected plugin: %+v", got)
	}
	if !strings.Contains(summary, "added") {
		t.Errorf("summary = %q, want added", summary)
	}
}

func TestApplyPatchPluginSetUpdate(t *testing.T) {
	c := pluginPatchConfig()
	summary, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "inject",
		PluginDef:  &pluginDef{Source: "path", Path: "inject-v2.wasm", Type: "middleware"},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c.Plugins["inject"].Path != "inject-v2.wasm" {
		t.Errorf("path = %q, want inject-v2.wasm", c.Plugins["inject"].Path)
	}
	if !strings.Contains(summary, "updated") {
		t.Errorf("summary = %q, want updated", summary)
	}
}

func TestApplyPatchPluginSetPathRequired(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "broken",
		PluginDef:  &pluginDef{Source: "path"},
	}); err == nil {
		t.Error("expected error when a new plugin has no path")
	}
}

func TestApplyPatchPluginSetInlinePreserved(t *testing.T) {
	c := pluginPatchConfig()
	c.Plugins["embedded"] = config.PluginConfig{Inline: "QUJD", Type: "middleware"}
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "embedded",
		PluginDef:  &pluginDef{Source: "inline", Type: "middleware", KV: true},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := c.Plugins["embedded"]
	if got.Inline != "QUJD" {
		t.Errorf("inline bytes not preserved: %q", got.Inline)
	}
	if got.Path != "" {
		t.Errorf("path should stay empty for an inline plugin, got %q", got.Path)
	}
	if !got.KV {
		t.Error("metadata edit (kv) not applied")
	}
}

func TestApplyPatchPluginSetInlineRequiresExisting(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "ghost",
		PluginDef:  &pluginDef{Source: "inline", Type: "middleware"},
	}); err == nil {
		t.Error("expected error when keeping inline source on a new plugin")
	}
}

func TestApplyPatchPluginSetTypeValidation(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "weird",
		PluginDef:  &pluginDef{Source: "path", Path: "x.wasm", Type: "filter"},
	}); err == nil {
		t.Error("expected error for an invalid plugin type")
	}
}

func TestApplyPatchPluginSetFetchNeedsAllowlist(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "caller",
		PluginDef:  &pluginDef{Source: "path", Path: "caller.wasm", Fetch: true},
	}); err == nil {
		t.Error("expected error when fetch is enabled without allowed_hosts")
	}
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "caller",
		PluginDef:  &pluginDef{Source: "path", Path: "caller.wasm", Fetch: true, AllowedHosts: []string{"api.example"}},
	}); err != nil {
		t.Errorf("apply with allowlist: %v", err)
	}
}

func TestApplyPatchPluginSetParsesLimits(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "inject",
		PluginDef:  &pluginDef{Source: "path", Path: "inject.wasm", MemoryLimit: "16m", Timeout: "100ms"},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := c.Plugins["inject"]
	if got.MemoryLimit.Bytes() != 16*1024*1024 {
		t.Errorf("memory_limit = %d bytes", got.MemoryLimit.Bytes())
	}
	if got.Timeout.Std().String() != "100ms" {
		t.Errorf("timeout = %s", got.Timeout.Std())
	}
	// A malformed size must be rejected.
	if _, err := applyPatch(c, patchRequest{
		Op:         "plugin_set",
		PluginName: "inject",
		PluginDef:  &pluginDef{Source: "path", Path: "inject.wasm", MemoryLimit: "notasize"},
	}); err == nil {
		t.Error("expected error for a malformed memory_limit")
	}
}

func TestApplyPatchPluginRemoveRefusesWhenReferenced(t *testing.T) {
	c := pluginPatchConfig()
	c.Servers[0].Locations[0].Plugins = []string{"inject"}
	_, err := applyPatch(c, patchRequest{Op: "plugin_remove", PluginName: "inject"})
	if err == nil {
		t.Fatal("expected error removing a referenced plugin")
	}
	if !strings.Contains(err.Error(), "attached") {
		t.Errorf("error = %v, want mention of attachment", err)
	}
	if _, ok := c.Plugins["inject"]; !ok {
		t.Error("plugin was removed despite being referenced")
	}
}

func TestApplyPatchPluginRemoveRefusesHandlerReference(t *testing.T) {
	c := pluginPatchConfig()
	c.Servers[0].Locations[0].Plugin = "block"
	if _, err := applyPatch(c, patchRequest{Op: "plugin_remove", PluginName: "block"}); err == nil {
		t.Error("expected error removing a plugin still used as a handler")
	}
}

func TestApplyPatchPluginRemoveSuccess(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{Op: "plugin_remove", PluginName: "inject"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := c.Plugins["inject"]; ok {
		t.Error("plugin not removed")
	}
}

func TestApplyPatchPluginRemoveUnknown(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{Op: "plugin_remove", PluginName: "nope"}); err == nil {
		t.Error("expected error removing an unknown plugin")
	}
}

func TestApplyPatchLocationAttachPlugin(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_attach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "inject",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := c.Servers[0].Locations[0].Plugins
	if len(got) != 1 || got[0] != "inject" {
		t.Errorf("plugins chain = %v, want [inject]", got)
	}
}

func TestApplyPatchLocationAttachPluginUnknown(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_attach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "ghost",
	}); err == nil {
		t.Error("expected error attaching an undeclared plugin")
	}
}

func TestApplyPatchLocationAttachPluginRejectsHandler(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_attach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "block",
	}); err == nil {
		t.Error("expected error attaching a handler plugin to the middleware chain")
	}
}

func TestApplyPatchLocationAttachPluginRejectsDuplicate(t *testing.T) {
	c := pluginPatchConfig()
	c.Servers[0].Locations[0].Plugins = []string{"inject"}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_attach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "inject",
	}); err == nil {
		t.Error("expected error attaching an already-attached plugin")
	}
}

func TestApplyPatchLocationDetachPlugin(t *testing.T) {
	c := pluginPatchConfig()
	c.Servers[0].Locations[0].Plugins = []string{"inject", "audit"}
	if _, err := applyPatch(c, patchRequest{
		Op: "location_detach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "inject",
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := c.Servers[0].Locations[0].Plugins
	if len(got) != 1 || got[0] != "audit" {
		t.Errorf("plugins chain = %v, want [audit]", got)
	}
}

func TestApplyPatchLocationDetachPluginNotAttached(t *testing.T) {
	c := pluginPatchConfig()
	if _, err := applyPatch(c, patchRequest{
		Op: "location_detach_plugin", Listen: ":8080", MatchType: "prefix", Path: "/api", PluginName: "inject",
	}); err == nil {
		t.Error("expected error detaching a plugin that is not attached")
	}
}

func TestDiffGlobalPlugins(t *testing.T) {
	before := pluginPatchConfig()
	after := pluginPatchConfig()
	// Add a plugin, drop the handler, grant fetch on the survivor.
	after.Plugins["audit"] = config.PluginConfig{Path: "audit.wasm", Type: "middleware"}
	delete(after.Plugins, "block")
	inject := after.Plugins["inject"]
	inject.Fetch = true
	inject.AllowedHosts = []string{"api.example"}
	after.Plugins["inject"] = inject

	d := diffConfigs(before, after)
	var added, removed, fetchGranted bool
	for _, e := range allDiffEntries(d) {
		if e.Kind != "plugin" {
			continue
		}
		switch {
		case e.Name == "audit" && e.Detail == "Add plugin audit":
			added = true
		case e.Name == "block" && strings.HasPrefix(e.Detail, "Remove plugin"):
			removed = true
		case e.Name == "inject" && strings.Contains(e.Detail, "outbound fetch"):
			fetchGranted = true
		}
	}
	if !added || !removed || !fetchGranted {
		t.Errorf("plugin diff incomplete: added=%v removed=%v fetchGranted=%v", added, removed, fetchGranted)
	}
}

func TestDiffLocationPluginChain(t *testing.T) {
	before := pluginPatchConfig()
	after := pluginPatchConfig()
	after.Servers[0].Locations[0].Plugins = []string{"inject"}

	d := diffConfigs(before, after)
	var attached bool
	for _, e := range allDiffEntries(d) {
		if e.Kind == "plugin" && e.After == "inject" && strings.Contains(e.Detail, "Attach plugin inject") {
			attached = true
		}
	}
	if !attached {
		t.Error("attach of inject to the route was not diffed")
	}
}

func TestProjectPlugins(t *testing.T) {
	c := pluginPatchConfig()
	c.Servers[0].Locations[0].Plugins = []string{"inject"}
	c.Servers[0].Locations[0].Plugin = "block"
	var mem config.Size
	_ = mem.UnmarshalText([]byte("8m"))
	pc := c.Plugins["inject"]
	pc.MemoryLimit = mem
	c.Plugins["inject"] = pc

	proj := projectPlugins(c, true)
	if !proj.Compiled {
		t.Error("compiled flag not propagated")
	}
	if len(proj.Plugins) != 2 {
		t.Fatalf("got %d plugins, want 2", len(proj.Plugins))
	}
	// Sorted: block, inject.
	if proj.Plugins[0].Name != "block" || proj.Plugins[1].Name != "inject" {
		t.Fatalf("unexpected order: %s, %s", proj.Plugins[0].Name, proj.Plugins[1].Name)
	}
	inject := proj.Plugins[1]
	if inject.Source != "path" || inject.MemoryLimit != "8m" {
		t.Errorf("inject projection = %+v", inject)
	}
	if len(inject.Attachments) != 1 || inject.Attachments[0].Role != "middleware" || inject.Attachments[0].Path != "/api" {
		t.Errorf("inject attachments = %+v", inject.Attachments)
	}
	block := proj.Plugins[0]
	if len(block.Attachments) != 1 || block.Attachments[0].Role != "handler" {
		t.Errorf("block attachments = %+v", block.Attachments)
	}
}

func TestHandlePluginsEndpoint(t *testing.T) {
	cfg := pluginPatchConfig()
	srv := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:      func() (*config.Config, error) { return cfg, nil },
		PluginsCompiled: true,
	})
	req := httptest.NewRequest("GET", "/api/plugins", nil)
	rec := httptest.NewRecorder()
	srv.handlePlugins(rec, req)
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"compiled":true`) || !strings.Contains(body, `"inject"`) {
		t.Errorf("unexpected body: %s", body)
	}
}
