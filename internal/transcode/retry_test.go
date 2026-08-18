// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/api/annotations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"jul/internal/config"
	"jul/internal/upstream"
)

// TestTranscodedUnaryRetriesUnavailable pins the property this slice exists
// for: a backend that cannot take the call costs an attempt, not the request.
// It also pins backend exclusion — the failing backend must be tried once, not
// repeatedly, or "retry" would just mean "ask the same broken server again".
func TestTranscodedUnaryRetriesUnavailable(t *testing.T) {
	badAddr, badHits := startEchoBackend(t, codes.Unavailable)
	goodAddr, goodHits := startEchoBackend(t, codes.OK)

	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), badAddr, goodAddr)

	res, body := doRequest(t, tr, http.MethodGet, "/v1/echo/abc", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retrying past the unavailable backend: %s", res.StatusCode, body)
	}
	if got := badHits.Load(); got != 1 {
		t.Fatalf("unavailable backend was called %d times, want exactly 1: the failed backend must be excluded from re-selection", got)
	}
	if got := goodHits.Load(); got != 1 {
		t.Fatalf("healthy backend was called %d times, want 1", got)
	}
}

// TestTranscodedUnaryDoesNotRetryApplicationErrors pins that only "this backend
// could not take the call" is retried. An InvalidArgument is the application's
// answer, and asking a second backend the same question gets the same answer
// more expensively while doubling load on a service that is already saying no.
func TestTranscodedUnaryDoesNotRetryApplicationErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		code       codes.Code
		wantStatus int
		wantCalls  int64
	}{
		{"invalid argument is terminal", codes.InvalidArgument, http.StatusBadRequest, 1},
		{"not found is terminal", codes.NotFound, http.StatusNotFound, 1},
		{"permission denied is terminal", codes.PermissionDenied, http.StatusForbidden, 1},
		{"unavailable is retried", codes.Unavailable, http.StatusServiceUnavailable, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstAddr, firstHits := startEchoBackend(t, tc.code)
			secondAddr, secondHits := startEchoBackend(t, tc.code)
			tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), firstAddr, secondAddr)

			res, _ := doRequest(t, tr, http.MethodGet, "/v1/echo/abc", "", nil)
			if res.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", res.StatusCode, tc.wantStatus)
			}
			if got := firstHits.Load() + secondHits.Load(); got != tc.wantCalls {
				t.Fatalf("backends were called %d times, want %d", got, tc.wantCalls)
			}
		})
	}
}

// TestTranscodedRetryIdempotencyMatrix walks the gate from ADR 0017: the HTTP
// binding, or the method's own declaration, may authorise a retry. The default
// IDEMPOTENCY_UNKNOWN authorises nothing, because silence is not a promise —
// that is the case most likely to be got wrong, and it is the one that would
// execute somebody's payment twice.
func TestTranscodedRetryIdempotencyMatrix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		httpMethod string
		level      descriptorpb.MethodOptions_IdempotencyLevel
		wantCalls  int64
	}{
		{"GET is retry-safe by binding", "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN, 2},
		{"PUT is retry-safe by binding", "PUT", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN, 2},
		{"DELETE is retry-safe by binding", "DELETE", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN, 2},
		{"POST with unknown idempotency is never retried", "POST", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN, 1},
		{"POST declared NO_SIDE_EFFECTS is retried", "POST", descriptorpb.MethodOptions_NO_SIDE_EFFECTS, 2},
		{"POST declared IDEMPOTENT is retried", "POST", descriptorpb.MethodOptions_IDEMPOTENT, 2},
		{"PATCH with unknown idempotency is never retried", "PATCH", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN, 1},
		{"PATCH declared IDEMPOTENT is retried", "PATCH", descriptorpb.MethodOptions_IDEMPOTENT, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			firstAddr, firstHits := startEchoBackend(t, codes.Unavailable)
			secondAddr, secondHits := startEchoBackend(t, codes.Unavailable)
			tr := transcoderOver(t, retryEchoDescriptor(t, tc.httpMethod, tc.level), firstAddr, secondAddr)

			body := ""
			if tc.httpMethod != "GET" && tc.httpMethod != "DELETE" {
				body = `{"message":"hi"}`
			}
			doRequest(t, tr, tc.httpMethod, "/v1/echo/abc", body, nil)

			if got := firstHits.Load() + secondHits.Load(); got != tc.wantCalls {
				t.Fatalf("%s at %s: backends were called %d times, want %d",
					tc.httpMethod, tc.level, got, tc.wantCalls)
			}
		})
	}
}

