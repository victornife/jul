// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"jul/internal/adminapi"
	"jul/internal/buildcaps"
)

// capabilitiesOutput is the stable JSON contract of `jul capabilities`.
type capabilitiesOutput struct {
	Product   string              `json:"product"`
	Version   string              `json:"version"`
	Features  buildcaps.Flags     `json:"features"`
	ExitCodes []adminapi.ExitCode `json:"exit_codes"`
}

// cmdCapabilities reports which optional features are compiled into this binary
// and the canonical ADR 0019 §33 exit-code contract.
func cmdCapabilities(args []string) int {
	fs := flag.NewFlagSet("capabilities", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit output as JSON (default when stdout is not a TTY)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	out := capabilitiesOutput{
		Product:   productName,
		Version:   version,
		Features:  buildcaps.Compiled(),
		ExitCodes: adminapi.ExitCodes(),
	}

	if *jsonOut || !isTTY(stdout) {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(out)
		return 0
	}

	fmt.Fprintf(stdout, "%s %s\n", out.Product, out.Version)
	fmt.Fprintf(stdout, "\ncompiled features:\n")
	for _, row := range out.Features.Named() {
		mark := "✓"
		if !row.Enabled {
			mark = "✗"
		}
		fmt.Fprintf(stdout, "  %s  %-20s\n", mark, row.Name)
	}
	fmt.Fprintf(stdout, "\nexit codes:\n")
	for _, ec := range out.ExitCodes {
		fmt.Fprintf(stdout, "  %d  %s\n", ec.Code, ec.Meaning)
	}
	return 0
}
