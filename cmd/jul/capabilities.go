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
// False means the feature is absent from this binary and its config keys will
// be rejected at runtime; true means it is compiled in and available.
//
// Three features expose a runtime boolean today (waf, stream_proxy,
// wasm_plugins). Other optional tags (acme, grpc, http3, otel, console,
// brotli, zstd, importer, consul, kubernetes) are enforced at config-validation
// time rather than exposed as runtime booleans; they will be added here as
// runtime detection is implemented.
type capFeatureFlags struct {
	WAF         bool `json:"waf"`
	StreamProxy bool `json:"stream_proxy"`
	WASMPlugins bool `json:"wasm_plugins"`
}

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
	{2, "usage or config error (bad flags, missing required argument, disabled admin)"},
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
	fmt.Fprintf(stdout, "\ncompiled features (runtime-detectable):\n")
	for _, row := range []struct {
		key string
		val bool
	}{
		{"waf", out.Features.WAF},
		{"stream_proxy", out.Features.StreamProxy},
		{"wasm_plugins", out.Features.WASMPlugins},
	} {
		mark := "✓"
		if !row.val {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "  %s  %-20s\n", mark, row.key)
	}
	fmt.Fprintf(stdout, "\n  (other tags: acme grpc http3 otel console brotli zstd importer\n")
	fmt.Fprintf(stdout, "   consul kubernetes — enforced at config-validation time)\n")
	fmt.Fprintf(stdout, "\nexit codes:\n")
	for _, ec := range out.ExitCodes {
		fmt.Fprintf(stdout, "  %d  %s\n", ec.Code, ec.Meaning)
	}
	return 0
}
