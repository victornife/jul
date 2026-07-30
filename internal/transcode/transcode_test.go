// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestHTTPStatusFromCode(t *testing.T) {
	cases := map[codes.Code]int{
		codes.OK:                 http.StatusOK,
		codes.InvalidArgument:    http.StatusBadRequest,
		codes.NotFound:           http.StatusNotFound,
		codes.AlreadyExists:      http.StatusConflict,
		codes.PermissionDenied:   http.StatusForbidden,
		codes.Unauthenticated:    http.StatusUnauthorized,
		codes.ResourceExhausted:  http.StatusTooManyRequests,
		codes.Unimplemented:      http.StatusNotImplemented,
		codes.Unavailable:        http.StatusServiceUnavailable,
		codes.DeadlineExceeded:   http.StatusGatewayTimeout,
		codes.Internal:           http.StatusInternalServerError,
		codes.FailedPrecondition: http.StatusBadRequest,
	}
	for code, want := range cases {
		if got := httpStatusFromCode(code); got != want {
			t.Errorf("httpStatusFromCode(%v) = %d, want %d", code, got, want)
		}
	}
}

// echoFileDescriptorProto builds the descriptor for a tiny annotated echo
// service equivalent to:
//
//	package echo;
//	message EchoRequest { string message = 1; string id = 2; }
//	message EchoReply   { string message = 1; }
//	service EchoService {
//	  rpc Echo(EchoRequest) returns (EchoReply) {
//	    option (google.api.http) = {
//	      post: "/v1/echo" body: "*"
//	      additional_bindings { get: "/v1/echo/{id}" }
//	    };
//	  }
//	}
func echoFileDescriptorProto(t testing.TB) *descriptorpb.FileDescriptorProto {
	t.Helper()
	strField := func(name string, num int32) *descriptorpb.FieldDescriptorProto {
		return &descriptorpb.FieldDescriptorProto{
			Name:     proto.String(name),
			Number:   proto.Int32(num),
			Label:    descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL.Enum(),
			Type:     descriptorpb.FieldDescriptorProto_TYPE_STRING.Enum(),
			JsonName: proto.String(name),
		}
	}
	methodOpts := &descriptorpb.MethodOptions{}
	proto.SetExtension(methodOpts, annotations.E_Http, &annotations.HttpRule{
		Pattern: &annotations.HttpRule_Post{Post: "/v1/echo"},
		Body:    "*",
		AdditionalBindings: []*annotations.HttpRule{
			{Pattern: &annotations.HttpRule_Get{Get: "/v1/echo/{id}"}},
		},
	})
	return &descriptorpb.FileDescriptorProto{
		Name:       proto.String("echo/echo.proto"),
		Package:    proto.String("echo"),
		Syntax:     proto.String("proto3"),
		Dependency: []string{"google/api/annotations.proto"},
		MessageType: []*descriptorpb.DescriptorProto{
			{Name: proto.String("EchoRequest"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1), strField("id", 2)}},
			{Name: proto.String("EchoReply"), Field: []*descriptorpb.FieldDescriptorProto{strField("message", 1)}},
		},
		Service: []*descriptorpb.ServiceDescriptorProto{{
			Name: proto.String("EchoService"),
			Method: []*descriptorpb.MethodDescriptorProto{{
				Name:       proto.String("Echo"),
				InputType:  proto.String(".echo.EchoRequest"),
				OutputType: proto.String(".echo.EchoReply"),
				Options:    methodOpts,
			}},
		}},
	}
}

