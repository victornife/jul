// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

// Package plugins loads and runs sandboxed WebAssembly plugins on top of the
// wazero runtime (pure Go, no cgo, so the server stays a single static binary).
//
// A Manager is created once for the process and owns the shared compilation
// cache and the key/value store. On startup and on every reload the server calls
// Manager.Build with the current [plugins] config to produce a *Set: the
// compiled, instantiated plugins for that generation. A Set exposes each plugin
// as middleware (wraps a handler, may pass through) or as a terminal handler
// (the location's action). The previous generation's Set is closed after the new
// one is live, mirroring the generational teardown used for proxy pools and gRPC
// connections.
//
// # ABI seam
//
// Guests speak an ABI: a contract of host import functions (the "jul" module)
// and guest exports (handle_request). v1 ships one ABI, jul-abi/v1, authored for
// guests compiled with the standard Go toolchain for GOOS=wasip1. Additional
// ABIs (for example http-wasm or proxy-wasm) can be added behind abiRegistry
// without touching the manager or the HTTP wiring.
package plugins

import (
	"context"

	"github.com/tetratelabs/wazero"
)

// Compiled reports whether this build includes the WASM plugin runtime. It is
// true here (the "wasmplugins" build tag is set) and false in the stub build,
// letting callers detect a lean binary.
const Compiled = true

// ABI identifiers. jul-abi/v1 is the only ABI implemented in v1; the constants
// document the seam where future ABIs register.
const (
	// ABIJulV1 is the native Jul.IA ABI for Go/wasip1 guests.
	ABIJulV1 = "jul-abi/v1"
)

// hostModuleRegistrar instantiates an ABI's host import module on a runtime,
// closing over the plugin's capabilities and config. This is the ABI seam:
// adding http-wasm or proxy-wasm support means registering another entry here
// (and selecting it per plugin), without changing the manager or HTTP wiring.
type hostModuleRegistrar func(ctx context.Context, r wazero.Runtime, p *plugin) error

// abiRegistry maps an ABI identifier to its host-module registrar.
var abiRegistry = map[string]hostModuleRegistrar{
	ABIJulV1: registerJulHostModule,
}
