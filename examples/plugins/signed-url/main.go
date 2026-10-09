// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// signed-url is a fail-closed jul-abi/v1 middleware for GET/HEAD media links.
package main

import (
	"juliaplugins/sdk"
	"juliaplugins/signed-url/policy"
	"time"
)

func init() {
	// The config is immutable for the lifetime of this module instance.
	var p *policy.Policy
	var loaded bool
	sdk.Handle = func(req *sdk.Request) sdk.Action {
		if !loaded {
			p, _ = policy.Parse(req.Config())
			loaded = true
		}
		reason := "configuration"
		if p != nil {
			reason = p.Validate(req.Method(), req.URI(), time.Now().Unix())
		}
		if reason == "" {
			return sdk.Continue
		}
		// Fixed reasons only: no keys, signatures, URLs or query values in logs.
		sdk.Log(sdk.LevelInfo, "signed-url denied: "+reason)
		sdk.SetResponseStatus(403)
		sdk.SetResponseHeader("Content-Type", "text/plain; charset=utf-8")
		sdk.SetResponseHeader("Cache-Control", "no-store")
		sdk.WriteResponseBody([]byte("Forbidden\n"))
		return sdk.Stop
	}
}
func main() {}
