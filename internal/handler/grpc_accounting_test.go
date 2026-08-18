// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"jul/internal/config"
	"jul/internal/upstream"
)

// startAdmittedGRPCFront serves the native gRPC passthrough for backendAddr
// under the supplied pool policy, returning the front address and the pool's
// admission owner.
func startAdmittedGRPCFront(t testing.TB, backendAddr string, r *config.ResilienceConfig) (string, *upstream.Admission) {
	t.Helper()
	ups := map[string]config.UpstreamConfig{
		"grpcapi": {
			Name:       "grpcapi",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: backendAddr, Weight: 1}},
			MaxFails:   3,
			Resilience: r,
		},
	}
	loc := config.LocationConfig{ProxyPass: "http://grpcapi", GRPC: true}
	h, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, ups, nil, grpcTestLogger(), nil)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}
	ah, ok := h.(*admittedHandler)
	if !ok {
		t.Fatalf("NewGRPCProxy returned %T, want *admittedHandler: native gRPC must acquire admission", h)
	}
	t.Cleanup(func() { _ = ah.Close() })

	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	srv := &http.Server{Handler: ah}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })
	return front.Addr().String(), ah.admission
}

// TestGRPCAccountingUnary pins the gRPC unary row: one slot per call, released
// when the call completes.
func TestGRPCAccountingUnary(t *testing.T) {
	backend := startGRPCEcho(t)
	front, adm := startAdmittedGRPCFront(t, backend, &config.ResilienceConfig{MaxActiveRequests: 4})
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		var reply []byte
		if err := conn.Invoke(ctx, "/echo.Echo/Unary", []byte("ping"), &reply); err != nil {
			t.Fatalf("invoke %d: %v", i, err)
		}
	}
	waitAdmissionIdle(t, adm)
}

// TestGRPCAccountingStreamHoldsSlotForLifetime pins the streaming rows. A
// server, client or bidirectional stream is one logical request for its whole
// lifetime, so the slot is held from the first frame until the stream closes —
// not released when the response headers arrive.
//
// The echo backend is bidirectional, which is the strictest of the three shapes:
// it stays open until the client closes its send side.
func TestGRPCAccountingStreamHoldsSlotForLifetime(t *testing.T) {
	backend := startGRPCEcho(t)
	front, adm := startAdmittedGRPCFront(t, backend, &config.ResilienceConfig{MaxActiveRequests: 4})
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Bidi")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := stream.SendMsg([]byte("frame-1")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := stream.RecvMsg(&got); err != nil {
		t.Fatalf("recv: %v", err)
	}
	if string(got) != "frame-1" {
		t.Fatalf("echo = %q", got)
	}

	// Frames have flowed in both directions and the stream is still open.
	if n := adm.Active(); n != 1 {
		t.Fatalf("active during an open stream = %d, want 1", n)
	}
	// A second concurrent stream on the SAME HTTP/2 connection takes its own
	// slot: the request limit counts streams, not sockets.
	second, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Bidi")
	if err != nil {
		t.Fatalf("new second stream: %v", err)
	}
	if err := second.SendMsg([]byte("frame-2")); err != nil {
		t.Fatalf("send on second: %v", err)
	}
	var got2 []byte
	if err := second.RecvMsg(&got2); err != nil {
		t.Fatalf("recv on second: %v", err)
	}
	if n := adm.Active(); n != 2 {
		t.Fatalf("active with two concurrent streams = %d, want 2", n)
	}

	_ = stream.CloseSend()
	_ = second.CloseSend()
	for _, s := range []grpc.ClientStream{stream, second} {
		var drain []byte
		for s.RecvMsg(&drain) == nil {
		}
	}
	waitAdmissionIdle(t, adm)
}

// TestGRPCAccountingOverLimitIsUnavailable pins the client-facing contract for
// an overloaded gRPC route.
//
// Overload is HTTP 503, which grpc-go maps to UNAVAILABLE — deliberately not
// RESOURCE_EXHAUSTED, because that maps back to HTTP 429 and would contradict
// the HTTP path, where overload is never the client's fault.
func TestGRPCAccountingOverLimitIsUnavailable(t *testing.T) {
	backend := startGRPCEcho(t)
	front, adm := startAdmittedGRPCFront(t, backend, &config.ResilienceConfig{MaxActiveRequests: 1})
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Hold the only slot with an open stream.
	held, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Bidi")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := held.SendMsg([]byte("hold")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var echo []byte
	if err := held.RecvMsg(&echo); err != nil {
		t.Fatalf("recv: %v", err)
	}
	waitAdmissionActive(t, adm, 1)

	var reply []byte
	err = conn.Invoke(ctx, "/echo.Echo/Unary", []byte("over"), &reply)
	if err == nil {
		t.Fatal("a call over the admission limit succeeded")
	}
	if code := status.Code(err); code != codes.Unavailable {
		t.Fatalf("code = %s, want UNAVAILABLE (overload must not be RESOURCE_EXHAUSTED, which maps to HTTP 429)", code)
	}

	_ = held.CloseSend()
	var drain []byte
	for held.RecvMsg(&drain) == nil {
	}
	waitAdmissionIdle(t, adm)
}

// TestGRPCAccountingCancelReleasesSlot pins that abandoning a stream returns its
// slot, which is the case that matters most: a client that gives up on a stalled
// backend must not consume capacity forever.
func TestGRPCAccountingCancelReleasesSlot(t *testing.T) {
	backend := startGRPCEcho(t)
	front, adm := startAdmittedGRPCFront(t, backend, &config.ResilienceConfig{MaxActiveRequests: 4})
	conn := dialGRPC(t, front)

	ctx, cancel := context.WithCancel(context.Background())
	stream, err := conn.NewStream(ctx, &grpc.StreamDesc{ServerStreams: true, ClientStreams: true}, "/echo.Echo/Bidi")
	if err != nil {
		t.Fatalf("new stream: %v", err)
	}
	if err := stream.SendMsg([]byte("x")); err != nil {
		t.Fatalf("send: %v", err)
	}
	var got []byte
	if err := stream.RecvMsg(&got); err != nil {
		t.Fatalf("recv: %v", err)
	}
	waitAdmissionActive(t, adm, 1)

	cancel()
	waitAdmissionIdle(t, adm)
}

func waitAdmissionActive(t testing.TB, a *upstream.Admission, n int64) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Active() != n {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for active=%d (have %d)", n, a.Active())
		}
		time.Sleep(time.Millisecond)
	}
}

func waitAdmissionIdle(t testing.TB, a *upstream.Admission) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for a.Active() != 0 || a.Pending() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for quiesce: active=%d pending=%d", a.Active(), a.Pending())
		}
		time.Sleep(time.Millisecond)
	}
}
