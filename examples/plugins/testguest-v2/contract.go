// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"strconv"
	"strings"

	sdk "juliaplugins/sdk/v2"
)

// Raw imports for contract-enforcement ops: each is called with an
// out-of-bounds pointer (oob:<fn>) or from the wrong phase (wrong:<fn>).

//go:wasmimport jul-abi/v2 log
func rawLog(level, ptr, n uint32)

//go:wasmimport jul-abi/v2 get_method
func rawGetMethod(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 set_uri
func rawSetURI(ptr, n uint32)

//go:wasmimport jul-abi/v2 get_request_header
func rawGetRequestHeader(namePtr, nameLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 set_request_header
func rawSetRequestHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul-abi/v2 read_request_body
func rawReadRequestBody(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 write_response_body
func rawWriteResponseBody(ptr, n uint32)

//go:wasmimport jul-abi/v2 set_response_status
func rawSetResponseStatus(code uint32)

//go:wasmimport jul-abi/v2 get_config
func rawGetConfig(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 kv_get
func rawKVGet(keyPtr, keyLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 kv_set
func rawKVSet(keyPtr, keyLen, valPtr, valLen uint32) int32

//go:wasmimport jul-abi/v2 fetch
func rawFetch(methodPtr, methodLen, urlPtr, urlLen, bodyPtr, bodyLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 fetch_read
func rawFetchRead(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 set_request_state
func rawSetRequestState(ptr, n uint32) int32

//go:wasmimport jul-abi/v2 get_request_state
func rawGetRequestState(buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_get_header
func rawRespGetHeader(namePtr, nameLen, index, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_set_header
func rawRespSetHeader(namePtr, nameLen, valPtr, valLen uint32) int32

//go:wasmimport jul-abi/v2 resp_del_header
func rawRespDelHeader(namePtr, nameLen uint32) int32

//go:wasmimport jul-abi/v2 resp_body_replace
func rawRespBodyReplace(ptr, n uint32) int32

//go:wasmimport jul-abi/v2 resp_body_read
func rawRespBodyRead(buf, limit uint32) int32

const bad = 0xFFFFFF00

// oob calls fn with an out-of-bounds guest range; the host must fail the
// invocation. It reports whether fn was known.
func oob(fn string) bool {
	n := "X-Op"
	switch fn {
	case "log":
		rawLog(1, bad, 0xFF)
	case "get_method":
		rawGetMethod(bad, 0xFF)
	case "set_uri":
		rawSetURI(bad, 0xFF)
	case "get_request_header":
		rawGetRequestHeader(bad, 0xFF, 0, 0)
	case "set_request_header":
		rawSetRequestHeader(bad, 0xFF, ptr(n), uint32(len(n)))
	case "set_response_header":
		rawSetResponseHeader(ptr(n), uint32(len(n)), bad, 0xFF)
	case "read_request_body":
		rawReadRequestBody(bad, 0xFFFF)
	case "write_response_body":
		rawWriteResponseBody(bad, 0xFF)
	case "get_config":
		rawGetConfig(bad, 0xFFFF)
	case "kv_get":
		rawKVGet(bad, 0xFF, 0, 0)
	case "kv_set":
		rawKVSet(ptr(n), uint32(len(n)), bad, 0xFF)
	case "fetch":
		rawFetch(bad, 0xFF, 0, 0, 0, 0, 0, 0)
	case "fetch_read":
		rawFetchRead(bad, 0xFF)
	case "set_request_state":
		rawSetRequestState(bad, 0xFF)
	case "get_request_state":
		rawGetRequestState(bad, 0xFF)
	case "resp_get_header":
		rawRespGetHeader(bad, 0xFF, 0, 0, 0)
	case "resp_set_header":
		rawRespSetHeader(ptr(n), uint32(len(n)), bad, 0xFF)
	case "resp_del_header":
		rawRespDelHeader(bad, 0xFF)
	case "resp_body_read":
		rawRespBodyRead(bad, 0xFFFF)
	case "resp_body_replace":
		rawRespBodyReplace(bad, 0xFF)
	default:
		return false
	}
	return true
}

// wrongPhase calls a request-mutating function from handle_response.
func wrongPhase(fn string) {
	n := "X-Late"
	switch fn {
	case "set_uri":
		rawSetURI(ptr("/x"), 2)
	case "set_request_header":
		rawSetRequestHeader(ptr(n), uint32(len(n)), ptr(n), uint32(len(n)))
	case "read_request_body":
		rawReadRequestBody(0, 0)
	case "write_response_body":
		rawWriteResponseBody(ptr(n), uint32(len(n)))
	case "set_response_status":
		rawSetResponseStatus(500)
	default:
		rawSetResponseHeader(ptr(n), uint32(len(n)), ptr(n), uint32(len(n)))
	}
}

// requestSurface exercises every request-phase call and reports the results.
func requestSurface(req *sdk.Request) {
	for lvl := sdk.LevelDebug; lvl <= sdk.LevelError; lvl++ {
		sdk.Log(lvl, "surface")
	}
	before := req.URI()
	req.SetURI("/rewritten?x=1")
	req.SetURI("::not a uri::")
	_, absent := req.Header("X-Absent")
	req.SetHeader("X-Added", "1")
	body := req.Body()
	again := req.Body()
	kvSet := sdk.KVSet("k", []byte("v"))
	kvVal, kvFound := sdk.KVGet("k")
	_, kvMissing := sdk.KVGet("missing")
	status, fetched, err := sdk.Fetch("GET", target(req), nil)
	parts := []string{
		req.Method(), before, strconv.FormatBool(absent), strconv.Itoa(len(body)), strconv.Itoa(len(again)),
		strconv.FormatBool(len(sdk.Config()) > 0), strconv.FormatBool(kvSet), string(kvVal),
		strconv.FormatBool(kvFound), strconv.FormatBool(kvMissing),
		strconv.Itoa(status), strconv.Itoa(len(fetched)), errName(err), strconv.FormatBool(sdk.LastFetchTruncated()),
	}
	req.SetResponseHeader("X-Surface", strings.Join(parts, "|"))
}

// responseSurface exercises every call allowed in the response phase.
func responseSurface(resp *sdk.Response) {
	for lvl := sdk.LevelDebug; lvl <= sdk.LevelError; lvl++ {
		sdk.Log(lvl, "surface")
	}
	h, _ := resp.RequestHeader("X-Op")
	kvSet := sdk.KVSet("rk", []byte("rv"))
	v, found := sdk.KVGet("rk")
	status, _, err := sdk.Fetch("GET", "http://fetch.test/", nil)
	parts := []string{resp.Method(), resp.URI(), h, strconv.FormatBool(len(sdk.Config()) > 0),
		strconv.FormatBool(kvSet), string(v), strconv.FormatBool(found), strconv.Itoa(status), errName(err)}
	_ = resp.SetHeader("X-Surface", strings.Join(parts, "|"))
}

func target(req *sdk.Request) string {
	if t, ok := req.Header("X-Fetch"); ok {
		return t
	}
	return "http://fetch.test/"
}

func errName(err error) string {
	switch err {
	case nil:
		return "ok"
	case sdk.ErrFetchDenied:
		return "denied"
	case sdk.ErrFetchBlocked:
		return "blocked"
	case sdk.ErrFetchEgressBlocked:
		return "egress"
	default:
		return "failed"
	}
}