// TestTranscodedRetryHonoursAttemptCap pins that the shared retry_attempts
// setting reaches the transcoder rather than stopping at the HTTP proxy.
func TestTranscodedRetryHonoursAttemptCap(t *testing.T) {
	a, aHits := startEchoBackend(t, codes.Unavailable)
	b, bHits := startEchoBackend(t, codes.Unavailable)
	c, cHits := startEchoBackend(t, codes.Unavailable)

	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), a, b, c)
	tr.retry = upstream.RetryOverride{Attempts: 2}

	doRequest(t, tr, http.MethodGet, "/v1/echo/abc", "", nil)

	if got := aHits.Load() + bHits.Load() + cHits.Load(); got != 2 {
		t.Fatalf("backends were called %d times, want the configured cap of 2", got)
	}
}

// startEchoBackend serves the echo method, always answering with the given
// code. codes.OK returns a real reply. It counts calls so a test can assert how
// many attempts a request actually cost.
func startEchoBackend(t *testing.T, code codes.Code) (addr string, hits *atomic.Int64) {
	t.Helper()
	fdp := retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("echo/echo.proto")
	if err != nil {
		t.Fatalf("find echo file: %v", err)
	}
	respDesc := fd.Services().Get(0).Methods().Get(0).Output()

	var count atomic.Int64
	handler := func(_ any, _ context.Context, dec func(any) error, _ grpc.UnaryServerInterceptor) (any, error) {
		count.Add(1)
		in := dynamicpb.NewMessage(fd.Services().Get(0).Methods().Get(0).Input())
		if derr := dec(in); derr != nil {
			return nil, derr
		}
		if code != codes.OK {
			return nil, status.Error(code, code.String())
		}
		reply := dynamicpb.NewMessage(respDesc)
		reply.ProtoReflect().Set(respDesc.Fields().ByName("message"), protoreflect.ValueOfString("ok"))
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
	return lis.Addr().String(), &count
}

// retryEchoDescriptor builds the echo service bound to one HTTP method with a
// chosen idempotency level, so the gate can be exercised across the matrix.
func retryEchoDescriptor(t *testing.T, httpMethod string, level descriptorpb.MethodOptions_IdempotencyLevel) *descriptorpb.FileDescriptorProto {
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

	const path = "/v1/echo/{id}"
	rule := &annotations.HttpRule{}
	switch httpMethod {
	case "GET":
		rule.Pattern = &annotations.HttpRule_Get{Get: path}
	case "DELETE":
		rule.Pattern = &annotations.HttpRule_Delete{Delete: path}
	case "PUT":
		rule.Pattern = &annotations.HttpRule_Put{Put: path}
		rule.Body = "*"
	case "PATCH":
		rule.Pattern = &annotations.HttpRule_Patch{Patch: path}
		rule.Body = "*"
	default:
		rule.Pattern = &annotations.HttpRule_Post{Post: path}
		rule.Body = "*"
	}

	methodOpts := &descriptorpb.MethodOptions{}
	if level != descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN {
		methodOpts.IdempotencyLevel = level.Enum()
	}
	proto.SetExtension(methodOpts, annotations.E_Http, rule)

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

// transcoderOver builds a Transcoder over a pool of the given backend
// addresses. Passive health is set high so a retry test exercises the retry
// rule rather than the cooldown that would otherwise remove backends underneath
// it.
func transcoderOver(t *testing.T, fdp *descriptorpb.FileDescriptorProto, addrs ...string) *Transcoder {
	t.Helper()
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	descFile := filepath.Join(t.TempDir(), "echo.pb")
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "retry-echo",
		Strategy:    "round_robin",
		Servers:     servers,
		MaxFails:    1000,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	tr, err := New(context.Background(), config.GRPCTranscodeConfig{
		Target:        addrs[0],
		DescriptorSet: descFile,
	}, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestTranscodedStreamingIsNotRetried pins the boundary between the two gRPC
// surfaces. A streaming call cannot be retried honestly: by the time it can
// fail, framing has been written, and replaying it would deliver a second
// stream into a client already consuming the first. So a dead backend at the
// head of the pool ends a streaming request, where the unary path would have
// failed over.
func TestTranscodedStreamingIsNotRetried(t *testing.T) {
	fdp := streamingFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	files, err := filesFromSet(set)
	if err != nil {
		t.Fatalf("build descriptors: %v", err)
	}
	fd, err := files.FindFileByPath("streamecho/streamecho.proto")
	if err != nil {
		t.Fatalf("find file: %v", err)
	}
	liveAddr := startStreamEchoServer(t, fd)

	descFile := filepath.Join(t.TempDir(), "streamecho.pb")
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	// Dead backend first, live backend second: retry would turn this into a 200.
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "stream-retry",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: "127.0.0.1:1", Weight: 1}, {Address: liveAddr, Weight: 1}},
		MaxFails:    1000,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	tr, err := New(context.Background(), config.GRPCTranscodeConfig{
		Target:        liveAddr,
		DescriptorSet: descFile,
		Streaming:     true,
		StreamMode:    "ndjson",
	}, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })

	res, _ := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a,b,c"}`, nil)
	if res.StatusCode == http.StatusOK {
		t.Fatal("a streaming call failed over to a second backend; framing has already been written, so it must not be retried")
	}
}

// TestTranscodedUnaryRetriesConnectionFailure pins the other retryable case.
// A backend Jul cannot even build a connection to has taken nothing, so the
// call is still recoverable — and the failure must not be decoded as whatever
// status an absent response would produce.
func TestTranscodedUnaryRetriesConnectionFailure(t *testing.T) {
	goodAddr, goodHits := startEchoBackend(t, codes.OK)

	// "%%%" is rejected when the gRPC client is built, so connFor fails before
	// anything is invoked.
	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), "%%%", goodAddr)

	res, body := doRequest(t, tr, http.MethodGet, "/v1/echo/abc", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after retrying past the unreachable backend: %s", res.StatusCode, body)
	}
	if got := goodHits.Load(); got != 1 {
		t.Fatalf("healthy backend was called %d times, want 1", got)
	}
}

