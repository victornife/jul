// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInspectionResponses(t *testing.T) {
	cases := []struct {
		name        string
		major       int
		contentType string
		write       func(http.ResponseWriter, *http.Request)
		status      int
		grpcStatus  string
		conn        string
		retry       string
	}{
		{"overload h1 closes", 1, "", WriteOverloaded, 503, "", "close", "1"},
		{"overload h2", 2, "text/plain", WriteOverloaded, 503, "", "", "1"},
		{"overload grpc", 2, "application/grpc+proto", WriteOverloaded, 200, "14", "", "1"},
		{"overload grpc-web reads HTTP status", 2, "application/grpc-web+proto", WriteOverloaded, 503, "", "", "1"},
		{"overload grpc content type over h1", 1, "application/grpc", WriteOverloaded, 503, "", "close", "1"},
		{"timeout h1 closes", 1, "", WriteBodyTimeout, 408, "", "close", ""},
		{"timeout h3 keeps connection", 3, "", WriteBodyTimeout, 408, "", "", ""},
		{"timeout grpc", 2, "application/grpc", WriteBodyTimeout, 200, "4", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.ProtoMajor, r.Proto = tc.major, fmt.Sprintf("HTTP/%d", tc.major)
			if tc.contentType != "" {
				r.Header.Set("Content-Type", tc.contentType)
			}
			w := httptest.NewRecorder()
			w.Header().Set("Content-Length", "99")
			tc.write(w, r)
			h := w.Result().Header
			if w.Code != tc.status || h.Get("Grpc-Status") != tc.grpcStatus ||
				h.Get("Connection") != tc.conn || h.Get("Retry-After") != tc.retry {
				t.Fatalf("status %d grpc %q conn %q retry %q", w.Code, h.Get("Grpc-Status"), h.Get("Connection"), h.Get("Retry-After"))
			}
			if tc.grpcStatus != "" && (w.Body.Len() != 0 || h.Get("Content-Length") != "") {
				t.Fatalf("gRPC answer must be trailers-only: body %q length %q", w.Body.String(), h.Get("Content-Length"))
			}
		})
	}
}

func TestEvaluationQueueRefusalsAreOverload(t *testing.T) {
	for _, outcome := range []Outcome{EvaluationSaturated, QueueExhausted, WaitExpired} {
		if !IsOverload(&AdmissionError{Outcome: outcome}) {
			t.Errorf("%s is not classified as overload", outcome)
		}
	}
	if IsOverload(&AdmissionError{Outcome: Cancelled}) {
		t.Fatal("cancellation must not be reported as overload")
	}
}