// startEchoServer registers a dynamic gRPC echo service backed by the given
// message descriptors and serves it on a local port. When withReflection is
// set, the file is registered globally and gRPC server reflection is enabled.
func startEchoServer(t testing.TB, fd protoreflect.FileDescriptor, withReflection bool) string {
	t.Helper()
	reqDesc := fd.Services().Get(0).Methods().Get(0).Input()
	respDesc := fd.Services().Get(0).Methods().Get(0).Output()

	handler := func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := dynamicpb.NewMessage(reqDesc)
		if err := dec(in); err != nil {
			return nil, err
		}
		m := in.ProtoReflect()
		message := m.Get(reqDesc.Fields().ByName("message")).String()
		id := m.Get(reqDesc.Fields().ByName("id")).String()

		var text string
		switch {
		case message == "fail":
			return nil, status.Error(codes.NotFound, "item not found")
		case message == "unavailable":
			return nil, status.Error(codes.Unavailable, "backend down")
		case message == "whoami":
			if md, ok := metadata.FromIncomingContext(ctx); ok {
				if v := md.Get("authorization"); len(v) > 0 {
					text = v[0]
				}
			}
		case id != "":
			text = "id=" + id + " msg=" + message
		default:
			text = message
		}

		reply := dynamicpb.NewMessage(respDesc)
		reply.ProtoReflect().Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString(text))
		return reply, nil
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*any)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: handler}},
		Metadata:    "echo/echo.proto",
	}, nil)

	if withReflection {
		if _, err := protoregistry.GlobalFiles.FindFileByPath("echo/echo.proto"); err != nil {
			if err := protoregistry.GlobalFiles.RegisterFile(fd); err != nil {
				t.Fatalf("register echo file globally: %v", err)
			}
		}
		reflection.Register(srv)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// newEchoTranscoder builds descriptors, starts the echo server, and returns a
// Transcoder pointed at it. When reflect is true it omits the descriptor file
// and uses server reflection instead.
func newEchoTranscoder(t testing.TB, reflect bool) *Transcoder {
	t.Helper()
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, reflect)

	cfg := config.GRPCTranscodeConfig{Target: addr}
	if reflect {
		cfg.UseReflection = true
	} else {
		descFile := filepath.Join(t.TempDir(), "echo.pb")
		raw, err := proto.Marshal(set)
		if err != nil {
			t.Fatalf("marshal set: %v", err)
		}
		if err := os.WriteFile(descFile, raw, 0o600); err != nil {
			t.Fatalf("write descriptor: %v", err)
		}
		cfg.DescriptorSet = descFile
	}

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-echo",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

func doRequest(t *testing.T, tr *Transcoder, method, target, body string, headers map[string]string) (*http.Response, string) {
	t.Helper()
	var rdr *strings.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	} else {
		rdr = strings.NewReader("")
	}
	req := httptest.NewRequest(method, target, rdr)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	tr.ServeHTTP(rec, req)
	res := rec.Result()
	return res, rec.Body.String()
}

func replyMessage(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("decode reply %q: %v", body, err)
	}
	s, _ := m["message"].(string)
	return s
}

func TestTranscodePostBody(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"hi"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "hi" {
		t.Errorf("reply message = %q, want %q", got, "hi")
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}
}

func TestTranscodeGetPathVar(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodGet, "/v1/echo/77", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "id=77 msg=" {
		t.Errorf("reply message = %q, want %q", got, "id=77 msg=")
	}
}

func TestTranscodeNotFoundRoute(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, _ := doRequest(t, tr, http.MethodGet, "/v1/nope", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %q, want application/problem+json", ct)
	}
}

func TestTranscodeGRPCErrorMapped(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"fail"}`, nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (grpc NotFound); body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "item not found") {
		t.Errorf("body %q missing grpc status message", body)
	}
}

func TestTranscodeBodyTooLarge(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	tr.maxMsg = 16
	big := `{"message":"` + strings.Repeat("x", 64) + `"}`
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", big, nil)
	if res.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", res.StatusCode, body)
	}
}

func TestTranscodeForwardsAuthorization(t *testing.T) {
	tr := newEchoTranscoder(t, false)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"whoami"}`,
		map[string]string{"Authorization": "Bearer xyz"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "Bearer xyz" {
		t.Errorf("forwarded authorization = %q, want %q", got, "Bearer xyz")
	}
}

func TestTranscodeViaReflection(t *testing.T) {
	tr := newEchoTranscoder(t, true)
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"reflected"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "reflected" {
		t.Errorf("reply message = %q, want %q", got, "reflected")
	}
}

