// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer && grpc

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"jul/internal/app"
	"jul/internal/config"
)

// startJulForGRPCCorpus starts a real Jul instance serving a native gRPC
// passthrough location. It does not reuse startRealJulForCorpus's plain-HTTP
// readiness probe, since a GET against a loc.GRPC=true action is not a
// request the gRPC passthrough handler answers the way ordinary HTTP
// locations do; readiness is instead polled with a real gRPC invocation,
// exactly as the PROXY-protocol/mTLS corpus tests each poll with their own
// real protocol traffic rather than a generic HTTP GET.
func startJulForGRPCCorpus(t *testing.T, name string, cfg *config.Config) (addr string, cleanup func(), logs *corpusLogBuffer) {
	t.Helper()
	if len(cfg.Servers) != 1 {
		t.Fatalf("%s: fixture requires exactly one server, got %d", name, len(cfg.Servers))
	}
	cfg.Servers[0].Listen = reserveLoopbackAddress(t)
	if err := app.ValidateRuntimeConfig(context.Background(), cfg); err != nil {
		t.Fatalf("%s: runtime preflight: %v", name, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	reload := make(chan struct{})
	logs = &corpusLogBuffer{}
	done := make(chan int, 1)
	go func() {
		done <- app.Serve(ctx, reload, memorySource{name: "<nginx-corpus:" + name + ">", cfg: cfg}, cfg, productName, version, app.WithLogOutput(logs))
	}()

	addr = cfg.Servers[0].Listen
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case code := <-done:
			done <- code
			t.Fatalf("%s: Jul exited during startup with code %d\nlogs:\n%s", name, code, logs.String())
		default:
		}
		probeConn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			probeConn.Close()
			lastErr = nil
			break
		}
		lastErr = err
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("%s: Jul did not become reachable before the startup deadline: %v\nlogs:\n%s", name, lastErr, logs.String())
	}

	return addr, func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("%s: Jul exit code = %d\nlogs:\n%s", name, code, logs.String())
			}
		case <-time.After(5 * time.Second):
			t.Errorf("%s: Jul did not shut down\nlogs:\n%s", name, logs.String())
		}
	}, logs
}

// grpcRawCodec is a transparent gRPC codec carrying opaque byte payloads, so
// the test client and the echo backend exchange messages without a .proto
// schema - the same technique internal/handler/grpcproxy_test.go's own unit
// tests use, reproduced here since it is unexported there.
type grpcRawCodec struct{}

func (grpcRawCodec) Marshal(v any) ([]byte, error) {
	switch b := v.(type) {
	case []byte:
		return b, nil
	case *[]byte:
		return *b, nil
	}
	return nil, fmt.Errorf("grpcRawCodec: unexpected type %T", v)
}

func (grpcRawCodec) Unmarshal(data []byte, v any) error {
	p, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("grpcRawCodec: unexpected type %T", v)
	}
	*p = append((*p)[:0], data...)
	return nil
}

func (grpcRawCodec) Name() string { return "raw" }

// grpcEchoStream echoes every received frame back to the caller and sets a
// trailer, so the test can assert that both message payloads and trailers
// (grpc-status lives alongside them) survive Jul's native gRPC passthrough.
func grpcEchoStream(_ any, stream grpc.ServerStream) error {
	stream.SetTrailer(metadata.Pairs("x-echo-trailer", "ok"))
	for {
		var msg []byte
		if err := stream.RecvMsg(&msg); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := stream.SendMsg(&msg); err != nil {
			return err
		}
	}
}

// startGRPCEchoBackend starts a real cleartext (h2c) gRPC server that echoes
// any method and returns its address.
func startGRPCEchoBackend(t *testing.T) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := grpc.NewServer(
		grpc.UnknownServiceHandler(grpcEchoStream),
		grpc.ForceServerCodec(grpcRawCodec{}),
	)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}

// TestNGINXCorpusGRPCPassRealE2E proves the grpc_pass -> proxy_pass +
// grpc = true translation (#367) actually works end to end through a real
// Jul instance and a real gRPC backend: a unary call's payload is echoed
// back unchanged, and the backend's trailer (alongside the implicit
// grpc-status) survives Jul's native HTTP/2 passthrough - not merely an
// ordinary HTTP proxy relay, which would not preserve gRPC framing/trailers.
func TestNGINXCorpusGRPCPassRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "grpc-gateway-runtime")
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 {
		t.Fatalf("grpc-gateway-runtime: want 1 server with 1 location, got %+v", cfg.Servers)
	}
	if loc := cfg.Servers[0].Locations[0]; !loc.GRPC || loc.ProxyPass == "" {
		t.Fatalf("grpc-gateway-runtime: want a native gRPC passthrough location, got %+v", loc)
	}

	backend := startGRPCEchoBackend(t)
	cfg.Servers[0].Locations[0].ProxyPass = "http://" + backend

	addr, cleanup, logs := startJulForGRPCCorpus(t, "grpc-gateway-runtime", cfg)
	defer cleanup()

	conn, err := grpc.NewClient(
		"passthrough:///"+addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.ForceCodec(grpcRawCodec{})),
	)
	if err != nil {
		t.Fatalf("dial Jul: %v", err)
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req := []byte("hello gRPC through Jul")
	var reply []byte
	var trailer metadata.MD
	if err := conn.Invoke(ctx, "/echo.Echo/Unary", req, &reply, grpc.Trailer(&trailer)); err != nil {
		t.Fatalf("invoke: %v\nlogs:\n%s", err, logs.String())
	}
	if string(reply) != string(req) {
		t.Fatalf("reply = %q, want %q", reply, req)
	}
	if got := trailer.Get("x-echo-trailer"); len(got) != 1 || got[0] != "ok" {
		t.Fatalf("trailer x-echo-trailer = %v, want [ok]", got)
	}
}
