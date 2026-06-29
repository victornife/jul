// Package sdk is a minimal guest SDK for writing Jul.IA WASM plugins in Go.
//
// It wraps the jul-abi/v1 host ABI (the "jul" host module imported below) in a
// small, allocation-light API. A plugin registers a single request handler:
//
//	func init() {
//		sdk.Handle = func(req *sdk.Request) sdk.Action {
//			req.SetResponseHeader("X-Plugin", "hello")
//			return sdk.Continue
//		}
//	}
//
//	func main() {}
//
// Build it for the host with:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .
//
// The host instantiates the module's reactor (_initialize runs package inits,
// registering Handle) and calls the exported handle_request once per HTTP
// request. Returning Stop means the guest has produced the full response and the
// host must not call the next handler; Continue passes the (possibly mutated)
// request on.
package sdk

import (
	"errors"
	"unsafe"
)

// Action is the decision a request handler returns.
type Action uint32

const (
	// Stop indicates the guest wrote the response; the host skips the next handler.
	Stop Action = 0
	// Continue passes the request to the next handler in the chain.
	Continue Action = 1
)

// Log levels for Log.
const (
	LevelDebug = 0
	LevelInfo  = 1
	LevelWarn  = 2
	LevelError = 3
)

// Handle is the request handler the host invokes. A plugin must set it (usually
// in init). A nil Handle defaults to Continue.
var Handle func(*Request) Action

// ---- host ABI imports (module "jul") -------------------------------------

//go:wasmimport jul log
func hostLog(level, ptr, n uint32)

//go:wasmimport jul get_method
func hostGetMethod(buf, bufLimit uint32) uint32

//go:wasmimport jul get_uri
func hostGetURI(buf, bufLimit uint32) uint32

//go:wasmimport jul set_uri
func hostSetURI(ptr, n uint32)

//go:wasmimport jul get_request_header
func hostGetRequestHeader(namePtr, nameLen, buf, bufLimit uint32) int32

//go:wasmimport jul set_request_header
func hostSetRequestHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul set_response_header
func hostSetResponseHeader(namePtr, nameLen, valPtr, valLen uint32)

//go:wasmimport jul read_request_body
func hostReadRequestBody(buf, bufLimit uint32) uint32

//go:wasmimport jul write_response_body
func hostWriteResponseBody(ptr, n uint32)

//go:wasmimport jul set_response_status
func hostSetResponseStatus(code uint32)

//go:wasmimport jul get_config
func hostGetConfig(buf, bufLimit uint32) uint32

//go:wasmimport jul kv_get
func hostKVGet(keyPtr, keyLen, buf, bufLimit uint32) int32

//go:wasmimport jul kv_set
func hostKVSet(keyPtr, keyLen, valPtr, valLen uint32) int32

//go:wasmimport jul fetch
func hostFetch(methodPtr, methodLen, urlPtr, urlLen, bodyPtr, bodyLen, buf, bufLimit uint32) int32

//go:wasmimport jul last_fetch_len
func hostLastFetchLen() uint32

//go:wasmimport jul fetch_read
func hostFetchRead(buf, bufLimit uint32) uint32

// ---- pointer helpers ------------------------------------------------------
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

// readInto repeatedly calls fn (a caller-allocates host getter returning the
// total length) growing the buffer until the value fits, then returns a copy.
func readInto(fn func(buf, limit uint32) uint32) []byte {
	buf := make([]byte, 256)
	n := fn(bytePtr(buf), uint32(len(buf)))
	if n > uint32(len(buf)) {
		buf = make([]byte, n)
		n = fn(bytePtr(buf), uint32(len(buf)))
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out
}

// ---- public API -----------------------------------------------------------

// Log writes a message to the host log at the given level.
func Log(level int, msg string) {
	b := []byte(msg)
	hostLog(uint32(level), bytePtr(b), uint32(len(b)))
}

// SetResponseHeader sets a response header (added before the next handler runs).
func SetResponseHeader(name, value string) {
	hostSetResponseHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value)))
}

// SetResponseStatus sets the response status code.
func SetResponseStatus(code int) { hostSetResponseStatus(uint32(code)) }

// WriteResponseBody appends bytes to the response body.
func WriteResponseBody(b []byte) { hostWriteResponseBody(bytePtr(b), uint32(len(b))) }

