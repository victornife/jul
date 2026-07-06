// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// testguest-loop is used by the plugin sandbox tests: it spins forever inside
// handle_request to verify the host enforces the per-call timeout and aborts the
// runaway guest. It is not a usage example.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o testguest-loop.wasm .
package main

import "juliaplugins/sdk"

func init() {
	sdk.Handle = func(*sdk.Request) sdk.Action {
		for {
			// Busy loop; the host interrupts this via the call deadline.
			sink++
		}
	}
}

var sink uint64

func main() {}
