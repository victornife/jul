// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// inspect-nobody is a synthetic jul-abi/v2 guest for the maintained inspection
// assessment (scripts/inspection-assessment, ADR 0032 slice I2). It never
// imports read_request_body, so Jul streams the request body to the origin
// instead of preparing it; it exercises only non-body host allocations. It is
// a measurement fixture, not a security control.
//
// Config (all strings):
//
//	name        = "a"     # label used in the evidence headers below
//	state_bytes = "4096"  # per-request state stored with set_request_state
//
// For every request it stores state_bytes of request state, labels the
// request X-Assess-<name>-State: <len>, and subscribes to the response
// headers only (no body buffering), so the state stays held until the
// response point. The response gains X-Assess-<name>-Resp: state-<len>. A
// request carrying X-Assess-Wasm-Block is answered 403 with a generated body.
//
// Build: examples/plugins/build.sh DIR all, or
// GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o inspect-nobody.wasm ./inspect-nobody
package main

import (
	"juliaplugins/inspect-nobody/nobody"
	sdk "juliaplugins/sdk/v2"
)

var _ = nobody.Install(&sdk.HandleRequest, &sdk.HandleResponse, sdk.Config,
	sdk.Headers, sdk.Continue, sdk.Stop, sdk.Deliver)

func main() {}