// KVGet reads a value from the plugin key/value store. ok is false if the key is
// absent or the plugin lacks the "kv" capability.
func KVGet(key string) (value []byte, ok bool) {
	buf := make([]byte, 256)
	n := hostKVGet(strPtr(key), uint32(len(key)), bytePtr(buf), uint32(len(buf)))
	if n < 0 {
		return nil, false
	}
	if uint32(n) > uint32(len(buf)) {
		buf = make([]byte, n)
		n = hostKVGet(strPtr(key), uint32(len(key)), bytePtr(buf), uint32(len(buf)))
		if n < 0 {
			return nil, false
		}
	}
	out := make([]byte, n)
	copy(out, buf[:n])
	return out, true
}

// KVSet stores a value in the plugin key/value store. It returns false if the
// plugin lacks the "kv" capability.
func KVSet(key string, value []byte) bool {
	return hostKVSet(strPtr(key), uint32(len(key)), bytePtr(value), uint32(len(value))) == 0
}

// Fetch errors mirror the host fetch return codes: a denied capability, a
// guarded/blocked target, or a transport failure.
var (
	ErrFetchDenied  = errors.New("fetch: plugin lacks the fetch capability")
	ErrFetchBlocked = errors.New("fetch: target blocked by guard")
	ErrFetchFailed  = errors.New("fetch: transport error")
)

// Fetch performs a guarded outbound HTTP request (requires the "fetch"
// capability and an allow-listed host). It returns the response status, the
// response body, and an error. The body is read with last_fetch_len + fetch_read
// so an oversize response is fully retrieved without a second outbound call.
func Fetch(method, url string, body []byte) (status int, resp []byte, err error) {
	mb, ub := []byte(method), []byte(url)
	buf := make([]byte, 256)
	rc := hostFetch(strPtr(method), uint32(len(mb)), strPtr(url), uint32(len(ub)),
		bytePtr(body), uint32(len(body)), bytePtr(buf), uint32(len(buf)))
	switch rc {
	case -2:
		return 0, nil, ErrFetchDenied
	case -3:
		return 0, nil, ErrFetchBlocked
	case -4:
		return 0, nil, ErrFetchFailed
	}
	resp = readInto(hostFetchRead)
	return int(rc), resp, nil
}

// Request is the in-flight HTTP request exposed to the guest.
type Request struct{}

// Method returns the HTTP method.
func (*Request) Method() string { return string(readInto(hostGetMethod)) }

// URI returns the request URI (path and query).
func (*Request) URI() string { return string(readInto(hostGetURI)) }

// SetURI rewrites the request URI before the next handler runs.
func (*Request) SetURI(s string) { hostSetURI(strPtr(s), uint32(len(s))) }

// Header returns a request header value and whether it was present.
func (*Request) Header(name string) (string, bool) {
	buf := make([]byte, 256)
	n := hostGetRequestHeader(strPtr(name), uint32(len(name)), bytePtr(buf), uint32(len(buf)))
	if n < 0 {
		return "", false
	}
	if uint32(n) > uint32(len(buf)) {
		buf = make([]byte, n)
		n = hostGetRequestHeader(strPtr(name), uint32(len(name)), bytePtr(buf), uint32(len(buf)))
		if n < 0 {
			return "", false
		}
	}
	return string(buf[:n]), true
}

// SetHeader sets a request header passed to the next handler.
func (*Request) SetHeader(name, value string) {
	hostSetRequestHeader(strPtr(name), uint32(len(name)), strPtr(value), uint32(len(value)))
}

// SetResponseHeader sets a response header (convenience on Request).
func (*Request) SetResponseHeader(name, value string) { SetResponseHeader(name, value) }

// Body returns the buffered request body (up to the configured limit).
func (*Request) Body() []byte { return readInto(hostReadRequestBody) }

// Config returns the plugin's configuration as a JSON object (the [plugins.NAME]
// config table), or an empty slice when none is set.
func (*Request) Config() []byte { return readInto(hostGetConfig) }

// ---- exported entry point -------------------------------------------------

//go:wasmexport handle_request
func handleRequest() uint32 {
	if Handle == nil {
		return uint32(Continue)
	}
	return uint32(Handle(&Request{}))
}