// TestTranscodeReflectionRejectsUnreflectiveBackend proves that requesting
// reflection-based transcoding against a backend that does not serve the gRPC
// reflection API fails cleanly at construction — a bounded error, not a hang or
// a partially-built transcoder. This is the negative side of the reflection
// path: an operator who enables use_reflection against a server without
// reflection (or an untrusted/descriptor-less endpoint) gets a clear failure
// instead of a silently empty route table (Finding QA-1 / SEC gRPC hardening).
func TestTranscodeReflectionRejectsUnreflectiveBackend(t *testing.T) {
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	// Serve the echo service WITHOUT registering the reflection API.
	addr := startEchoServer(t, fd, false)

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-echo-noreflect",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	// A short reflect timeout bounds the test even if the backend stalls.
	tr, err := New(
		context.Background(),
		config.GRPCTranscodeConfig{Target: addr, UseReflection: true},
		pool,
		nil,
		Options{reflectTimeout: 3 * time.Second},
	)
	if err == nil {
		if tr != nil {
			_ = tr.Close()
		}
		t.Fatal("New with use_reflection against a non-reflective backend: got nil error, want failure")
	}
	if tr != nil {
		t.Errorf("New returned a non-nil transcoder alongside an error: %v", err)
	}
	if !strings.Contains(err.Error(), "grpc_transcode") {
		t.Errorf("error = %q, want it to identify the grpc_transcode target", err)
	}
}

