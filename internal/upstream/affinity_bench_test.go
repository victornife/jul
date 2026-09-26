// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"fmt"
	"strconv"
	"testing"

	"jul/internal/affinity"
	"jul/internal/config"
)

var affinityBenchSizes = []int{2, 8, 32, 128}

func benchServers(n int, weighted bool) []config.UpstreamServer {
	out := make([]config.UpstreamServer, n)
	for i := range out {
		w := 1
		if weighted {
			w = i%4 + 1
		}
		out[i] = config.UpstreamServer{Address: fmt.Sprintf("10.%d.%d.%d:8080", i/65536, (i/256)%256, i%256+1), Weight: w}
	}
	return out
}

func benchPool(b *testing.B, strategy string, n int, weighted bool) *Pool {
	b.Helper()
	cfg := config.UpstreamConfig{Name: "bench", Strategy: strategy, Servers: benchServers(n, weighted), MaxFails: 1}
	if strategy == "consistent_hash" {
		cfg.Hash = &config.HashConfig{Key: "header", Name: "X-Tenant"}
	}
	p, err := NewPool(cfg, "http")
	if err != nil {
		b.Fatal(err)
	}
	return p
}

// BenchmarkAffinityPick compares the full Pool selection path — eligibility
// filter, balancer and in-flight accounting — for consistent_hash against
// round_robin and least_conn at representative pool sizes. keys cycle through
// 1024 distinct values so the result is not one hot key.
func BenchmarkAffinityPick(b *testing.B) {
	keys := make([]affinity.Key, 1024)
	for i := range keys {
		keys[i] = affinity.KeyOf("tenant-" + strconv.Itoa(i))
	}
	for _, n := range affinityBenchSizes {
		for _, tc := range []struct {
			name, strategy string
			weighted       bool
		}{
			{"round_robin", "round_robin", false},
			{"least_conn", "least_conn", false},
			{"consistent_hash", "consistent_hash", false},
			{"consistent_hash_weighted", "consistent_hash", true},
		} {
			b.Run(fmt.Sprintf("%s/n=%d", tc.name, n), func(b *testing.B) {
				p := benchPool(b, tc.strategy, n, tc.weighted)
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					at, err := p.PickKeyed(ctx, keys[i&1023], nil)
					if err != nil {
						b.Fatal(err)
					}
					p.Release(at.Backend)
				}
			})
		}
	}
}

// BenchmarkAffinityPickParallel measures contention scaling: consistent_hash
// has no shared mutable balancer state, so it should scale with cores where
// round_robin shares one atomic counter.
func BenchmarkAffinityPickParallel(b *testing.B) {
	for _, strategy := range []string{"round_robin", "least_conn", "consistent_hash"} {
		b.Run(strategy+"/n=32", func(b *testing.B) {
			p := benchPool(b, strategy, 32, false)
			ctx := context.Background()
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					at, err := p.PickKeyed(ctx, affinity.KeyOf("t"+strconv.Itoa(i&255)), nil)
					if err != nil {
						b.Error(err)
						return
					}
					p.Release(at.Backend)
					i++
				}
			})
		})
	}
}

// BenchmarkAffinityRank isolates the ranking itself (no filter, no
// accounting), which is the part that grows with pool size.
func BenchmarkAffinityRank(b *testing.B) {
	for _, n := range affinityBenchSizes {
		for _, weighted := range []bool{false, true} {
			b.Run(fmt.Sprintf("weighted=%v/n=%d", weighted, n), func(b *testing.B) {
				p := benchPool(b, "consistent_hash", n, weighted)
				r := p.balancer.(*rendezvous)
				bs := p.Backends()
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					_ = r.pickKey(bs, uint64(i)*0x9e3779b97f4a7c15)
				}
			})
		}
	}
}

// BenchmarkAffinityMembershipUpdate is the cost of a discovery refresh that
// replaces the backend set. consistent_hash adds one identity hash per new
// backend and keeps no derived structure to rebuild.
func BenchmarkAffinityMembershipUpdate(b *testing.B) {
	for _, n := range affinityBenchSizes {
		for _, strategy := range []string{"round_robin", "consistent_hash"} {
			b.Run(fmt.Sprintf("%s/n=%d", strategy, n), func(b *testing.B) {
				p := benchPool(b, strategy, n, false)
				a, c := benchServers(n, false), benchServers(n+1, false)[1:]
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if i&1 == 0 {
						p.UpdateBackends(a)
					} else {
						p.UpdateBackends(c)
					}
				}
			})
		}
	}
}
