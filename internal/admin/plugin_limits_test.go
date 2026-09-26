// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

func intp(n int) *int { return &n }

// limitedPlugin sets every resource limit plugin_set does not edit directly.
func limitedPlugin() config.PluginConfig {
	return config.PluginConfig{
		Path:             "p.wasm",
		Type:             "middleware",
		MaxRequestBody:   config.Size(2 << 20),
		MaxResponseBody:  config.Size(4 << 20),
		FetchTimeout:     config.Duration(3 * time.Second),
		MaxFetchResponse: config.Size(512 << 10),
		KVMaxEntries:     77,
		KVMaxBytes:       config.Size(3 << 20),
		MaxInvocations:   250,
		SHA256:           strings.Repeat("e", 64),
	}
}

// TestBuildPluginPreservesOmittedLimits pins #462: an editor payload that
// does not know about a limit keeps the operator's value (and the pin).
func TestBuildPluginPreservesOmittedLimits(t *testing.T) {
	existing := limitedPlugin()
	got, _, err := buildPlugin(pluginDef{Source: "path", Path: "p.wasm", Type: "handler", Config: map[string]string{"k": "v"}}, existing)
	if err != nil {
		t.Fatal(err)
	}
	want := existing
	want.Type = "handler"
	want.Config = map[string]string{"k": "v"}
	if got.MaxRequestBody != want.MaxRequestBody || got.MaxResponseBody != want.MaxResponseBody ||
		got.FetchTimeout != want.FetchTimeout || got.MaxFetchResponse != want.MaxFetchResponse ||
		got.KVMaxEntries != want.KVMaxEntries || got.KVMaxBytes != want.KVMaxBytes ||
		got.MaxInvocations != want.MaxInvocations || got.SHA256 != want.SHA256 || got.Type != "handler" {
		t.Fatalf("limits not preserved:\n got %+v\nwant %+v", got, want)
	}
}

// TestBuildPluginLimitFields exercises explicit update and explicit clear for
// every preserved limit, one field at a time, so each is independently covered.
func TestBuildPluginLimitFields(t *testing.T) {
	cases := []struct {
		name  string
		set   func(*pluginDef)
		clear func(*pluginDef)
		value func(config.PluginConfig) int64
		want  int64
	}{
		{"max_request_body", func(d *pluginDef) { d.MaxRequestBody = strp("5m") }, func(d *pluginDef) { d.MaxRequestBody = strp("") },
			func(p config.PluginConfig) int64 { return p.MaxRequestBody.Bytes() }, 5 << 20},
		{"max_response_body", func(d *pluginDef) { d.MaxResponseBody = strp("6m") }, func(d *pluginDef) { d.MaxResponseBody = strp(" ") },
			func(p config.PluginConfig) int64 { return p.MaxResponseBody.Bytes() }, 6 << 20},
		{"fetch_timeout", func(d *pluginDef) { d.FetchTimeout = strp("9s") }, func(d *pluginDef) { d.FetchTimeout = strp("") },
			func(p config.PluginConfig) int64 { return int64(p.FetchTimeout.Std()) }, int64(9 * time.Second)},
		{"max_fetch_response", func(d *pluginDef) { d.MaxFetchResponse = strp("64k") }, func(d *pluginDef) { d.MaxFetchResponse = strp("") },
			func(p config.PluginConfig) int64 { return p.MaxFetchResponse.Bytes() }, 64 << 10},
		{"kv_max_entries", func(d *pluginDef) { d.KVMaxEntries = intp(9) }, func(d *pluginDef) { d.KVMaxEntries = intp(0) },
			func(p config.PluginConfig) int64 { return int64(p.KVMaxEntries) }, 9},
		{"kv_max_bytes", func(d *pluginDef) { d.KVMaxBytes = strp("2k") }, func(d *pluginDef) { d.KVMaxBytes = strp("") },
			func(p config.PluginConfig) int64 { return p.KVMaxBytes.Bytes() }, 2 << 10},
		{"max_invocations", func(d *pluginDef) { d.MaxInvocations = intp(10) }, func(d *pluginDef) { d.MaxInvocations = intp(0) },
			func(p config.PluginConfig) int64 { return int64(p.MaxInvocations) }, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			existing := limitedPlugin()
			def := pluginDef{Source: "path", Path: "p.wasm"}
			tc.set(&def)
			got, _, err := buildPlugin(def, existing)
			if err != nil || tc.value(got) != tc.want {
				t.Fatalf("explicit set: value %d err %v, want %d", tc.value(got), err, tc.want)
			}
			if got.SHA256 != existing.SHA256 {
				t.Fatal("setting a limit dropped the pin")
			}
			def = pluginDef{Source: "path", Path: "p.wasm"}
			tc.clear(&def)
			got, _, err = buildPlugin(def, existing)
			if err != nil || tc.value(got) != 0 {
				t.Fatalf("explicit clear: value %d err %v, want the default (0)", tc.value(got), err)
			}
		})
	}
}

