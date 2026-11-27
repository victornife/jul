// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package quota

import (
	"sync/atomic"
	"testing"
)

func BenchmarkCoordinatorAdmit(b *testing.B) {
	policy := Policy{
		Application: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000}, PerCaller: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000}},
		},
	}
	c, err := NewCoordinator(policy, 1)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		decision, err := c.Admit("public", "caller")
		if err != nil || !decision.Allowed {
			b.Fatalf("admission: %+v, %v", decision, err)
		}
	}
}

func BenchmarkCoordinatorAdmitParallel(b *testing.B) {
	policy := Policy{
		Application: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000},
		Classes: map[string]Class{
			"public": {Shared: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000}, PerCaller: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000}},
		},
	}
	c, err := NewCoordinator(policy, 1)
	if err != nil {
		b.Fatal(err)
	}
	var failures atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			decision, err := c.Admit("public", "caller")
			if err != nil || !decision.Allowed {
				failures.Add(1)
			}
		}
	})
	b.StopTimer()
	if failures.Load() != 0 {
		b.Fatalf("%d admissions failed", failures.Load())
	}
}
