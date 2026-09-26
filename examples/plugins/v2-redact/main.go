// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// v2-redact is a jul-abi/v2 bounded body transform. It subscribes to the
// response body of every request and replaces each configured secret string
// in text and JSON responses with "[REDACTED]".
//
// Config (all strings):
//
//	secrets     = "comma,separated,literals"   # what to redact
//	fail_closed = "true"                        # reject (502) a text/JSON
//	                                            # response it could not inspect
//
// Configure with abi = "jul-abi/v2". A body transform needs memory_limit of at
// least ~2x max_response_body.
// Build: GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o v2-redact.wasm .
package main

import (
	"bytes"
	"encoding/json"
	"strings"

	sdk "juliaplugins/sdk/v2"
)

type settings struct {
	Secrets    string `json:"secrets"`
	FailClosed string `json:"fail_closed"`
}

func load() settings {
	var s settings
	_ = json.Unmarshal(sdk.Config(), &s)
	return s
}

func textual(ct string) bool {
	ct = strings.ToLower(ct)
	return strings.HasPrefix(ct, "text/") || strings.Contains(ct, "json")
}

func init() {
	sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
		_ = req.SubscribeResponse(sdk.Body)
		return sdk.Continue
	}
	sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
		ct, _ := resp.Header("Content-Type")
		if !textual(ct) {
			return sdk.Deliver
		}
		cfg := load()
		body, err := resp.Body()
		if err != nil {
			// Not inspectable (too large, encoded, streaming, ...).
			if cfg.FailClosed == "true" && resp.BodyState() != sdk.BodyNone {
				return sdk.Reject
			}
			return sdk.Deliver
		}
		out := body
		for _, secret := range strings.Split(cfg.Secrets, ",") {
			if secret = strings.TrimSpace(secret); secret != "" {
				out = bytes.ReplaceAll(out, []byte(secret), []byte("[REDACTED]"))
			}
		}
		if !bytes.Equal(out, body) {
			_ = resp.ReplaceBody(out)
			_ = resp.SetHeader("X-Redacted", "1")
		}
		return sdk.Deliver
	}
}

func main() {}
