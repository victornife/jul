// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// inspect-load is a synthetic jul-abi/v2 guest for the #695 WAF/WASM resource
// assessment (scripts/waf-wasm-assessment.go). It is a measurement fixture,
// not a security control: it makes request- and response-body inspection work
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
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o inspect-load.wasm .
package main

import (
	"bytes"
	"encoding/json"
	"strconv"

	sdk "juliaplugins/sdk/v2"
)

type settings struct {
	Name      string `json:"name"`
	ReadBody  string `json:"read_body"`
	Subscribe string `json:"subscribe"`
	Replace   string `json:"replace"`
}

var (
	reqBlock  = []byte("ASSESS-WASM-REQ-BLOCK")
	respBlock = []byte("ASSESS-WASM-RESP-BLOCK")
)

func load() settings {
	var s settings
	_ = json.Unmarshal(sdk.Config(), &s)
	if s.Name == "" {
		s.Name = "x"
	}
	return s
}

func init() {
	sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
		cfg := load()
		if cfg.ReadBody == "true" {
			body := req.Body()
			if bytes.Contains(body, reqBlock) {
				req.SetResponseStatus(403)
				req.WriteResponseBody([]byte("blocked by inspect-load " + cfg.Name + "\n"))
				return sdk.Stop
			}
			req.SetHeader("X-Assess-"+cfg.Name+"-Req-Len", strconv.Itoa(len(body)))
		}
		if cfg.Subscribe == "true" {
			_ = req.SubscribeResponse(sdk.Body)
		}
		return sdk.Continue
	}
	sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
		cfg := load()
		body, err := resp.Body()
		if err != nil {
			_ = resp.SetHeader("X-Assess-"+cfg.Name+"-Resp", "unavailable-"+strconv.Itoa(int(resp.BodyState())))
			return sdk.Deliver
		}
		if bytes.Contains(body, respBlock) {
			return sdk.Reject
		}
		if cfg.Replace == "true" {
			_ = resp.ReplaceBody(body)
		}
		_ = resp.SetHeader("X-Assess-"+cfg.Name+"-Resp", strconv.Itoa(len(body)))
		return sdk.Deliver
	}
}

func main() {}
