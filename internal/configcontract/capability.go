// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import "strings"

// Capability is a build-tag-gated optional feature, identified by the same
// name cmd/jul's `capabilities` output uses (e.g. "waf", "grpc").
type Capability string

// Capabilities lists every optional build tag known to the generator, in the
// same order as Makefile's FULL_TAGS / cmd/jul's capFeatureFlags.
const (
	CapWAF        Capability = "waf"
	CapStream     Capability = "stream_proxy"
	CapWASM       Capability = "wasm_plugins"
	CapACME       Capability = "acme"
	CapGRPC       Capability = "grpc"
	CapHTTP3      Capability = "http3"
	CapOTel       Capability = "otel"
	CapConsole    Capability = "console"
	CapConsul     Capability = "consul"
	CapKubernetes Capability = "kubernetes"
)

// CapabilityEntry statically associates a canonical schema path prefix with
// the build tag/capability its configuration surface requires. This is the
// tag-independent registry ADR 0019 §6/§30 asks for: it names the
// relationship "this path requires tag X" without depending on whether X is
// compiled into the generator binary that renders it — a lean generator and a
// fully tagged one must produce byte-identical capability metadata.
//
// Brotli, zstd and the NGINX importer are deliberately absent: brotli/zstd
// gate specific VALUES of compression.encoders (an enum member), not a
// distinct schema path, and the importer is a CLI-only feature with no
// configuration schema path at all — neither has a concrete field-level
// consumer, so per ADR 0019 §21 ("if a proposed field has no concrete
// consumer, leave it out") they are omitted rather than forced onto a path
// that does not represent them.
type CapabilityEntry struct {
	// PathPrefix is a canonical config.SchemaPaths() path. It matches a schema
	// path p when p == PathPrefix or p has PathPrefix+"." as a prefix, so one
	// entry covers a whole subtree (e.g. every field under "waf").
	PathPrefix string
	Capability Capability
}

// CapabilityRegistry is the closed, static table. Every entry's PathPrefix
// must resolve against config.SchemaPaths() (tested).
var CapabilityRegistry = []CapabilityEntry{
	{"waf", CapWAF},
	{"servers.*.locations.*.waf", CapWAF},

	{"plugins", CapWASM},
	{"servers.*.plugins", CapWASM},
	{"servers.*.locations.*.plugins", CapWASM},
	{"servers.*.locations.*.plugin", CapWASM},

	{"stream", CapStream},

	{"servers.*.locations.*.grpc", CapGRPC},
	{"servers.*.locations.*.grpc_transcode", CapGRPC},

	{"servers.*.http3", CapHTTP3},

	{"servers.*.tls.acme", CapACME},

	{"admin.console", CapConsole},

	{"observability.tracing", CapOTel},

	{"upstreams.*.discovery.consul", CapConsul},
	{"upstreams.*.discovery.kubernetes", CapKubernetes},
}

// CapabilitiesFor returns every capability required by path, matching by
// prefix as CapabilityEntry.PathPrefix documents. Most paths return nil (core,
// no build tag required).
func CapabilitiesFor(path string) []Capability {
	var out []Capability
	for _, e := range CapabilityRegistry {
		if path == e.PathPrefix || strings.HasPrefix(path, e.PathPrefix+".") {
			out = append(out, e.Capability)
		}
	}
	return out
}
