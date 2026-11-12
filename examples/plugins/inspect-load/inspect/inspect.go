// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package inspect is the host-testable behaviour of the inspect-load guest.
// It is the maintained port of the #695 synthetic guest
// (docs/reviews/evidence/2026-11-09-695/tooling/inspect-load.go.txt) and keeps
// its request-body, response-body and invocation evidence unchanged.
package inspect

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// Request and response markers the assessment canaries send.
var (
	ReqBlock  = []byte("ASSESS-WASM-REQ-BLOCK")
	RespBlock = []byte("ASSESS-WASM-RESP-BLOCK")
)

// Settings is the guest configuration; every value is a string.
type Settings struct {
	Name      string `json:"name"`
	ReadBody  string `json:"read_body"`
	Subscribe string `json:"subscribe"`
	Replace   string `json:"replace"`
}

// Load parses the plugin configuration. Like the archived guest it is called
// on every invocation, so configuration parsing remains part of the measured work.
func Load(raw []byte) Settings {
	var s Settings
	_ = json.Unmarshal(raw, &s)
	if s.Name == "" {
		s.Name = "x"
	}
	return s
}

// Request is the request-phase surface the guest uses.
type Request[M any] interface {
	Body() []byte
	SetHeader(name, value string)
	SetResponseStatus(code int)
	WriteResponseBody(b []byte)
	SubscribeResponse(mode M) error
}

// Response is the response-phase surface the guest uses.
type Response[S ~uint32] interface {
	Body() ([]byte, error)
	BodyState() S
	SetHeader(name, value string) error
	ReplaceBody(b []byte) error
}

// Install sets the SDK callbacks. It is generic so the wasip1 main package can
// pass the SDK's concrete types without containing any statements of its own.
func Install[Req Request[M], Resp Response[S], A, V, M any, S ~uint32](
	handleRequest *func(Req) A, handleResponse *func(Resp) V, config func() []byte,
	bodyMode M, cont, stop A, deliver, reject V,
) bool {
	*handleRequest = func(req Req) A {
		cfg := Load(config())
		if cfg.ReadBody == "true" {
			body := req.Body()
			if bytes.Contains(body, ReqBlock) {
				req.SetResponseStatus(403)
				req.WriteResponseBody([]byte("blocked by inspect-load " + cfg.Name + "\n"))
				return stop
			}
			req.SetHeader("X-Assess-"+cfg.Name+"-Req-Len", strconv.Itoa(len(body)))
		}
		if cfg.Subscribe == "true" {
			_ = req.SubscribeResponse(bodyMode)
		}
		return cont
	}
	*handleResponse = func(resp Resp) V {
		cfg := Load(config())
		body, err := resp.Body()
		if err != nil {
			_ = resp.SetHeader("X-Assess-"+cfg.Name+"-Resp", "unavailable-"+strconv.Itoa(int(resp.BodyState())))
			return deliver
		}
		if bytes.Contains(body, RespBlock) {
			return reject
		}
		if cfg.Replace == "true" {
			_ = resp.ReplaceBody(body)
		}
		_ = resp.SetHeader("X-Assess-"+cfg.Name+"-Resp", strconv.Itoa(len(body)))
		return deliver
	}
	return true
}
