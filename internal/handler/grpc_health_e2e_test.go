// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"
)

// startGRPCBackendWithHealth starts a TLS gRPC server exposing only the
// standard grpc.health.v1 Health service, toggled through the returned
// *health.Server. The Health/Check RPC doubles as this test's live-traffic
// proof: it is forwarded end-to-end through Jul's native gRPC passthrough with
// its real generated protobuf types, so no custom test-only codec is needed.
func startGRPCBackendWithHealth(t *testing.T, cert tls.Certificate) (addr string, hs *grpchealth.Server) {
	t.Helper()
	lis, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2"},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hs = grpchealth.NewServer()
	s := grpc.NewServer()
	healthpb.RegisterHealthServer(s, hs)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String(), hs
}

// serveFront wraps an already-built handler behind a real h2c front listener.
func serveFront(t *testing.T, h http.Handler) string {
	t.Helper()
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen front: %v", err)
	}
	srv := &http.Server{Handler: h}
	var proto http.Protocols
	proto.SetHTTP1(true)
	proto.SetUnencryptedHTTP2(true)
	srv.Protocols = &proto
	go func() { _ = srv.Serve(front) }()
	t.Cleanup(func() { _ = srv.Close() })
	return front.Addr().String()
}

// checkThrough performs a real Health/Check call through the h2c front and
// reports whether it succeeded, so the fail/recover assertions read the same
// pool state a client actually observes rather than the checker's internal
// view.
func checkThrough(t *testing.T, front string) error {
	t.Helper()
	conn, err := grpc.NewClient("passthrough:///"+front, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial front: %v", err)
	}
	defer func() { _ = conn.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

// TestGRPCHealthEjectsAndRecoversRealBackend is #427's native-gRPC-proxy
// integration proof: a single backend's grpc.health.v1 serving status gates
// its pool eligibility through the SAME pool a live gRPC call is proxied
// through — not a parallel health registry.
func TestGRPCHealthEjectsAndRecoversRealBackend(t *testing.T) {
	ca := newBackendPKI(t)
	addr, hs := startGRPCBackendWithHealth(t, ca.issue(t, "grpc-backend", []string{"svc.internal"}, nil))
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	up := config.UpstreamConfig{
		Name:     "grpcsvc",
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails: 3,
		HealthCheck: &config.HealthCheckConfig{
			Enabled: true, Type: "grpc",
			Interval: config.Duration(20 * time.Millisecond), Timeout: config.Duration(500 * time.Millisecond),
			HealthyThreshold: 2, UnhealthyThreshold: 2,
		},
		BackendTLS: &config.BackendTLSConfig{CAMode: "file_only", CAFile: ca.caPath, ServerName: "svc.internal"},
	}
	upstreams := map[string]config.UpstreamConfig{"grpcsvc": up}
	loc := config.LocationConfig{ProxyPass: "https://grpcsvc", GRPC: true}

	reg := upstream.NewRegistry(upstream.RegistryOptions{})
	reg.Begin()
	pool, err := reg.For(context.Background(), up, "https")
	if err != nil {
		t.Fatalf("For: %v", err)
	}
	reg.Commit()
	reg.Activate()
	defer pool.Close()

	waitHealthy := func(want bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for {
			healthy := pool.HealthyCount() == 1
			if healthy == want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("pool healthy state never reached %v (HealthyCount=%d)", want, pool.HealthyCount())
			}
			time.Sleep(2 * time.Millisecond)
		}
	}

	// waitTraffic polls the front for up to a few seconds so a call made in the
	// narrow window right at a threshold transition (health is inherently
	// eventually consistent) does not flake the assertion; it still fails the
	// test if the wanted outcome never becomes true.
	waitTraffic := func(front string, wantOK bool) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		var lastErr error
		for {
			lastErr = checkThrough(t, front)
			if (lastErr == nil) == wantOK {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("traffic never reached wantOK=%v, last error: %v", wantOK, lastErr)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitHealthy(true)

	// Build the real native gRPC proxy handler against the SAME registry, as a
	// reload would: this reuses the already-live pool (and its running
	// checker) rather than starting a second one.
	reg.Begin()
	h, err := NewGRPCProxy(context.Background(), config.ServerConfig{}, loc, upstreams, reg, grpcTestLogger(), nil)
	if err != nil {
		t.Fatalf("NewGRPCProxy: %v", err)
	}
	reg.Commit()
	reg.Activate()
	front := serveFront(t, h)

	waitTraffic(front, true)

	// NOT_SERVING must eject the backend after the configured threshold — the
	// live call must then fail with no eligible backend, never silently
	// succeed against an unhealthy backend.
	hs.SetServingStatus("", healthpb.HealthCheckResponse_NOT_SERVING)
	waitHealthy(false)
	waitTraffic(front, false)

	// Recovery must re-admit it and traffic must succeed again.
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	waitHealthy(true)
	waitTraffic(front, true)
}

// TestGRPCHealthNamedServiceIndependence proves a named service's status is
// evaluated independently of whole-server ("") health, matching the standard
// protocol: SERVICE_UNKNOWN (a name the backend never registered) must never
// be treated as healthy.
func TestGRPCHealthNamedServiceIndependence(t *testing.T) {
	ca := newBackendPKI(t)
	addr, hs := startGRPCBackendWithHealth(t, ca.issue(t, "grpc-backend", []string{"svc.internal"}, nil))
	hs.SetServingStatus("pkg.Widget", healthpb.HealthCheckResponse_SERVING)

	policy, err := backendtls.Resolve(backendtls.Options{
		CAMode: "file_only", CAFile: ca.caPath, ServerName: "svc.internal",
	}, "grpcsvc")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	probeOK := func(service string) bool {
		conn, err := grpc.NewClient("passthrough:///"+addr, grpc.WithTransportCredentials(credentials.NewTLS(policy.ClientConfig())))
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		defer func() { _ = conn.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{Service: service})
		if err != nil {
			return false
		}
		return resp.GetStatus() == healthpb.HealthCheckResponse_SERVING
	}

	if !probeOK("pkg.Widget") {
		t.Error("a registered service reporting SERVING should probe healthy")
	}
	if probeOK("pkg.NeverRegistered") {
		t.Error("SERVICE_UNKNOWN must never probe healthy")
	}

	hs.SetServingStatus("pkg.Widget", healthpb.HealthCheckResponse_NOT_SERVING)
	if probeOK("pkg.Widget") {
		t.Error("NOT_SERVING must not probe healthy")
	}
}
