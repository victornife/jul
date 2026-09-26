// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// TestBuildPluginABIIsExplicit: the guided editor can never silently change a
// plugin's ABI (#430); only an explicit value does.
func TestBuildPluginABIIsExplicit(t *testing.T) {
	existing := config.PluginConfig{Path: "p.wasm", ABI: config.PluginABIV2}
	kept, _, err := buildPlugin(pluginDef{Path: "p.wasm", Type: "middleware"}, existing)
	if err != nil || kept.ABI != config.PluginABIV2 {
		t.Fatalf("omitted abi changed the ABI: %q %v", kept.ABI, err)
	}
	cleared, _, err := buildPlugin(pluginDef{Path: "p.wasm", ABI: strp("")}, existing)
	if err != nil || cleared.ABI != "" {
		t.Fatalf("explicit clear: %q %v", cleared.ABI, err)
	}
	set, _, err := buildPlugin(pluginDef{Path: "p.wasm", ABI: strp(" jul-abi/v2 ")}, config.PluginConfig{})
	if err != nil || set.ABI != config.PluginABIV2 {
		t.Fatalf("explicit set: %q %v", set.ABI, err)
	}
	if _, _, err := buildPlugin(pluginDef{Path: "p.wasm", ABI: strp("jul-abi/v3")}, existing); err == nil {
		t.Fatal("unknown abi accepted")
	}
}

func TestProjectPluginABI(t *testing.T) {
	c := pluginPatchConfig()
	c.Plugins["v2"] = config.PluginConfig{Path: "v2.wasm", ABI: config.PluginABIV2}
	c.Plugins["v2cap"] = config.PluginConfig{Path: "v2.wasm", ABI: config.PluginABIV2, MaxResponseBody: config.Size(1 << 20)}
	out := projectPlugins(c, true)
	attachPluginModules(&out, map[string]PluginModule{"v2": {Digest: digestA, ResponsePhase: true}, "inject": {Digest: digestA}})
	got := map[string]PluginProjection{}
	for _, p := range out.Plugins {
		got[p.Name] = p
	}
	if p := got["inject"]; p.ABI != config.PluginABIV1 || p.ResponsePhase || p.ResponseBodyMax != "" {
		t.Fatalf("v1 plugin shows v2 fields: %+v", p)
	}
	if p := got["v2"]; p.ABI != config.PluginABIV2 || !p.ResponsePhase || p.ResponseBodyMax != "8m" {
		t.Fatalf("v2 plugin: %+v", p)
	}
	if p := got["v2cap"]; p.ResponsePhase || p.ResponseBodyMax != "1m" {
		t.Fatalf("v2 plugin not serving / capped: %+v", p)
	}
}

func TestDiffPluginABIAndInvocations(t *testing.T) {
	var d ConfigDiff
	diffPluginFields("p", config.PluginConfig{Path: "p.wasm"}, config.PluginConfig{Path: "p.wasm", ABI: config.PluginABIV2, MaxInvocations: 5}, &d)
	var abi, inv bool
	for _, e := range allDiffEntries(d) {
		abi = abi || (e.Before == config.PluginABIV1 && e.After == config.PluginABIV2)
		inv = inv || (e.Before == "0" && e.After == "5")
	}
	if !abi || !inv {
		t.Fatalf("diff = %+v", d)
	}
	if len(d.Warnings) == 0 || !strings.Contains(d.Warnings[0], "jul-abi/v2") {
		t.Fatalf("moving to v2 must warn: %+v", d.Warnings)
	}
	var same ConfigDiff
	diffPluginFields("p", config.PluginConfig{ABI: ""}, config.PluginConfig{ABI: config.PluginABIV1}, &same)
	if len(allDiffEntries(same)) != 0 {
		t.Fatalf("unset and explicit v1 are the same ABI: %+v", same)
	}
}