// TestTranscodedUnreachableBackendIsNotAGRPCStatus pins that a failure to build
// the connection keeps its own 502. Falling through to the gRPC status mapping
// would decode an absent response as Unknown and report 500, telling an
// operator the application failed when in fact it was never reached.
func TestTranscodedUnreachableBackendIsNotAGRPCStatus(t *testing.T) {
	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), "%%%")

	res, body := doRequest(t, tr, http.MethodGet, "/v1/echo/abc", "", nil)
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 for an unreachable backend", res.StatusCode)
	}
	if !strings.Contains(body, "grpc backend unreachable") {
		t.Fatalf("body = %q, want it to name the connection failure", body)
	}
}

// TestTranscodedStreamingUnreachableBackend covers the streaming path's own
// error mapping, which is deliberately not shared with the retrying unary path.
func TestTranscodedStreamingUnreachableBackend(t *testing.T) {
	tr := streamTranscoderOver(t, "%%%")
	res, body := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a"}`, nil)
	if res.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: %s", res.StatusCode, body)
	}
}

// TestTranscodedStreamingNoAvailableBackend pins that an exhausted pool is
// reported as 503, distinct from a backend that exists but cannot be reached.
func TestTranscodedStreamingNoAvailableBackend(t *testing.T) {
	tr := streamTranscoderOver(t, "127.0.0.1:1")
	// One failure trips the only backend, so selection itself fails.
	for _, b := range tr.pool.Backends() {
		tr.pool.MarkFailure(b)
	}
	res, body := doRequest(t, tr, http.MethodPost, "/v1/down", `{"value":"a"}`, nil)
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503: %s", res.StatusCode, body)
	}
	if !strings.Contains(body, "no available gRPC backend") {
		t.Fatalf("body = %q, want it to name backend supply rather than reachability", body)
	}
}

