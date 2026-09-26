// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

// Package plugins loads and runs sandboxed WebAssembly plugins on top of the
// wazero runtime (pure Go, no cgo, so the server stays a single static binary).
//
// A Manager is created once for the process and owns the shared compilation
// cache and the key/value store. On startup and on every reload that reaches
// Prepare the server calls Manager.Build with the current [plugins] config to produce a *Set: the
// compiled, instantiated plugins for that generation. A Set exposes each plugin
// as middleware (wraps a handler, may pass through) or as a terminal handler
// (the location's action). The previous generation's Set is closed after the new
// one is live, mirroring the generational teardown used for proxy pools and gRPC
// connections.
//
// # ABI seam
//
// Guests speak an ABI: a contract of host import functions and guest exports.
// jul-abi/v1 (host module "jul", export handle_request) is frozen. jul-abi/v2
// (host module "jul-abi/v2") keeps the v1 request surface and adds an opt-in,
// bounded response phase (handle_response); see docs/abi.md and ADR 0020. A
// plugin's configured abi selects the registrar, and negotiateABI verifies the
// module declares the same ABI before anything is instantiated.
package plugins

import (
	"context"

	"github.com/tetratelabs/wazero"

	"jul/internal/config"
)

// Compiled reports whether this build includes the WASM plugin runtime. It is
// true here (the "wasmplugins" build tag is set) and false in the stub build,
// letting callers detect a lean binary.
const Compiled = true

// ABI identifiers accepted by [plugins.NAME] abi.
const (
	// ABIJulV1 is the native Jul.IA ABI for Go/wasip1 guests.
	ABIJulV1 = config.PluginABIV1
	// ABIJulV2 adds the bounded response phase.
	ABIJulV2 = config.PluginABIV2
)

// Host import module names. v2 has its own module so the frozen v1 surface
// can never be bound by a v2 guest, or the reverse.
const (
	hostModuleV1 = "jul"
	hostModuleV2 = ABIJulV2
)

// hostModuleRegistrar instantiates an ABI's host import module on a runtime,
// closing over the plugin's capabilities and config.
type hostModuleRegistrar func(ctx context.Context, r wazero.Runtime, p *plugin) error

// abiRegistry maps an ABI identifier to its host-module registrar.
var abiRegistry = map[string]hostModuleRegistrar{
	ABIJulV1: registerJulHostModule,
	ABIJulV2: registerJulV2HostModule,
}
