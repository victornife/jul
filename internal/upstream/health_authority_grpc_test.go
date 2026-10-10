// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
//go:build grpc

package upstream

import (
	"context"
	"crypto/tls"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	grpchealth "google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"jul/internal/backendtls"
	"net"
	"testing"
	"time"
)

func TestHealthGRPCAuthorityPreservesTrust(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "h2c", true: "TLS"}[secure], func(t *testing.T) {
			ca := newProbePKI(t)
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			scheme := "http"
			if secure {
				scheme = "https"
				listener = tls.NewListener(listener, &tls.Config{Certificates: []tls.Certificate{ca.issue(t, []string{"grpc.internal"})}, MinVersion: tls.VersionTLS12, NextProtos: []string{"h2"}})
			}
			var opts []grpc.ServerOption
			opts = append(opts, grpc.UnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				authority := md.Get(":authority")
				if len(authority) != 1 || authority[0] != "routing.internal:443" {
					return nil, status.Error(codes.PermissionDenied, "wrong route")
				}
				return handler(ctx, req)
			}))
			server := grpc.NewServer(opts...)
			hs := grpchealth.NewServer()
			hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
			healthpb.RegisterHealthServer(server, hs)
			go func() { _ = server.Serve(listener) }()
			defer server.Stop()
			pool := grpcPool(t, scheme, listener.Addr().String())
			b := pool.Backends()[0]
			policy, err := backendtls.Resolve(backendtls.Options{CAFile: ca.caPath, CAMode: backendtls.CAModeFileOnly, ServerName: "grpc.internal"}, "health-authority")
			if err != nil {
				t.Fatal(err)
			}
			probe := func(p *backendtls.Policy, host string) bool {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				return doProbeGRPCHealth(ctx, b, "", p, host)
			}
			if !probe(policy, "routing.internal:443") {
				t.Fatal("routing override broke valid TLS identity")
			}
			if probe(policy, "") {
				t.Fatal("vhost routing unexpectedly succeeded without override")
			}
			if secure {
				if probe(nil, "routing.internal:443") {
					t.Fatal("Host override weakened CA verification")
				}
				bad, err := backendtls.Resolve(backendtls.Options{CAFile: ca.caPath, CAMode: backendtls.CAModeFileOnly, ServerName: "wrong.internal"}, "wrong-health")
				if err != nil {
					t.Fatal(err)
				}
				if probe(bad, "routing.internal:443") {
					t.Fatal("Host override weakened certificate name verification")
				}
			}
		})
	}
	creds := probeRoutingCredentials{TransportCredentials: credentials.NewTLS(&tls.Config{ServerName: "grpc.internal"}), serverName: "grpc.internal"}
	if creds.Clone().Info() != (credentials.ProtocolInfo{SecurityProtocol: "tls"}) {
		t.Fatal("clone lost routing/identity separation")
	}
}
