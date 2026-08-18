// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"runtime"
	"testing"

	"jul/internal/resilience"
)

func benchPolicy(b *testing.B, o resilience.Options) *resilience.Policy {
	b.Helper()
	p, err := resilience.Resolve(o)
	if err != nil {
		b.Fatalf("resolve: %v", err)
	}
	return p
}

// BenchmarkAdmit_Uncontended measures the fast path with one goroutine: a
// policy load, one CAS and the release closure. This is the cost every request
// pays when nothing is contending.
func BenchmarkAdmit_Uncontended(b *testing.B) {
	for _, gomaxprocs := range []int{1, 4, 16} {
		b.Run(procsName(gomaxprocs), func(b *testing.B) {
			defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(gomaxprocs))
			a := NewAdmission(benchPolicy(b, resilience.Options{MaxActiveRequests: 1 << 20}))
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				release, err := a.Admit(ctx, nil)
				if err != nil {
					b.Fatalf("admit: %v", err)
				}
				release()
			}
		})
	}
}

// BenchmarkAdmit_Contended is the measurement the ADR deferred the sharding
// decision to.
//
// Every goroutine hammers one counter, which is the worst case for a single
// atomic: if the CAS fast path collapses here, a sharded counter or a lock-free
// structure would be justified. The limit is deliberately far above the
// concurrency so the slow path is never taken — this measures contention on the
// counter itself, not queueing.
func BenchmarkAdmit_Contended(b *testing.B) {
	for _, gomaxprocs := range []int{1, 4, 16} {
		b.Run(procsName(gomaxprocs), func(b *testing.B) {
			defer runtime.GOMAXPROCS(runtime.GOMAXPROCS(gomaxprocs))
			a := NewAdmission(benchPolicy(b, resilience.Options{MaxActiveRequests: 1 << 20}))
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				ctx := context.Background()
				for pb.Next() {
					release, err := a.Admit(ctx, nil)
					if err != nil {
						b.Fatalf("admit: %v", err)
					}
					release()
				}
			})
		})
	}
}

// BenchmarkAdmit_Unlimited measures the path an upstream with no resilience
// block takes: a policy load, a zero test and one counter increment. It is the
// cost the ADR's compatibility promise rests on, and the reason the proxy
// round-trip gate can be met at all.
func BenchmarkAdmit_Unlimited(b *testing.B) {
	a := NewAdmission(nil)
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		release, err := a.Admit(ctx, nil)
		if err != nil {
			b.Fatalf("admit: %v", err)
		}
		release()
	}
}

// BenchmarkAdmit_Queued measures the slow path: the limit is one, so every
// concurrent caller parks on the FIFO and is handed the slot directly by the
// releasing goroutine. This is what a fully saturated pool costs per request,
// and it is the number that would justify a different queue container.
func BenchmarkAdmit_Queued(b *testing.B) {
	a := NewAdmission(benchPolicy(b, resilience.Options{
		MaxActiveRequests:  1,
		MaxPendingRequests: 4096,
	}))
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		ctx := context.Background()
		for pb.Next() {
			release, err := a.Admit(ctx, nil)
			if err != nil {
				continue // queue full under extreme parallelism; not the measured path
			}
			release()
		}
	})
}

func procsName(n int) string {
	switch n {
	case 1:
		return "GOMAXPROCS=1"
	case 4:
		return "GOMAXPROCS=4"
	default:
		return "GOMAXPROCS=16"
	}
}
