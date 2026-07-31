// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"jul/internal/plugins"
	"jul/internal/stream"
	"jul/internal/waf"
)

// capabilitiesOutput is the stable JSON contract of `jul capabilities`.
// Fields are additive; existing keys are never renamed or removed.
type capabilitiesOutput struct {
	Product   string          `json:"product"`
	Version   string          `json:"version"`
	Features  capFeatureFlags `json:"features"`
	ExitCodes []capExitCode   `json:"exit_codes"`
}

// capFeatureFlags reports which optional features are compiled into this binary.
// Each flag corresponds to a Go build tag that must be specified at build time.
// False means the feature is absent from this binary and its config keys are
// rejected at preflight; true means it is compiled in and available.
//
// Every optional build tag is reported. waf/stream_proxy/wasm_plugins are read
// from their package-level Compiled constants; the remaining tags are detected
// by the tag-gated capabilities_tag_*.go files in this package.
type capFeatureFlags struct {
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
// capabilities_tag_*.go files: each sets its flag true only when its build tag
// is present at build time, so `jul capabilities` reports exactly what the
// binary supports.
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

// capExitCode is one row of the canonical exit-code table.
type capExitCode struct {
	Code    int    `json:"code"`
	Meaning string `json:"meaning"`
}

// exitCodes is the canonical exit-code contract for every jul subcommand.
// The same table is part of `jul capabilities --json` output and is referenced
// in docs/configuration.md.
var exitCodes = []capExitCode{
	{0, "success / clean shutdown / healthy probe"},
	{1, "error / validation failed / unhealthy probe / fmt would change the file"},
	{2, "usage or config error (bad flags, missing required argument, disabled admin), or lint -strict warnings"},
}

// cmdCapabilities reports which optional features are compiled into this binary
// and the canonical exit-code contract. Intended for CI pipelines, deployment
// automation, and diagnostic tooling that need to confirm what the binary
// supports before running a full server.
//
// Exit codes: 0 ok, 2 usage error.
func cmdCapabilities(args []string) int {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit output as JSON (default when stdout is not a TTY)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	out := capabilitiesOutput{
		Product: productName,
		Version: version,
		Features: capFeatureFlags{
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
		},
		ExitCodes: exitCodes,
	}

	if *jsonOut || !isTTY(stdout) {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}

	// Human-readable output for interactive use.
	fmt.Fprintf(stdout, "%s %s\n", out.Product, out.Version)
	fmt.Fprintf(stdout, "\ncompiled features:\n")
	for _, row := range []struct {
		key string
		val bool
	}{
		{"waf", out.Features.WAF},
		{"stream_proxy", out.Features.StreamProxy},
		{"wasm_plugins", out.Features.WASMPlugins},
		{"acme", out.Features.ACME},
		{"grpc", out.Features.GRPC},
		{"http3", out.Features.HTTP3},
		{"otel", out.Features.OTel},
		{"console", out.Features.Console},
		{"brotli", out.Features.Brotli},
		{"zstd", out.Features.Zstd},
		{"importer", out.Features.Importer},
		{"consul", out.Features.Consul},
		{"kubernetes", out.Features.Kubernetes},
	} {
		mark := "✓"
		if !row.val {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "  %s  %-20s\n", mark, row.key)
	}
	fmt.Fprintf(stdout, "\nexit codes:\n")
	for _, ec := range out.ExitCodes {
		fmt.Fprintf(stdout, "  %d  %s\n", ec.Code, ec.Meaning)
	}
	return 0
}
