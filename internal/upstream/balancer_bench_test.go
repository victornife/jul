// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"testing"
	"time"

	"jul/internal/config"
)

// benchBackends builds n directly-constructed backends with rotating weights
// and in-flight counts for balancer micro-benchmarks.
func benchBackends(n int) []*Backend {
	bs := make([]*Backend, n)
	for i := range bs {
		bs[i] = &Backend{}
		bs[i].setWeight(i%4 + 1)
		bs[i].inflight.Store(int64(i % 7))
	}
	return bs
}

func benchmarkBalancer(b *testing.B, strategy string) {
	bal := newBalancer(strategy)
	backends := benchBackends(8)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = bal.pick(backends)
	}
}

func BenchmarkBalancerRoundRobin(b *testing.B) { benchmarkBalancer(b, "round_robin") }

func BenchmarkBalancerWeightedRR(b *testing.B) { benchmarkBalancer(b, "weighted_round_robin") }

func BenchmarkBalancerLeastConn(b *testing.B) { benchmarkBalancer(b, "least_conn") }

// BenchmarkPoolPick measures the full hot path: availability filter, balancer
// selection, and in-flight accounting via the public Pool API.
func BenchmarkPoolPick(b *testing.B) {
	p, err := NewPool(config.UpstreamConfig{
		Name:        "bench",
		Strategy:    "round_robin",
		MaxFails:    1,
		FailTimeout: config.Duration(10 * time.Second),
		Servers: []config.UpstreamServer{
			{Address: "10.0.0.1:80", Weight: 1},
			{Address: "10.0.0.2:80", Weight: 1},
			{Address: "10.0.0.3:80", Weight: 1},
			{Address: "10.0.0.4:80", Weight: 1},
		},
	}, "http")
	if err != nil {
		b.Fatalf("NewPool: %v", err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		backend, err := p.Pick()
		if err != nil {
			b.Fatalf("Pick: %v", err)
		}
		p.Release(backend)
	}
}