func TestTranscodePassiveHealthMarking(t *testing.T) {
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, false)

	cfg := config.GRPCTranscodeConfig{Target: addr}
	descFile := filepath.Join(t.TempDir(), "echo.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}
	cfg.DescriptorSet = descFile

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-health",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    2,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Application-level NotFound should NOT mark failure.
	for i := 0; i < 3; i++ {
		res, _ := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"fail"}`, nil)
		if res.StatusCode != http.StatusNotFound {
			t.Fatalf("NotFound request %d: status = %d, want 404", i, res.StatusCode)
		}
	}
	// Backend should still be available after 3 NotFound errors.
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"hi"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("after NotFound errors: status = %d, body = %s", res.StatusCode, body)
	}

	// Backend Unavailable SHOULD mark failure and trigger cooldown after MaxFails.
	for i := 0; i < 2; i++ {
		res, _ := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"unavailable"}`, nil)
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("Unavailable request %d: status = %d, want 503", i, res.StatusCode)
		}
	}
	// After 2 consecutive Unavailable errors (MaxFails=2), the backend is in cooldown.
	res, body = doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"unavailable"}`, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("post-cooldown status = %d, want 503; body = %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "no available gRPC backend") {
		t.Fatalf("expected cooldown pick failure, got body: %s", body)
	}
}

// TestTranscoderEvictsStaleConnections (R10-06) verifies that when a backend
// address is removed from the upstream pool, the transcoder eventually closes
// the cached gRPC connection for that address instead of keeping it open until
// the handler generation is replaced.
// setRetiredGraceForTest overrides the retired-connection grace period for the
// duration of a test.
func setRetiredGraceForTest(t testing.TB, d time.Duration) {
	t.Helper()
	old := retiredConnGrace
	retiredConnGrace = d
	t.Cleanup(func() { retiredConnGrace = old })
}

func TestTranscoderEvictsStaleConnections(t *testing.T) {
	setRetiredGraceForTest(t, 50*time.Millisecond)
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, false)

	descFile := filepath.Join(t.TempDir(), "echo.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-evict",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cfg := config.GRPCTranscodeConfig{Target: addr, DescriptorSet: descFile}
	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Warm the connection cache.
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"hi"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("warmup request: status = %d, body = %s", res.StatusCode, body)
	}

	conn, err := tr.connFor(addr)
	if err != nil {
		t.Fatalf("connFor after warmup: %v", err)
	}
	if conn.GetState() == connectivity.Shutdown {
		t.Fatal("cached connection is already shutdown before eviction")
	}

	// Remove the backend from the pool, then trigger eviction synchronously.
	pool.UpdateBackends(nil)
	tr.evictStaleConns()

	// The connection should be retired, not closed, so in-flight streams can
	// continue during backend churn (R11-05).
	if state := conn.GetState(); state == connectivity.Shutdown {
		t.Fatal("stale connection was closed immediately, want retired grace")
	}

	// After the grace period expires the connection is closed.
	time.Sleep(100 * time.Millisecond)
	tr.evictStaleConns()

	if state := conn.GetState(); state != connectivity.Shutdown {
		t.Fatalf("stale connection state = %v, want Shutdown after grace", state)
	}

	// The cached entry should also be gone: a subsequent connFor dials a new
	// connection (the backend server is still running, so dial succeeds).
	conn2, err := tr.connFor(addr)
	if err != nil {
		t.Fatalf("connFor after eviction: %v", err)
	}
	if conn2 == conn {
		t.Fatal("evicted connection was reused")
	}
}

// startSlowEchoServer is like startEchoServer but sleeps for d on requests
// whose message field equals "slow". This lets a test remove the backend from
// the pool while a request is still using the cached connection.
func startSlowEchoServer(t testing.TB, fd protoreflect.FileDescriptor, d time.Duration) string {
	t.Helper()
	reqDesc := fd.Services().Get(0).Methods().Get(0).Input()
	respDesc := fd.Services().Get(0).Methods().Get(0).Output()

	handler := func(_ any, ctx context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		in := dynamicpb.NewMessage(reqDesc)
		if err := dec(in); err != nil {
			return nil, err
		}
		message := in.ProtoReflect().Get(reqDesc.Fields().ByName("message")).String()
		if message == "slow" {
			time.Sleep(d)
		}

		reply := dynamicpb.NewMessage(respDesc)
		reply.ProtoReflect().Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString(message))
		return reply, nil
	}

	srv := grpc.NewServer()
	srv.RegisterService(&grpc.ServiceDesc{
		ServiceName: "echo.EchoService",
		HandlerType: (*any)(nil),
		Methods:     []grpc.MethodDesc{{MethodName: "Echo", Handler: handler}},
		Metadata:    "echo/echo.proto",
	}, nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

// TestTranscoderRetiresStaleConnectionsDuringRequest (R11-05) verifies that a
// request already holding a connection can finish after the backend is removed
// from the pool, because the connection is retired with a grace period instead
// of being closed immediately.
func TestTranscoderRetiresStaleConnectionsDuringRequest(t *testing.T) {
	setRetiredGraceForTest(t, 200*time.Millisecond)

	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startSlowEchoServer(t, fd, 300*time.Millisecond)

	descFile := filepath.Join(t.TempDir(), "echo.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-retire",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cfg := config.GRPCTranscodeConfig{Target: addr, DescriptorSet: descFile}
	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Start a slow request. It will pick the backend and conn before we remove
	// the backend from the pool.
	result := make(chan *http.Response, 1)
	go func() {
		res, _ := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"slow"}`, nil)
		result <- res
	}()

	// Give the goroutine time to pick a backend and start the RPC.
	time.Sleep(50 * time.Millisecond)

	// Remove the backend and evict while the request is in flight.
	pool.UpdateBackends(nil)
	tr.evictStaleConns()

	// The in-flight request must complete because its connection was retired,
	// not closed.
	select {
	case res := <-result:
		if res.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(res.Body)
			t.Fatalf("in-flight request failed: status = %d, body = %s", res.StatusCode, string(body))
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight request did not complete during retired grace")
	}

	// Once the grace period expires the retired connection is closed.
	time.Sleep(250 * time.Millisecond)
	tr.evictStaleConns()
}

// staticDiscoverer is a test discovery provider that returns a fixed target
// set. It lets acceptance tests build a discovery-only upstream without
// touching real DNS/Consul infrastructure.
type staticDiscoverer struct {
	targets []upstream.Target
	err     error
}

func (d staticDiscoverer) Resolve(context.Context) ([]upstream.Target, error) {
	return d.targets, d.err
}

func (d staticDiscoverer) Describe() string { return "static" }