// streamTranscoderOver builds a streaming transcoder over a pool of the given
// addresses.
func streamTranscoderOver(t *testing.T, addrs ...string) *Transcoder {
	t.Helper()
	fdp := streamingFileDescriptorProto(t)
	set := &descriptorpb.FileDescriptorSet{File: []*descriptorpb.FileDescriptorProto{fdp}}
	raw, err := proto.Marshal(set)
	if err != nil {
		t.Fatalf("marshal set: %v", err)
	}
	descFile := filepath.Join(t.TempDir(), "streamecho.pb")
	if err := os.WriteFile(descFile, raw, 0o600); err != nil {
		t.Fatalf("write descriptor: %v", err)
	}

	servers := make([]config.UpstreamServer, 0, len(addrs))
	for _, a := range addrs {
		servers = append(servers, config.UpstreamServer{Address: a, Weight: 1})
	}
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "stream-errors",
		Strategy:    "round_robin",
		Servers:     servers,
		MaxFails:    1,
		FailTimeout: config.Duration(time.Minute),
	}, "http")
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	t.Cleanup(func() { pool.Close() })

	tr, err := New(context.Background(), config.GRPCTranscodeConfig{
		Target:        addrs[0],
		DescriptorSet: descFile,
		Streaming:     true,
		StreamMode:    "ndjson",
	}, pool, nil, Options{})
	if err != nil {
		t.Fatalf("New transcoder: %v", err)
	}
	t.Cleanup(func() { _ = tr.Close() })
	return tr
}

// TestConnCacheDropsAReplacedWorkload pins why the cache carries a logical
// identity alongside its dial key. A replacement pod at a recycled address is a
// different process; reusing the connection established to its predecessor
// would hand a stream to something that no longer exists.
func TestConnCacheDropsAReplacedWorkload(t *testing.T) {
	addr, _ := startEchoBackend(t, codes.OK)
	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), addr)

	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: addr}

	first, err := tr.connFor(key, "pod-a")
	if err != nil {
		t.Fatalf("connFor: %v", err)
	}
	again, err := tr.connFor(key, "pod-a")
	if err != nil {
		t.Fatalf("connFor: %v", err)
	}
	if again != first {
		t.Fatal("the same workload at the same address got a second connection")
	}

	replaced, err := tr.connFor(key, "pod-b")
	if err != nil {
		t.Fatalf("connFor: %v", err)
	}
	if replaced == first {
		t.Fatal("a replacement workload reused the connection dialled to its predecessor")
	}

	// The predecessor's connection is retired rather than closed: a stream
	// started against it may still be draining.
	if _, ok := tr.retired.Load(key); !ok {
		t.Fatal("the replaced connection was dropped instead of retired; an in-flight stream would have been cut")
	}
	if state := first.GetState(); state == connectivity.Shutdown {
		t.Fatal("the replaced connection was closed while it may still have been draining")
	}
}

// TestConnCacheEvictsAReplacedWorkloadOnReconcile pins the same rule on the
// level-triggered path, which is what actually runs in production: discovery
// churn inside one handler generation is noticed by the reconciler, not by a
// removal callback.
func TestConnCacheEvictsAReplacedWorkloadOnReconcile(t *testing.T) {
	addr, _ := startEchoBackend(t, codes.OK)
	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), addr)
	tr.pool.UpdateTargets([]upstream.Target{{Address: addr, ID: "pod-a"}})

	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: addr}
	if _, err := tr.connFor(key, "pod-a"); err != nil {
		t.Fatalf("connFor: %v", err)
	}
	if _, ok := tr.conns.Load(key); !ok {
		t.Fatal("precondition: the connection should be cached")
	}

	// The pod is replaced at the same address.
	tr.pool.UpdateTargets([]upstream.Target{{Address: addr, ID: "pod-b"}})
	tr.evictStaleConns()

	if _, ok := tr.conns.Load(key); ok {
		t.Fatal("the reconciler kept a connection to a workload that no longer exists")
	}
	if _, ok := tr.retired.Load(key); !ok {
		t.Fatal("the stale connection was not retired")
	}
}

// TestConnCacheKeepsAStableWorkload pins the other half: a reconcile pass must
// not churn connections for a backend that has not changed.
func TestConnCacheKeepsAStableWorkload(t *testing.T) {
	addr, _ := startEchoBackend(t, codes.OK)
	tr := transcoderOver(t, retryEchoDescriptor(t, "GET", descriptorpb.MethodOptions_IDEMPOTENCY_UNKNOWN), addr)
	tr.pool.UpdateTargets([]upstream.Target{{Address: addr, ID: "pod-a"}})

	key := upstream.BackendIdentity{Scheme: "http", Network: "tcp", Address: addr}
	conn, err := tr.connFor(key, "pod-a")
	if err != nil {
		t.Fatalf("connFor: %v", err)
	}
	tr.evictStaleConns()

	v, ok := tr.conns.Load(key)
	if !ok {
		t.Fatal("a reconcile pass evicted a live backend's connection")
	}
	if v.(*cachedConn).conn != conn {
		t.Fatal("a reconcile pass replaced a live backend's connection")
	}
}
