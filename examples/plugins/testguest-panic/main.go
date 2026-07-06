// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// testguest-panic is used by the plugin sandbox tests: it panics inside
// handle_request to verify the host contains the trap (returns 500, server stays
// alive). It is not a usage example.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o testguest-panic.wasm .
package main

import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(*sdk.Request) sdk.Action {
		panic("boom from guest")
	}
}

func main() {}
