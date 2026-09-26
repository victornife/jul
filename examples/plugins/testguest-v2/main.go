// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// testguest-v2 is the jul-abi/v2 conformance guest used by the host test
// suite. The X-Op request header selects the behaviour; results are reported
// in response headers. It is not an example to copy.
package main

import (
	"strconv"
	"strings"
	"unsafe"

	sdk "juliaplugins/sdk/v2"
)

// Raw imports used to exercise host-side contract enforcement.

//go:wasmimport jul-abi/v2 resp_status
func rawRespStatus() int32

//go:wasmimport jul-abi/v2 resp_header_names
func rawRespHeaderNames(buf, limit uint32) int32

//go:wasmimport jul-abi/v2 set_response_header
func rawSetResponseHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul-abi/v2 get_uri
func rawGetURI(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 subscribe_response
func rawSubscribe(mode uint32) int32

func rc(err error) string {
	switch err {
	case nil:
		return "0"
	case sdk.ErrNotFound:
		return "-1"
	case sdk.ErrInvalid:
		return "-2"
	case sdk.ErrForbidden:
		return "-3"
	case sdk.ErrUnavailable:
		return "-4"
	case sdk.ErrTooLarge:
		return "-5"
	case sdk.ErrCommitted:
		return "-6"
	case sdk.ErrWrongPhase:
		return "-7"
	default:
		return "-8"
	}
}

var spin int

func init() {
	sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
		op, _ := req.Header("X-Op")
		if fn, ok := strings.CutPrefix(op, "req-oob:"); ok {
			oob(fn)
			return sdk.Continue
		}
		switch op {
		case "req-surface":
			requestSurface(req)
			return sdk.Continue
		case "stop":
			req.SetResponseStatus(418)
			req.WriteResponseBody([]byte("stopped"))
			return sdk.Stop
		case "req-badresult":
			return sdk.Action(7)
		case "req-oob":
			rawGetURI(0xFFFFFF00, 0xFF)
			return sdk.Continue
		case "req-wrong-phase":
			req.SetResponseHeader("X-RC", strconv.Itoa(int(rawRespStatus())))
			return sdk.Continue
		case "req-state-big":
			req.SetResponseHeader("X-RC", rc(req.SetState(make([]byte, 5000))))
			return sdk.Continue
		case "req-bad-mode":
			req.SetResponseHeader("X-RC", strconv.Itoa(int(rawSubscribe(9))))
			return sdk.Continue
		case "req-only":
			req.SetResponseHeader("X-V2-Request", "1")
			return sdk.Continue
		}
		mode := sdk.Headers
		if m, _ := req.Header("X-Mode"); m == "body" {
			mode = sdk.Body
		}
		err := req.SubscribeResponse(mode)
		req.SetResponseHeader("X-Sub-RC", rc(err))
		if s, ok := req.Header("X-State"); ok {
			_ = req.SetState([]byte(s))
		}
		return sdk.Continue
	}
	sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
		op, _ := resp.RequestHeader("X-Op")
		if c := configOp(); c != "" {
			op = c
		}
		if fn, ok := strings.CutPrefix(op, "oob:"); ok {
			oob(fn)
			return sdk.Deliver
		}
		if fn, ok := strings.CutPrefix(op, "wrong:"); ok {
			wrongPhase(fn)
			return sdk.Deliver
		}
		switch op {
		case "resp-surface":
			responseSurface(resp)
		case "echo":
			_ = resp.SetHeader("X-Seen-Status", strconv.Itoa(resp.Status()))
			_ = resp.SetHeader("X-Body-State", strconv.Itoa(int(resp.BodyState())))
			_ = resp.SetHeader("X-State", string(resp.State()))
			_ = resp.SetHeader("X-Method", resp.Method())
			_ = resp.SetHeader("X-URI", resp.URI())
			_ = resp.SetHeader("X-Names", strings.Join(resp.HeaderNames(), ","))
			_ = resp.SetHeader("X-Dup", strings.Join(resp.HeaderValues("X-Dup-In"), "|"))
			if b, err := resp.Body(); err == nil {
				_ = resp.SetHeader("X-Body-Len", strconv.Itoa(len(b)))
			} else {
				_ = resp.SetHeader("X-Body-RC", rc(err))
			}
		case "upper":
			b, err := resp.Body()
			if err != nil {
				_ = resp.SetHeader("X-Body-RC", rc(err))
				return sdk.Deliver
			}
			_ = resp.SetHeader("X-Replace-RC", rc(resp.ReplaceBody([]byte(strings.ToUpper(string(b))))))
		case "append":
			b, _ := resp.Body()
			_ = resp.ReplaceBody(append(b, '!'))
		case "empty":
			_ = resp.SetHeader("X-Replace-RC", rc(resp.ReplaceBody(nil)))
		case "big":
			n, _ := strconv.Atoi(mustHeader(resp, "X-Size"))
			_ = resp.SetHeader("X-Replace-RC", rc(resp.ReplaceBody(make([]byte, n))))
		case "set-status":
			n, _ := strconv.Atoi(mustHeader(resp, "X-Status-To"))
			_ = resp.SetHeader("X-RC", rc(resp.SetStatus(n)))
		case "headers":
			codes := []string{
				rc(resp.SetHeader("Content-Length", "1")),
				rc(resp.SetHeader("Transfer-Encoding", "chunked")),
				rc(resp.DelHeader("Connection")),
				rc(resp.SetHeader("bad name", "v")),
				rc(resp.SetHeader("X-Split", "a\r\nX-Injected: 1")),
				rc(resp.AddHeader("X-Multi", "1")),
				rc(resp.AddHeader("X-Multi", "2")),
				rc(resp.DelHeader("Server")),
				rc(resp.DelHeader("X-Absent")),
				rc(resp.SetHeader("Trailer:X", "v")),
			}
			_ = resp.SetHeader("X-RCs", strings.Join(codes, ","))
		case "flood":
			v := strings.Repeat("a", 60<<10)
			first := rc(resp.SetHeader("X-Big-1", v))
			second := rc(resp.SetHeader("X-Big-2", v))
			_ = resp.SetHeader("X-RCs", first+","+second)
		case "reject":
			if s, ok := resp.RequestHeader("X-Status-To"); ok {
				n, _ := strconv.Atoi(s)
				_ = resp.SetStatus(n)
			}
			_ = resp.SetHeader("X-Should-Not-Survive", "1")
			return sdk.Reject
		case "trap":
			_ = resp.SetHeader("X-Partial", "1")
			panic("testguest-v2: response trap")
		case "loop":
			for {
				spin++
			}
		case "oob":
			rawRespHeaderNames(0xFFFFFF00, 0xFFFF)
		case "wrong-phase":
			n := "X-Late"
			rawSetResponseHeader(ptr(n), uint32(len(n)), ptr(n), uint32(len(n)))
		case "resp-badresult":
			return sdk.Verdict(5)
		case "committed":
			sdk.KVSet("committed", []byte(strconv.Itoa(resp.Status())+" "+rc(resp.SetHeader("X-Late", "1"))+" "+strconv.Itoa(int(resp.BodyState()))))
		}
		return sdk.Deliver
	}
}

func ptr(s string) uint32 { return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s)))) }

// configOp reads an optional {"op":"..."} from the plugin config without a
// JSON dependency, so two instances of this guest can run different ops.
func configOp() string {
	cfg := string(sdk.Config())
	i := strings.Index(cfg, `"op":"`)
	if i < 0 {
		return ""
	}
	rest := cfg[i+6:]
	if j := strings.IndexByte(rest, '"'); j >= 0 {
		return rest[:j]
	}
	return ""
}

func mustHeader(resp *sdk.Response, name string) string {
	v, _ := resp.RequestHeader(name)
	return v
}

func main() {}
