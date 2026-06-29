//go:build grpc

package transcode

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

// BenchmarkTranscodeUnaryPostBody measures the end-to-end cost of transcoding a
// POST request with a JSON body to a unary gRPC call over a loopback backend:
// route match, JSON->protobuf decode, gRPC round trip, protobuf->JSON encode.
func BenchmarkTranscodeUnaryPostBody(b *testing.B) {
	tr := newEchoTranscoder(b, false)
	const body = `{"message":"hi"}`

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/echo", strings.NewReader(body))
		rec := httptest.NewRecorder()
		tr.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkTranscodeUnaryGetPathVar measures a GET request whose request message
// is assembled entirely from a path variable (no body), exercising the
// path-template capture and scalar field-setting path.
func BenchmarkTranscodeUnaryGetPathVar(b *testing.B) {
	tr := newEchoTranscoder(b, false)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/echo/77", nil)
		rec := httptest.NewRecorder()
		tr.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}
	}
}

// BenchmarkNativeGRPCUnary is the native gRPC baseline: it invokes the same
// loopback backend method directly through the transcoder's connection, with no
// HTTP, JSON, or routing work. The delta against BenchmarkTranscodeUnaryPostBody
// is the transcoding overhead over a native gRPC call on the same loopback.
func BenchmarkNativeGRPCUnary(b *testing.B) {
	tr := newEchoTranscoder(b, false)
	rt := tr.routes[0] // POST /v1/echo -> echo.EchoService.Echo (unary)

	conn, err := tr.firstConn()
	if err != nil {
		b.Fatalf("get connection: %v", err)
	}

	req := dynamicpb.NewMessage(rt.method.Input())
	req.ProtoReflect().Set(
		rt.method.Input().Fields().ByName("message"),
		protoreflect.ValueOfString("hi"),
	)
	path := grpcMethodPath(rt.method)
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		resp := dynamicpb.NewMessage(rt.method.Output())
		if err := conn.Invoke(ctx, path, req, resp); err != nil {
			b.Fatalf("invoke: %v", err)
		}
	}
}

// BenchmarkPathTemplateMatch isolates the routing hot path: matching a request
// path against a compiled google.api.http template and capturing its variable.
func BenchmarkPathTemplateMatch(b *testing.B) {
	tmpl, err := parseTemplate("/v1/shelves/{shelf}/books/{book}")
	if err != nil {
		b.Fatalf("parseTemplate: %v", err)
	}
	const path = "/v1/shelves/42/books/99"

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := tmpl.match(path); !ok {
			b.Fatal("template did not match")
		}
	}
}
