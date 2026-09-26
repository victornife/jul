// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package sdk is the guest SDK for Jul.IA jul-abi/v2 WASM plugins.
//
// Use it only when a plugin needs the response the location actually produced
// (its status, headers, or a bounded body). A request-only plugin gains nothing
// from v2 and should stay on the v1 SDK (juliaplugins/sdk). See docs/abi.md.
//
// A v2 plugin sets HandleRequest and, to see responses, HandleResponse, and
// subscribes per request from HandleRequest:
//
//	func init() {
//		sdk.HandleRequest = func(req *sdk.Request) sdk.Action {
//			req.SubscribeResponse(sdk.Headers)
//			return sdk.Continue
//		}
//		sdk.HandleResponse = func(resp *sdk.Response) sdk.Verdict {
//			resp.SetHeader("X-Upstream-Status", strconv.Itoa(resp.Status()))
//			return sdk.Deliver
//		}
//	}
//
//	func main() {}
//
// The two callbacks may run on different module instances: package-level
// variables are NOT per request. Pass per-request data with Request.SetState
// and read it back with Response.State.
//
// Configure the plugin with abi = "jul-abi/v2". Build with:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
package sdk

import (
	"errors"
	"strings"
	"unsafe"
)

// Action is the decision HandleRequest returns.
type Action uint32

const (
	// Stop means the guest wrote the response; the host skips the next handler.
	Stop Action = 0
	// Continue passes the request on.
	Continue Action = 1
)

// Verdict is the decision HandleResponse returns.
type Verdict uint32

const (
	// Deliver sends the (possibly modified) response.
	Deliver Verdict = 0
	// Reject discards the response: the client receives the status set with
	// SetStatus when it is 400-599 (otherwise 502) and an empty body.
	Reject Verdict = 1
)

// Mode selects what a response subscription presents.
type Mode uint32

const (
	// Headers presents the status and headers.
	Headers Mode = 0
	// Body additionally presents the bounded buffered body when eligible.
	Body Mode = 1
)

// BodyState reports whether the response body is available and, if not, why.
type BodyState uint32

const (
	BodyAvailable    BodyState = 0
	BodyNotRequested BodyState = 1 // subscribed with Headers
	BodyNone         BodyState = 2 // HEAD, 204 or 304
	BodyTooLarge     BodyState = 3 // exceeds max_response_body
	BodyStreaming    BodyState = 4 // declared stream (SSE, gRPC, ...)
	BodyEncoded      BodyState = 5 // non-identity Content-Encoding
	BodyPartial      BodyState = 6 // 206 Partial Content
	BodyUpgraded     BodyState = 7 // protocol switch; read-only
)

// Log levels for Log.
const (
	LevelDebug = 0
	LevelInfo  = 1
	LevelWarn  = 2
	LevelError = 3
)

// Errors returned by the v2 host calls.
var (
	ErrNotFound    = errors.New("jul: not found")
	ErrInvalid     = errors.New("jul: invalid argument")
	ErrForbidden   = errors.New("jul: forbidden header or status")
	ErrUnavailable = errors.New("jul: response body not available")
	ErrTooLarge    = errors.New("jul: value too large")
	ErrCommitted   = errors.New("jul: response already committed")
	ErrWrongPhase  = errors.New("jul: call not allowed in this phase")
	ErrUnsupported = errors.New("jul: not supported for this plugin")
)

func codeErr(rc int32) error {
	switch rc {
	case 0:
		return nil
	case -1:
		return ErrNotFound
	case -2:
		return ErrInvalid
	case -3:
		return ErrForbidden
	case -4:
		return ErrUnavailable
	case -5:
		return ErrTooLarge
	case -6:
		return ErrCommitted
	case -7:
		return ErrWrongPhase
	default:
		return ErrUnsupported
	}
}

// HandleRequest is the request callback. A nil HandleRequest continues.
var HandleRequest func(*Request) Action

// HandleResponse is the response callback, run for requests that subscribed.
// A nil HandleResponse delivers the response unchanged.
var HandleResponse func(*Response) Verdict

