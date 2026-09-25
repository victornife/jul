// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !grpc

package upstream

import (
	"context"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

// TestRegistryForGRPCHealthCheckFailsClearlyInLeanBuild pins #427's build-tag
// boundary: a lean build (no "grpc" tag, so grpcHealthProbe stays nil) must
// reject health_check.type = "grpc" with a clear, immediate error at pool-build
// time — never silently fall back to TCP/HTTP and never mark the backend
// merely unhealthy after the probe interval elapses.
func TestRegistryForGRPCHealthCheckFailsClearlyInLeanBuild(t *testing.T) {
	if grpcHealthProbe != nil {
		t.Fatal("grpcHealthProbe should be nil without the grpc build tag")
	}
	r := NewRegistry(RegistryOptions{})
	r.Begin()
	up := upstreamCfg("grpc-health-lean", "round_robin", "127.0.0.1:1")
	up.HealthCheck = &config.HealthCheckConfig{
		Enabled: true, Type: "grpc", Service: "pkg.Service",
		Interval: config.Duration(5 * time.Second), Timeout: config.Duration(time.Second),
		HealthyThreshold: 2, UnhealthyThreshold: 3,
	}
	_, err := r.For(context.Background(), up, "http")
	if err == nil {
		r.Abort()
		t.Fatal("For should fail clearly when type=grpc is requested in a build without the grpc tag")
	}
	if !strings.Contains(err.Error(), "grpc") {
		t.Errorf("error %q should mention the missing grpc capability", err.Error())
	}
	r.Abort()
}
