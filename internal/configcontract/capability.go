// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import "strings"

// Capability is a build-tag-gated optional feature, identified by the same
// name cmd/jul's `capabilities` output uses (e.g. "waf", "grpc"). It is a
// logical/display name and is not always identical to the actual Go build
// tag that compiles it in — see CapabilityBuildTag.
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
	CapBrotli     Capability = "brotli"
	CapZstd       Capability = "zstd"
)

// CapabilityBuildTag maps each logical Capability to the actual Go build tag
// that compiles it in (Makefile's FULL_TAGS). Most capability names already
// match their tag; `stream_proxy`/`wasm_plugins` are the two cmd/jul
// display names that differ from their real tags (`stream`/`wasmplugins`), so
// a consumer must not assume a capability's name IS the tag to pass to `go
// build -tags`.
var CapabilityBuildTag = map[Capability]string{
	CapWAF:        "waf",
	CapStream:     "stream",
	CapWASM:       "wasmplugins",
	CapACME:       "acme",
	CapGRPC:       "grpc",
	CapHTTP3:      "http3",
	CapOTel:       "otel",
	CapConsole:    "console",
	CapConsul:     "consul",
	CapKubernetes: "kubernetes",
	CapBrotli:     "brotli",
	CapZstd:       "zstd",
}

// CapabilityEntry statically associates a canonical schema path prefix with
// the build tag/capability its configuration surface requires. This is the
// tag-independent registry ADR 0019 §6/§30 asks for: it names the
// relationship "this path requires tag X" without depending on whether X is
// compiled into the generator binary that renders it — a lean generator and a
// fully tagged one must produce byte-identical capability metadata.
//
// Brotli, zstd and the NGINX importer are deliberately absent from this
// PATH-level table: brotli/zstd gate specific VALUES of compression.encoders
// (an enum member), not a distinct schema path — see ValueCapabilityRegistry
// for that value-dependent relationship — and the importer is a CLI-only
// feature with no configuration schema path at all, so neither has a concrete
// field-level consumer here (ADR 0019 §21: "if a proposed field has no
// concrete consumer, leave it out").
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

// ValueCapability statically associates one specific value of a schema
// leaf — not the whole field — with a required capability. This is the
// mechanism for a build-tag requirement that a field's presence does not
// gate but one of its accepted values does (compression.encoders' "br" and
// "zstd" members each require their own build tag; "gzip" requires none).
type ValueCapability struct {
	Path       string
	Value      string
	Capability Capability
}

// ValueCapabilityRegistry is the closed, static table of value-dependent
// capability requirements. Every entry's Path must resolve against
// config.SchemaLeaves() (tested), and Capability must be a real Capability.
var ValueCapabilityRegistry = []ValueCapability{
	{"compression.encoders", "br", CapBrotli},
	{"compression.encoders", "zstd", CapZstd},
}

// ValueCapabilitiesFor returns the value->capability requirements declared
// for path, or nil when none apply.
func ValueCapabilitiesFor(path string) map[string]Capability {
	var out map[string]Capability
	for _, e := range ValueCapabilityRegistry {
		if e.Path == path {
			if out == nil {
				out = map[string]Capability{}
			}
			out[e.Value] = e.Capability
		}
	}
	return out
}
