// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package nobody is the host-testable behaviour of the inspect-nobody guest.
package nobody

import (
	"bytes"
	"encoding/json"
	"strconv"
)

// MaxState is the largest per-request state the fixture stores.
const MaxState = 4096

// BlockHeader makes the guest answer 403 without reading the body.
const BlockHeader = "X-Assess-Wasm-Block"

// Load parses the plugin configuration on every invocation, like inspect-load,
// so configuration parsing stays part of the measured work. An invalid or
// out-of-range state_bytes uses MaxState.
func Load(raw []byte) (name string, stateBytes int) {
	var s struct {
		Name       string `json:"name"`
		StateBytes string `json:"state_bytes"`
	}
	_ = json.Unmarshal(raw, &s)
	if s.Name == "" {
		s.Name = "x"
	}
	n, err := strconv.Atoi(s.StateBytes)
	if err != nil || n < 0 || n > MaxState {
		n = MaxState
	}
	return s.Name, n
}

// Request is the request-phase surface the guest uses.
type Request[M any] interface {
	Header(name string) (string, bool)
	SetHeader(name, value string)
	SetResponseStatus(code int)
	WriteResponseBody(b []byte)
	SetState(b []byte) error
	SubscribeResponse(mode M) error
}

// Response is the response-phase surface the guest uses.
type Response interface {
	State() []byte
	SetHeader(name, value string) error
}

// Install sets the SDK callbacks. It is generic so the wasip1 main package can
// pass the SDK's concrete types without containing any statements of its own.
func Install[Req Request[M], Resp Response, A, V, M any](
	handleRequest *func(Req) A, handleResponse *func(Resp) V, config func() []byte,
	headersMode M, cont, stop A, deliver V,
) bool {
	*handleRequest = func(req Req) A {
		name, n := Load(config())
		if _, block := req.Header(BlockHeader); block {
			req.SetResponseStatus(403)
			req.WriteResponseBody([]byte("blocked by inspect-nobody " + name + "\n"))
			return stop
		}
		if err := req.SetState(bytes.Repeat([]byte{'s'}, n)); err != nil {
			req.SetResponseStatus(500)
			return stop
		}
		req.SetHeader("X-Assess-"+name+"-State", strconv.Itoa(n))
		_ = req.SubscribeResponse(headersMode)
		return cont
	}
	*handleResponse = func(resp Resp) V {
		name, _ := Load(config())
		_ = resp.SetHeader("X-Assess-"+name+"-Resp", "state-"+strconv.Itoa(len(resp.State())))
		return deliver
	}
	return true
}
