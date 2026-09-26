// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// v2-status-header is a jul-abi/v2 metadata-only response plugin. For every
// request it subscribes to the response headers (no body buffering) and then:
// labels the response with its status class, strips backend fingerprinting
// headers, and marks 5xx responses uncacheable.
//
// Configure with abi = "jul-abi/v2".
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o v2-status-header.wasm .
package main

import (
	"strconv"

	sdk "juliaplugins/sdk/v2"
)

func init() {
	sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
		_ = req.SubscribeResponse(sdk.Headers)
		return sdk.Continue
	}
	sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
		status := resp.Status()
		_ = resp.SetHeader("X-Upstream-Status-Class", strconv.Itoa(status/100)+"xx")
		_ = resp.DelHeader("Server")
		_ = resp.DelHeader("X-Powered-By")
		if status >= 500 {
			_ = resp.SetHeader("Cache-Control", "no-store")
		}
		return sdk.Deliver
	}
}

func main() {}