// ---- host ABI imports (module "jul_v2") ------------------------------------

//go:wasmimport jul-abi/v2 log
func hostLog(level, ptr, n uint32)

//go:wasmimport jul-abi/v2 get_method
func hostGetMethod(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 get_uri
func hostGetURI(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 set_uri
func hostSetURI(ptr, n uint32)

//go:wasmimport jul-abi/v2 get_request_header
func hostGetRequestHeader(namePtr, nameLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 set_request_header
func hostSetRequestHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul-abi/v2 set_response_header
func hostSetResponseHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul-abi/v2 read_request_body
func hostReadRequestBody(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 write_response_body
func hostWriteResponseBody(ptr, n uint32)

//go:wasmimport jul-abi/v2 set_response_status
func hostSetResponseStatus(code uint32)

//go:wasmimport jul-abi/v2 get_config
func hostGetConfig(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 kv_get
func hostKVGet(keyPtr, keyLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 kv_set
func hostKVSet(keyPtr, keyLen, valPtr, valLen uint32) int32

//go:wasmimport jul-abi/v2 fetch
func hostFetch(methodPtr, methodLen, urlPtr, urlLen, bodyPtr, bodyLen, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 last_fetch_len
func hostLastFetchLen() uint32

//go:wasmimport jul-abi/v2 last_fetch_truncated
func hostLastFetchTruncated() int32

//go:wasmimport jul-abi/v2 fetch_read
func hostFetchRead(buf, limit uint32) uint32

//go:wasmimport jul-abi/v2 subscribe_response
func hostSubscribeResponse(mode uint32) int32

//go:wasmimport jul-abi/v2 set_request_state
func hostSetRequestState(ptr, n uint32) int32

//go:wasmimport jul-abi/v2 get_request_state
func hostGetRequestState(buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_status
func hostRespStatus() int32

//go:wasmimport jul-abi/v2 resp_set_status
func hostRespSetStatus(code uint32) int32

//go:wasmimport jul-abi/v2 resp_get_header
func hostRespGetHeader(namePtr, nameLen, index, buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_header_names
func hostRespHeaderNames(buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_set_header
func hostRespSetHeader(namePtr, nameLen, valPtr, valLen uint32) int32

//go:wasmimport jul-abi/v2 resp_add_header
func hostRespAddHeader(namePtr, nameLen, valPtr, valLen uint32) int32

//go:wasmimport jul-abi/v2 resp_del_header
func hostRespDelHeader(namePtr, nameLen uint32) int32

//go:wasmimport jul-abi/v2 resp_body_state
func hostRespBodyState() int32

//go:wasmimport jul-abi/v2 resp_body_read
func hostRespBodyRead(buf, limit uint32) int32

//go:wasmimport jul-abi/v2 resp_body_replace
func hostRespBodyReplace(ptr, n uint32) int32

// ---- helpers ---------------------------------------------------------------

func bytePtr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

func strPtr(s string) uint32 {
	if len(s) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(unsafe.StringData(s))))
}

// readInto grows a buffer until a caller-allocates host getter fits.
func readInto(fn func(buf, limit uint32) uint32) []byte {
	buf := make([]byte, 256)
	n := fn(bytePtr(buf), uint32(len(buf)))
	if n > uint32(len(buf)) {
		buf = make([]byte, n)
		n = fn(bytePtr(buf), uint32(len(buf)))
	}
	return append([]byte(nil), buf[:n]...)
}

// readInto32 is readInto for getters returning a negative code on failure.
func readInto32(fn func(buf, limit uint32) int32) ([]byte, int32) {
	buf := make([]byte, 256)
	n := fn(bytePtr(buf), uint32(len(buf)))
	if n < 0 {
		return nil, n
	}
	if uint32(n) > uint32(len(buf)) {
		buf = make([]byte, n)
		if n = fn(bytePtr(buf), uint32(len(buf))); n < 0 {
			return nil, n
		}
	}
	return append([]byte(nil), buf[:n]...), 0
}

// Log writes a message to the host log at the given level.
func Log(level int, msg string) { hostLog(uint32(level), strPtr(msg), uint32(len(msg))) }

// Config returns the plugin's config table as a JSON object.
func Config() []byte { return readInto(hostGetConfig) }

// KVGet reads a value from the plugin key/value store (needs kv = true).
func KVGet(key string) ([]byte, bool) {
	b, rc := readInto32(func(buf, limit uint32) int32 {
		return hostKVGet(strPtr(key), uint32(len(key)), buf, limit)
	})
	return b, rc == 0
}

// KVSet stores a value in the plugin key/value store (needs kv = true).
func KVSet(key string, value []byte) bool {
	return hostKVSet(strPtr(key), uint32(len(key)), bytePtr(value), uint32(len(value))) == 0
}

// Fetch errors mirror the host fetch return codes.
var (
	ErrFetchDenied        = errors.New("fetch: plugin lacks the fetch capability")
	ErrFetchBlocked       = errors.New("fetch: target blocked by the plugin allow-list or SSRF guard")
	ErrFetchFailed        = errors.New("fetch: transport error")
	ErrFetchEgressBlocked = errors.New("fetch: target blocked by the global egress policy")
)

// Fetch performs a guarded outbound HTTP request (needs fetch = true and an
// allow-listed host). Check LastFetchTruncated for a capped response.
func Fetch(method, url string, body []byte) (int, []byte, error) {
	buf := make([]byte, 256)
	rc := hostFetch(strPtr(method), uint32(len(method)), strPtr(url), uint32(len(url)),
		bytePtr(body), uint32(len(body)), bytePtr(buf), uint32(len(buf)))
	switch rc {
	case -2:
		return 0, nil, ErrFetchDenied
	case -3:
		return 0, nil, ErrFetchBlocked
	case -4:
		return 0, nil, ErrFetchFailed
	case -5:
		return 0, nil, ErrFetchEgressBlocked
	}
	n := hostLastFetchLen()
	if n <= uint32(len(buf)) {
		return int(rc), buf[:n], nil
	}
	buf = make([]byte, n)
	return int(rc), buf[:hostFetchRead(bytePtr(buf), n)], nil
}

// LastFetchTruncated reports whether the last Fetch response was capped at
// max_fetch_response.
func LastFetchTruncated() bool { return hostLastFetchTruncated() != 0 }

func method() string { return string(readInto(hostGetMethod)) }
func uri() string    { return string(readInto(hostGetURI)) }

func requestHeader(name string) (string, bool) {
	b, rc := readInto32(func(buf, limit uint32) int32 {
		return hostGetRequestHeader(strPtr(name), uint32(len(name)), buf, limit)
	})
	return string(b), rc == 0
}

// ---- request phase -----------------------------------------------------------

// Request is the in-flight request during HandleRequest.
type Request struct{}

// Method returns the HTTP method.
func (*Request) Method() string { return method() }

// URI returns the request URI (path and query).
func (*Request) URI() string { return uri() }

// SetURI rewrites the request URI before the next handler runs.
func (*Request) SetURI(s string) { hostSetURI(strPtr(s), uint32(len(s))) }

// Header returns a request header value and whether it was present.
func (*Request) Header(name string) (string, bool) { return requestHeader(name) }

// SetHeader sets a request header for the next handler.
func (*Request) SetHeader(name, value string) {
	hostSetRequestHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value)))
}

// SetResponseHeader sets a response header before the next handler runs (the
// v1 behaviour; to act on the actual response use HandleResponse).
func (*Request) SetResponseHeader(name, value string) {
	hostSetResponseHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value)))
}

// SetResponseStatus sets the status of the guest's own response (with Stop).
func (*Request) SetResponseStatus(code int) { hostSetResponseStatus(uint32(code)) }

// WriteResponseBody appends to the guest's own response body (with Stop).
func (*Request) WriteResponseBody(b []byte) { hostWriteResponseBody(bytePtr(b), uint32(len(b))) }

// Body returns the buffered request body (up to max_request_body).
func (*Request) Body() []byte { return readInto(hostReadRequestBody) }

// SubscribeResponse asks for HandleResponse to run for this request. It takes
// effect only if HandleRequest returns Continue; the last call wins.
func (*Request) SubscribeResponse(mode Mode) error {
	return codeErr(hostSubscribeResponse(uint32(mode)))
}

// SetState stores up to 4096 bytes of per-request state for HandleResponse.
func (*Request) SetState(b []byte) error {
	return codeErr(hostSetRequestState(bytePtr(b), uint32(len(b))))
}

// ---- response phase ----------------------------------------------------------

// Response is the action's response during HandleResponse. The request it
// answers is readable (never writable) through Method, URI and RequestHeader.
type Response struct{}

// Method returns the request method.
func (*Response) Method() string { return method() }

// URI returns the request URI.
func (*Response) URI() string { return uri() }

// RequestHeader returns a request header value and whether it was present.
func (*Response) RequestHeader(name string) (string, bool) { return requestHeader(name) }

// State returns the bytes HandleRequest stored with Request.SetState.
func (*Response) State() []byte {
	b, _ := readInto32(hostGetRequestState)
	return b
}

// Status returns the current response status.
func (*Response) Status() int { return int(hostRespStatus()) }

// SetStatus changes the status (200-599 except 204, 205, 304).
func (*Response) SetStatus(code int) error { return codeErr(hostRespSetStatus(uint32(code))) }

// HeaderValues returns every value of a response header.
func (*Response) HeaderValues(name string) []string {
	var out []string
	for i := uint32(0); ; i++ {
		b, rc := readInto32(func(buf, limit uint32) int32 {
			return hostRespGetHeader(strPtr(name), uint32(len(name)), i, buf, limit)
		})
		if rc != 0 {
			return out
		}
		out = append(out, string(b))
	}
}

// Header returns the first value of a response header.
func (r *Response) Header(name string) (string, bool) {
	b, rc := readInto32(func(buf, limit uint32) int32 {
		return hostRespGetHeader(strPtr(name), uint32(len(name)), 0, buf, limit)
	})
	return string(b), rc == 0
}

// HeaderNames returns the response header names, sorted.
func (*Response) HeaderNames() []string {
	b, rc := readInto32(hostRespHeaderNames)
	if rc != 0 || len(b) == 0 {
		return nil
	}
	return strings.Split(string(b), "\n")
}

// SetHeader replaces a response header.
func (*Response) SetHeader(name, value string) error {
	return codeErr(hostRespSetHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value))))
}

