// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"errors"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"jul/internal/upstream"
)

// Client-facing outcome names (ADR 0032 §5). They are bounded labels, never
// derived from request data.
const (
	ReasonOverloaded  = "inspection_overloaded"
	ReasonBodyTimeout = "body_timeout"
)

var (
	// ErrBodyTimeout reports that a request body did not finish arriving
	// before its inspection body_timeout deadline in enforce mode.
	ErrBodyTimeout = errors.New("inspection: request body arrival deadline exceeded")
	// ErrDeadlineUnsupported reports that enforce mode required a request
	// read deadline the response writer cannot apply.
	ErrDeadlineUnsupported = errors.New("inspection: request read deadline unsupported")
)

// IsOverload reports whether err is a governor admission refusal. HTTP clients
// receive 503; native gRPC clients receive UNAVAILABLE. Observe mode never
// returns one.
func IsOverload(err error) bool {
	return IsOutcome(err, ByteSaturated) || IsOutcome(err, WAFStateSaturated) ||
		IsOutcome(err, EvaluationSaturated) || IsOutcome(err, QueueExhausted) ||
		IsOutcome(err, WaitExpired) || IsOutcome(err, AuditSaturated)
}

// IsGRPCRequest reports whether r is a native gRPC request over HTTP/2 or
// HTTP/3. gRPC-Web uses HTTP status codes and is intentionally excluded.
func IsGRPCRequest(r *http.Request) bool {
	if r == nil || r.ProtoMajor < 2 {
		return false
	}
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return false
	}
	return mt == "application/grpc" || (strings.HasPrefix(mt, "application/grpc+") && !strings.HasPrefix(mt, "application/grpc-web"))
}

// WriteOverloaded answers an inspection overload: 503 with Retry-After: 1,
// closing an HTTP/1 connection whose request body may be unread, or a
// trailers-only UNAVAILABLE for a gRPC request. Without the close, net/http
// would first drain a stalled upload and the 503 might never arrive.
func WriteOverloaded(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "1")
	if isGRPC(r) {
		writeGRPCStatus(w, upstream.GRPCCodeUnavailable, ReasonOverloaded)
		return
	}
	if r != nil && r.ProtoMajor < 2 {
		w.Header().Set("Connection", "close")
	}
	http.Error(w, ReasonOverloaded, http.StatusServiceUnavailable)
}

// WriteBodyTimeout answers an expired body-arrival deadline: 408, closing an
// HTTP/1 connection whose request body was not fully read, or a trailers-only
// DEADLINE_EXCEEDED for a gRPC request.
func WriteBodyTimeout(w http.ResponseWriter, r *http.Request) {
	if isGRPC(r) {
		writeGRPCStatus(w, upstream.GRPCCodeDeadlineExceeded, ReasonBodyTimeout)
		return
	}
	if r.ProtoMajor < 2 {
		w.Header().Set("Connection", "close")
	}
	http.Error(w, ReasonBodyTimeout, http.StatusRequestTimeout)
}

// isGRPC matches gRPC over HTTP/2 or HTTP/3, not gRPC-Web, whose clients read
// the HTTP status.
func isGRPC(r *http.Request) bool {
	return IsGRPCRequest(r)
}

// writeGRPCStatus sends a trailers-only gRPC response: the status travels in
// the header block that ends the stream.
func writeGRPCStatus(w http.ResponseWriter, code uint32, msg string) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Content-Type", "application/grpc")
	h.Set("Grpc-Status", strconv.FormatUint(uint64(code), 10))
	h.Set("Grpc-Message", msg)
	w.WriteHeader(http.StatusOK)
}