// TestTranscodeReflectionWithDiscoveryUpstream (R11-04) verifies that a
// grpc_transcode location using server reflection can be backed by a
// discovery-only upstream. The reflection RPC must see the resolved target
// from the candidate-generation snapshot rather than an empty static seed.
func TestTranscodeReflectionWithDiscoveryUpstream(t *testing.T) {
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, true)

	reg := upstream.NewRegistry(upstream.RegistryOptions{
		NewDiscoverer: func(config.DiscoveryConfig, upstream.DialFunc) (upstream.Discoverer, error) {
			return staticDiscoverer{targets: []upstream.Target{{Address: addr, Weight: 1}}}, nil
		},
	})
	up := config.UpstreamConfig{
		Name: "echo",
		Discovery: &config.DiscoveryConfig{
			Type:    "dns", // ignored by the test discoverer, but marks pool dynamic
			Refresh: config.Duration(time.Minute),
		},
	}

	reg.Begin()
	pool, err := reg.For(up, "http")
	if err != nil {
		t.Fatalf("registry.For: %v", err)
	}
	snap := reg.CandidateSnapshot("echo", "http")
	if snap == nil {
		t.Fatal("CandidateSnapshot for discovery upstream is nil")
	}
	if _, err := snap.Pick(); err != nil {
		t.Fatalf("CandidateSnapshot has no backends: %v", err)
	}
	reg.Commit()
	reg.Activate()
	t.Cleanup(reg.CloseAll)

	cfg := config.GRPCTranscodeConfig{
		Target:        "echo",
		UseReflection: true,
	}
	tr, err := New(context.Background(), cfg, pool, snap, Options{})
	if err != nil {
		t.Fatalf("New transcoder with discovery reflection: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"discovery"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("discovery reflection request: status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "discovery" {
		t.Fatalf("discovery reflection reply = %q, want discovery", got)
	}
}

// TestTranscodeReflectionWithReusedDiscoveryUpstream (R12-01) verifies that a
// discovery-only upstream still provides candidate backends for reflection
// after the registry reuses the pool across a reload.
func TestTranscodeReflectionWithReusedDiscoveryUpstream(t *testing.T) {
	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, true)

	reg := upstream.NewRegistry(upstream.RegistryOptions{
		NewDiscoverer: func(config.DiscoveryConfig, upstream.DialFunc) (upstream.Discoverer, error) {
			return staticDiscoverer{targets: []upstream.Target{{Address: addr, Weight: 1}}}, nil
		},
	})
	up := config.UpstreamConfig{
		Name: "echo",
		Discovery: &config.DiscoveryConfig{
			Type:    "dns",
			Refresh: config.Duration(time.Minute),
		},
	}

	// First build: pool is created and discovery seeds its backends.
	reg.Begin()
	pool, err := reg.For(up, "http")
	if err != nil {
		t.Fatalf("first registry.For: %v", err)
	}
	reg.Commit()
	reg.Activate()

	// Second build: the pool is reused. CandidateSnapshot must still contain
	// the discovered backends, not the empty static servers list.
	reg.Begin()
	pool2, err := reg.For(up, "http")
	if err != nil {
		t.Fatalf("second registry.For: %v", err)
	}
	if pool2 != pool {
		t.Fatal("expected pool to be reused across reload")
	}
	snap := reg.CandidateSnapshot("echo", "http")
	if snap == nil {
		t.Fatal("CandidateSnapshot for reused discovery upstream is nil")
	}
	if _, err := snap.Pick(); err != nil {
		t.Fatalf("CandidateSnapshot for reused discovery upstream has no backends: %v", err)
	}
	reg.Commit()
	reg.Activate()
	t.Cleanup(reg.CloseAll)

	cfg := config.GRPCTranscodeConfig{
		Target:        "echo",
		UseReflection: true,
	}
	tr, err := New(context.Background(), cfg, pool, snap, Options{})
	if err != nil {
		t.Fatalf("New transcoder with reused discovery reflection: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"reused"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("reused discovery reflection request: status = %d, body = %s", res.StatusCode, body)
	}
	if got := replyMessage(t, body); got != "reused" {
		t.Fatalf("reused discovery reflection reply = %q, want reused", got)
	}
}

// TestTranscoderRetiredConnectionReappearsUsable (R12-02) verifies that when a
// removed backend reappears during the retirement grace period, the cached
// connection is promoted back to the active map and remains usable. It must
// not be closed by the promotion path itself.
func TestTranscoderRetiredConnectionReappearsUsable(t *testing.T) {
	setRetiredGraceForTest(t, 200*time.Millisecond)

	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, false)

	descFile := filepath.Join(t.TempDir(), "echo.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-reappear",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cfg := config.GRPCTranscodeConfig{Target: addr, DescriptorSet: descFile}
	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Warm the connection cache.
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"warm"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("warmup request: status = %d, body = %s", res.StatusCode, body)
	}

	conn, err := tr.connFor(addr)
	if err != nil {
		t.Fatalf("connFor after warmup: %v", err)
	}
	if conn.GetState() == connectivity.Shutdown {
		t.Fatal("cached connection is already shutdown before eviction")
	}

	// Remove the backend and retire the connection.
	pool.UpdateBackends(nil)
	tr.evictStaleConns()
	if conn.GetState() == connectivity.Shutdown {
		t.Fatal("connection was closed immediately, want retired grace")
	}

	// Re-add the backend before the grace period expires.
	pool.UpdateBackends([]config.UpstreamServer{{Address: addr, Weight: 1}})
	tr.evictStaleConns()

	// Send multiple requests. The original connection must still be usable;
	// the promotion path must not have closed it.
	for i := 0; i < 5; i++ {
		res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"reappear"}`, nil)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("request %d after reappearance: status = %d, body = %s", i, res.StatusCode, body)
		}
		if got := replyMessage(t, body); got != "reappear" {
			t.Fatalf("request %d reply = %q, want reappear", i, got)
		}
	}

	if state := conn.GetState(); state == connectivity.Shutdown {
		t.Fatal("promoted connection ended up in Shutdown")
	}
}

// TestTranscoderRetiredConnectionConcurrentReappearance (R13-01) verifies that
// when a removed backend reappears and many requests race to promote the same
// retired connection, none of them close the shared connection. This exercises
// the LoadOrStore race that a sequential test cannot catch.
func TestTranscoderRetiredConnectionConcurrentReappearance(t *testing.T) {
	setRetiredGraceForTest(t, 500*time.Millisecond)

	fdp := echoFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}

	addr := startEchoServer(t, fd, false)

	descFile := filepath.Join(t.TempDir(), "echo.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "test-concurrent-reappear",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create test pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	cfg := config.GRPCTranscodeConfig{Target: addr, DescriptorSet: descFile}
	tr, err := New(context.Background(), cfg, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	// Warm the connection cache and capture it.
	res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"warm"}`, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("warmup request: status = %d, body = %s", res.StatusCode, body)
	}
	conn, err := tr.connFor(addr)
	if err != nil {
		t.Fatalf("connFor after warmup: %v", err)
	}

	// Remove the backend, retire the connection, then re-add the backend.
	pool.UpdateBackends(nil)
	tr.evictStaleConns()
	pool.UpdateBackends([]config.UpstreamServer{{Address: addr, Weight: 1}})

	// Release many requests through a barrier so they race through connFor.
	const n = 32
	barrier := make(chan struct{})
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			<-barrier
			res, body := doRequest(t, tr, http.MethodPost, "/v1/echo", `{"message":"concurrent"}`, nil)
			if res.StatusCode != http.StatusOK {
				errs <- fmt.Errorf("request %d: status = %d, body = %s", i, res.StatusCode, body)
				return
			}
			if got := replyMessage(t, body); got != "concurrent" {
				errs <- fmt.Errorf("request %d: reply = %q, want concurrent", i, got)
				return
			}
			errs <- nil
		}(i)
	}
	close(barrier)

	for i := 0; i < n; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}

	if state := conn.GetState(); state == connectivity.Shutdown {
		t.Fatal("shared promoted connection ended up in Shutdown")
	}
}
