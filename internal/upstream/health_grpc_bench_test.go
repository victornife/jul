// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package upstream

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"jul/internal/config"
)

// BenchmarkProbeGRPCShortLived measures the #427 §48-49 design choice: a
// fresh grpc.NewClient + Check + Close per probe (what doProbeGRPCHealth
// does), against a local h2c backend.
func BenchmarkProbeGRPCShortLived(b *testing.B) {
	addr, hs := startPlainGRPCHealthBench(b)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	pool, err := NewPool(config.UpstreamConfig{
		Name: "grpc-bench", Strategy: "round_robin",
		Servers: []config.UpstreamServer{{Address: addr, Weight: 1}}, MaxFails: 3,
	}, "http")
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	defer pool.Close()

	backend := pool.Backends()[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		ok := doProbeGRPCHealth(ctx, backend, "", nil)
		cancel()
		if !ok {
			b.Fatal("probe failed")
		}
	}
}

// BenchmarkProbeGRPCPersistentConn measures the alternative design (a
// ClientConn reused across probes) for comparison. It exists only to justify
// the short-lived choice with a number, not to propose adopting it.
func BenchmarkProbeGRPCPersistentConn(b *testing.B) {
	addr, hs := startPlainGRPCHealthBench(b)
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	conn, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	client := healthpb.NewHealthClient(conn)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		resp, err := client.Check(ctx, &healthpb.HealthCheckRequest{})
		cancel()
		if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			b.Fatalf("check: resp=%v err=%v", resp, err)
		}
	}
}

// startPlainGRPCHealthBench is startPlainGRPCHealth adapted for *testing.B.
func startPlainGRPCHealthBench(b *testing.B) (addr string, hs *grpchealth.Server) {
	b.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatalf("listen: %v", err)
	}
	hs = grpchealth.NewServer()
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	b.Cleanup(s.Stop)
	return lis.Addr().String(), hs
}
