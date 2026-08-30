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
