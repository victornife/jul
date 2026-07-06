// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// kv-counter is a Jul.IA middleware plugin that counts requests in the plugin
// key/value store and reports the running total in the X-Count response header.
// It demonstrates the capability-gated KV host functions: the plugin must be
// granted `kv = true` in its [plugins.NAME] config or the KV calls are denied.
//
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o kv-counter.wasm .
package main

import (
	"strconv"

	"juliaplugins/sdk"
)

func init() {
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		const key = "count"
		n := 0
		if v, ok := sdk.KVGet(key); ok {
			n, _ = strconv.Atoi(string(v))
		}
		n++
		sdk.KVSet(key, []byte(strconv.Itoa(n)))
		req.SetResponseHeader("X-Count", strconv.Itoa(n))
		return sdk.Continue
	}
}

func main() {}
