// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

// TestBuildWithEgressCapturesGenerationScopedWrapper proves the process-lifetime
// plugin Manager does not retain one startup egress policy. Each Set built for a
// handler generation captures the wrapper supplied for that candidate. The
// local SSRF/allowed_hosts layer is still composed inside fetchDial; the
// existing TestFetchDialIntersection covers the security intersection itself.
func TestBuildWithEgressCapturesGenerationScopedWrapper(t *testing.T) {
	m := testManager(t)
	var callsA, callsB atomic.Int64
	wrap := func(counter *atomic.Int64) func(DialFunc) DialFunc {
		return func(base DialFunc) DialFunc {
			return func(ctx context.Context, network, addr string) (net.Conn, error) {
				counter.Add(1)
				return base(ctx, network, addr)
			}
		}
	}
	cfg := map[string]config.PluginConfig{"p": pcfg("header-inject")}

	setA, err := m.BuildWithEgress(context.Background(), cfg, wrap(&callsA))
	if err != nil {
		t.Fatalf("BuildWithEgress A: %v", err)
	}
	defer setA.Close()
	setB, err := m.BuildWithEgress(context.Background(), cfg, wrap(&callsB))
	if err != nil {
		t.Fatalf("BuildWithEgress B: %v", err)
	}
	defer setB.Close()

	base := func(context.Context, string, string) (net.Conn, error) {
		left, right := net.Pipe()
		_ = right.Close()
		return left, nil
	}
	resolver := rebindResolver{ip: "8.8.8.8"}

	for name, tc := range map[string]struct {
		set     *Set
		wantA   int64
		wantB   int64
	}{
		"old set keeps A": {set: setA, wantA: 1, wantB: 0},
		"new set uses B":  {set: setB, wantA: 1, wantB: 1},
	} {
		t.Run(name, func(t *testing.T) {
			p := tc.set.plugins["p"]
			if p == nil {
				t.Fatal("compiled plugin missing")
			}
			conn, err := p.fetchDial(dialerFunc(base), resolver).DialContext(context.Background(), "tcp", "api.example.com:443")
			if err != nil {
				t.Fatalf("fetchDial: %v", err)
			}
			_ = conn.Close()
			if callsA.Load() != tc.wantA || callsB.Load() != tc.wantB {
				t.Fatalf("wrapper calls A/B = %d/%d, want %d/%d", callsA.Load(), callsB.Load(), tc.wantA, tc.wantB)
			}
		})
	}
}
