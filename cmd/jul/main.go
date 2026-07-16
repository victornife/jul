// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Command jul is an NGINX-inspired HTTP edge server configured via TOML.
//
// The composition root (handler factory, per-reload handler tree, process-
// lifetime subsystem init, preflight gate, admin wiring) lives in
// internal/app so it can be unit-tested without a full process boot.
// See docs/architecture.md#composition-root-helpers and ADR-0007.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"jul/internal/app"
	"jul/internal/config"
	"jul/internal/signals"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "dev"

// productName is the commercial product name shown to users.
const productName = "Jul.IA"

func main() {
	// When launched by the Windows Service Control Manager, run under the
	// service protocol; otherwise fall through to normal foreground execution.
	if handled, code := runService(); handled {
		os.Exit(code)
	}
	os.Exit(run())
}

func run() int {
	// Subcommands (lint/fmt/run) are additive; when none matches we fall back to
	// the original flag-based behavior so existing invocations are unchanged.
	if handled, code := dispatchSubcommand(os.Args[1:]); handled {
		return code
	}

	var (
		configPath  string
		checkOnly   bool
		showVersion bool
	)
	flag.Usage = usage
	flag.StringVar(&configPath, "config", "server.toml", "path to the TOML configuration file")
	flag.BoolVar(&checkOnly, "check", false, "validate the configuration and exit")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		// Legacy flag: prefer `jul version` (see usage).
		fmt.Fprintln(os.Stderr, "Deprecation notice: `jul --version` is kept for compatibility; prefer `jul version`.")
		fmt.Printf("%s %s\n", productName, version)
		return 0
	}

	src := config.NewTOMLSource(configPath)
	cfg, err := src.Load()
	if err != nil {
		// A missing config file on a bare `jul` is the most common first-run
		// stumble; point the operator at zero-config mode and the docs instead of
		// only surfacing a raw open error.
		if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
			fmt.Fprintf(os.Stderr, "error: no configuration file at %q\n\n", configPath)
			fmt.Fprintln(os.Stderr, "Start without a config file using zero-config mode:")
			fmt.Fprintln(os.Stderr, "  jul run --serve .              # serve the current directory")
			fmt.Fprintln(os.Stderr, "  jul run --proxy http://:3000   # reverse-proxy a local app")
			fmt.Fprintln(os.Stderr, "\nOr create a server.toml and run `jul`. See `jul --help` and docs/getting-started.md.")
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := app.ValidateRuntimeConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration in %s:\n%v\n", src.Name(), err)
		return 1
	}
	if checkOnly {
		// Legacy flag: prefer `jul check` (see usage).
		fmt.Fprintln(os.Stderr, "Deprecation notice: `jul --check` is kept for compatibility; prefer `jul check`.")
		fmt.Printf("configuration %s is valid\n", src.Name())
		return 0
	}

	ctx, reloadSig, stop := signals.Listen(context.Background())
	defer stop()
	return app.Serve(ctx, reloadSig, src, cfg, productName, version)
}