func TestBuildPluginRejectsInvalidLimits(t *testing.T) {
	for name, def := range map[string]pluginDef{
		"size":           {MaxRequestBody: strp("lots")},
		"negative size":  {KVMaxBytes: strp("-1")},
		"duration":       {FetchTimeout: strp("soon")},
		"negative dur":   {FetchTimeout: strp("-1s")},
		"negative count": {MaxInvocations: intp(-1)},
		"negative kv":    {KVMaxEntries: intp(-2)},
		"response body":  {MaxResponseBody: strp("x")},
		"fetch response": {MaxFetchResponse: strp("x")},
	} {
		def.Source, def.Path = "path", "p.wasm"
		if _, _, err := buildPlugin(def, limitedPlugin()); err == nil {
			t.Errorf("%s: invalid limit accepted", name)
		}
	}
}

// TestPluginSetJSONRoundTrip drives the wire shape: an editor payload decoded
// from JSON without the limit keys keeps them; explicit keys update/clear.
func TestPluginSetJSONRoundTrip(t *testing.T) {
	c := pluginPatchConfig()
	c.Plugins["inject"] = limitedPlugin()
	decode := func(raw string) *pluginDef {
		var d pluginDef
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			t.Fatal(err)
		}
		return &d
	}
	if _, err := applyPatch(c, patchRequest{Op: "plugin_set", PluginName: "inject",
		PluginDef: decode(`{"source":"path","path":"p.wasm","type":"middleware","timeout":"50ms"}`)}); err != nil {
		t.Fatal(err)
	}
	got := c.Plugins["inject"]
	if got.MaxInvocations != 250 || got.KVMaxEntries != 77 || got.MaxResponseBody.Bytes() != 4<<20 || got.Timeout.Std() != 50*time.Millisecond {
		t.Fatalf("round trip lost limits: %+v", got)
	}
	if _, err := applyPatch(c, patchRequest{Op: "plugin_set", PluginName: "inject",
		PluginDef: decode(`{"source":"path","path":"p.wasm","max_invocations":0,"max_response_body":"1m"}`)}); err != nil {
		t.Fatal(err)
	}
	got = c.Plugins["inject"]
	if got.MaxInvocations != 0 || got.MaxResponseBody.Bytes() != 1<<20 || got.KVMaxEntries != 77 {
		t.Fatalf("explicit update/clear: %+v", got)
	}
}

func TestProjectPluginLimits(t *testing.T) {
	c := pluginPatchConfig()
	c.Plugins["inject"] = limitedPlugin()
	proj := projectPlugins(c, true)
	var inject, block *PluginProjection
	for i := range proj.Plugins {
		switch proj.Plugins[i].Name {
		case "inject":
			inject = &proj.Plugins[i]
		case "block":
			block = &proj.Plugins[i]
		}
	}
	if inject == nil || inject.Limits == nil {
		t.Fatal("configured limits not projected")
	}
	want := PluginLimits{MaxRequestBody: "2m", MaxResponseBody: "4m", FetchTimeout: "3s", MaxFetchResponse: "512k",
		KVMaxEntries: 77, KVMaxBytes: "3m", MaxInvocations: 250}
	if *inject.Limits != want {
		t.Fatalf("limits = %+v, want %+v", *inject.Limits, want)
	}
	if block == nil || block.Limits != nil {
		t.Fatalf("a plugin without explicit limits projected %+v", block)
	}
}
