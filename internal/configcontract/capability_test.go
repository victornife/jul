// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"testing"

	"jul/internal/config"
)

// TestCapabilityRegistryResolvesAgainstSchema proves every entry's PathPrefix
// names a real schema path, so the capability table cannot drift from the
// schema it annotates.
func TestCapabilityRegistryResolvesAgainstSchema(t *testing.T) {
	schema := map[string]bool{}
	for _, p := range config.SchemaPaths() {
		schema[p.Path] = true
	}
	for _, e := range CapabilityRegistry {
		if !schema[e.PathPrefix] {
			t.Errorf("capability %s: PathPrefix %q does not resolve against config.SchemaPaths()", e.Capability, e.PathPrefix)
		}
	}
}

// TestCapabilitiesForMatchesKnownPaths pins representative leaves to their
// required capability, and proves an ordinary core path requires none.
func TestCapabilitiesForMatchesKnownPaths(t *testing.T) {
	cases := []struct {
		path string
		want Capability
	}{
		{"waf.mode", CapWAF},
		{"servers.*.locations.*.waf.mode", CapWAF},
		{"plugins.*.path", CapWASM},
		{"servers.*.locations.*.plugins", CapWASM},
		{"stream.*.listen", CapStream},
		{"servers.*.locations.*.grpc", CapGRPC},
		{"servers.*.locations.*.grpc_transcode.max_message_size", CapGRPC},
		{"servers.*.http3.alt_svc_max_age", CapHTTP3},
		{"servers.*.tls.acme.ca", CapACME},
		{"admin.console", CapConsole},
		{"observability.tracing.sample_ratio", CapOTel},
		{"upstreams.*.discovery.consul.service", CapConsul},
		{"upstreams.*.discovery.kubernetes.namespace", CapKubernetes},
	}
	for _, tc := range cases {
		got := CapabilitiesFor(tc.path)
		found := false
		for _, c := range got {
			if c == tc.want {
				found = true
			}
		}
		if !found {
			t.Errorf("CapabilitiesFor(%q) = %v, want to include %q", tc.path, got, tc.want)
		}
	}

	for _, corePath := range []string{"global.log_level", "servers.*.listen", "cache.default_ttl"} {
		if got := CapabilitiesFor(corePath); len(got) != 0 {
			t.Errorf("CapabilitiesFor(%q) = %v, want none (core path)", corePath, got)
		}
	}
}

// TestCapabilityRegistryNoOverlapAmbiguity proves no schema leaf resolves to
// two different capabilities, which would make "required tag" ambiguous.
func TestCapabilityRegistryNoOverlapAmbiguity(t *testing.T) {
	for _, p := range config.SchemaLeaves() {
		caps := CapabilitiesFor(p.Path)
		seen := map[Capability]bool{}
		for _, c := range caps {
			if seen[c] {
				t.Errorf("%s: capability %q listed more than once", p.Path, c)
			}
			seen[c] = true
		}
		if len(seen) > 1 {
			t.Errorf("%s: resolves to more than one capability: %v", p.Path, caps)
		}
	}
}

// TestCapabilityBuildTagDistinguishesLogicalNameFromRealTag proves the two
// display names cmd/jul's capabilities output uses that differ from their
// actual Go build tag are represented correctly, and that every path-level
// and value-level capability has a registered build tag.
func TestCapabilityBuildTagDistinguishesLogicalNameFromRealTag(t *testing.T) {
	cases := []struct {
		cap  Capability
		want string
	}{
		{CapStream, "stream"},
		{CapWASM, "wasmplugins"},
		{CapWAF, "waf"},
		{CapBrotli, "brotli"},
		{CapZstd, "zstd"},
	}
	for _, tc := range cases {
		got, ok := CapabilityBuildTag[tc.cap]
		if !ok {
			t.Errorf("capability %q has no registered build tag", tc.cap)
			continue
		}
		if got != tc.want {
			t.Errorf("CapabilityBuildTag[%q] = %q, want %q", tc.cap, got, tc.want)
		}
	}
	if CapabilityBuildTag[CapStream] == string(CapStream) {
		t.Error("CapStream's logical name and real build tag should differ (stream_proxy vs stream)")
	}
	if CapabilityBuildTag[CapWASM] == string(CapWASM) {
		t.Error("CapWASM's logical name and real build tag should differ (wasm_plugins vs wasmplugins)")
	}

	for _, e := range CapabilityRegistry {
		if _, ok := CapabilityBuildTag[e.Capability]; !ok {
			t.Errorf("path-level capability %q has no registered build tag", e.Capability)
		}
	}
	for _, e := range ValueCapabilityRegistry {
		if _, ok := CapabilityBuildTag[e.Capability]; !ok {
			t.Errorf("value-level capability %q has no registered build tag", e.Capability)
		}
	}
}

// TestValueCapabilityRegistryResolvesAgainstSchema proves every entry's Path
// names a real schema leaf.
func TestValueCapabilityRegistryResolvesAgainstSchema(t *testing.T) {
	leaves := map[string]bool{}
	for _, p := range config.SchemaLeaves() {
		leaves[p.Path] = true
	}
	for _, e := range ValueCapabilityRegistry {
		if !leaves[e.Path] {
			t.Errorf("value-capability %s: path %q does not resolve against config.SchemaLeaves()", e.Capability, e.Path)
		}
	}
}

// TestValueCapabilitiesForCompressionEncoders pins the brotli/zstd
// value-dependent requirement: "gzip" requires nothing, "br" requires
// brotli, "zstd" requires zstd.
func TestValueCapabilitiesForCompressionEncoders(t *testing.T) {
	got := ValueCapabilitiesFor("compression.encoders")
	if got["br"] != CapBrotli {
		t.Errorf(`ValueCapabilitiesFor("compression.encoders")["br"] = %q, want %q`, got["br"], CapBrotli)
	}
	if got["zstd"] != CapZstd {
		t.Errorf(`ValueCapabilitiesFor("compression.encoders")["zstd"] = %q, want %q`, got["zstd"], CapZstd)
	}
	if _, ok := got["gzip"]; ok {
		t.Error(`"gzip" should require no capability`)
	}
	if got := ValueCapabilitiesFor("global.log_level"); got != nil {
		t.Errorf("an ordinary path should have no value capabilities, got %v", got)
	}
}
