// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package buildcaps reports which optional features are compiled into this
// binary.
//
// It exists because the same answer is needed in two places that cannot import
// each other: `jul capabilities` in cmd/jul, and GET /api/v1/capabilities in
// internal/admin. ADR 0019 §30 requires the two to agree — "so `jul
// capabilities` and the API agree" is the reason the endpoint publishes the
// table at all — and the only way to guarantee that is one source, not two
// tables someone keeps in step by hand.
//
// Three flags are read from package-level constants their own packages already
// export. The remaining ten are set by the tag-gated files in this package,
// each of which sets exactly one flag under exactly one build tag.
package buildcaps

import (
	"jul/internal/plugins"
	"jul/internal/stream"
	"jul/internal/waf"
)

// Flags reports the optional build tags compiled into this binary. False means
// the feature is absent and its configuration keys are rejected at preflight;
// true means it is compiled in and available.
//
// The JSON names are a published contract: they appear in `jul capabilities
// --json` and in GET /api/v1/capabilities. Fields are additive; existing keys
// are never renamed or removed.
type Flags struct {
	WAF         bool `json:"waf"`
	StreamProxy bool `json:"stream_proxy"`
	WASMPlugins bool `json:"wasm_plugins"`
	ACME        bool `json:"acme"`
	GRPC        bool `json:"grpc"`
	HTTP3       bool `json:"http3"`
	OTel        bool `json:"otel"`
	Console     bool `json:"console"`
	Brotli      bool `json:"brotli"`
	Zstd        bool `json:"zstd"`
	Importer    bool `json:"importer"`
	Consul      bool `json:"consul"`
	Kubernetes  bool `json:"kubernetes"`
}

// Optional build tags compiled into this binary, detected by the tag-gated
// tag_*.go files in this package: each sets its flag true only when its build
// tag is present at build time.
var (
	tagACME       bool
	tagGRPC       bool
	tagHTTP3      bool
	tagOTel       bool
	tagConsole    bool
	tagBrotli     bool
	tagZstd       bool
	tagImporter   bool
	tagConsul     bool
	tagKubernetes bool
)

// Compiled returns the flags for the running binary.
func Compiled() Flags {
	return Flags{
		WAF:         waf.Compiled,
		StreamProxy: stream.Compiled,
		WASMPlugins: plugins.Compiled,
		ACME:        tagACME,
		GRPC:        tagGRPC,
		HTTP3:       tagHTTP3,
		OTel:        tagOTel,
		Console:     tagConsole,
		Brotli:      tagBrotli,
		Zstd:        tagZstd,
		Importer:    tagImporter,
		Consul:      tagConsul,
		Kubernetes:  tagKubernetes,
	}
}

// Named returns the flags as name/value pairs in a fixed display order, so the
// CLI's human output and any other renderer iterate one list rather than
// repeating the field order and drifting from it.
func (f Flags) Named() []NamedFlag {
	return []NamedFlag{
		{"waf", f.WAF},
		{"stream_proxy", f.StreamProxy},
		{"wasm_plugins", f.WASMPlugins},
		{"acme", f.ACME},
		{"grpc", f.GRPC},
		{"http3", f.HTTP3},
		{"otel", f.OTel},
		{"console", f.Console},
		{"brotli", f.Brotli},
		{"zstd", f.Zstd},
		{"importer", f.Importer},
		{"consul", f.Consul},
		{"kubernetes", f.Kubernetes},
	}
}

// NamedFlag is one row of Named.
type NamedFlag struct {
	Name    string
	Enabled bool
}