// AddHeader appends a response header value.
func (*Response) AddHeader(name, value string) error {
	return codeErr(hostRespAddHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value))))
}

// DelHeader removes every value of a response header.
func (*Response) DelHeader(name string) error {
	return codeErr(hostRespDelHeader(strPtr(name), uint32(len(name))))
}

// BodyState reports whether Body is available and, if not, why.
func (*Response) BodyState() BodyState { return BodyState(hostRespBodyState()) }

// Body returns the buffered response body, or ErrUnavailable.
func (*Response) Body() ([]byte, error) {
	b, rc := readInto32(hostRespBodyRead)
	return b, codeErr(rc)
}

// ReplaceBody replaces the whole response body (the host fixes Content-Length).
func (*Response) ReplaceBody(b []byte) error {
	return codeErr(hostRespBodyReplace(bytePtr(b), uint32(len(b))))
}

// ---- exports -----------------------------------------------------------------

//go:wasmexport jul-abi/v2
func abiV2() {}

//go:wasmexport handle_request
func handleRequest() uint32 {
	if HandleRequest == nil {
		return uint32(Continue)
	}
	return uint32(HandleRequest(&Request{}))
}

//go:wasmexport handle_response
func handleResponse() uint32 {
	if HandleResponse == nil {
		return uint32(Deliver)
	}
	return uint32(HandleResponse(&Response{}))
}
