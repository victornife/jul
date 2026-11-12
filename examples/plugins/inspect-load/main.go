// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// inspect-load is a synthetic jul-abi/v2 guest for the maintained inspection
// assessment (scripts/inspection-assessment). It is a measurement fixture, not
// a security control: it makes request- and response-body inspection work
// observable so a load run can prove the work actually executed.
//
// Config (all strings):
//
//	name      = "a"     # label used in the evidence headers below
//	read_body = "true"  # buffer and scan the request body
//	subscribe = "true"  # subscribe to and scan the response body
//	replace   = "true"  # also exercise the body-replacement path
//
// Evidence: the request gains X-Assess-<name>-Req-Len (the scanned length,
// echoed by the assessment origin) and the response gains
// X-Assess-<name>-Resp (scanned length or "unavailable-<state>"). A request
// body containing ASSESS-WASM-REQ-BLOCK is answered 403; a response body
// containing ASSESS-WASM-RESP-BLOCK is rejected.
//
// Build: examples/plugins/build.sh DIR all, or
// GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false -buildmode=c-shared -o inspect-load.wasm ./inspect-load
package main

import (
	"juliaplugins/inspect-load/inspect"
	sdk "juliaplugins/sdk/v2"
)

var _ = inspect.Install(&sdk.HandleRequest, &sdk.HandleResponse, sdk.Config,
	sdk.Body, sdk.Continue, sdk.Stop, sdk.Deliver, sdk.Reject)

func main() {}
