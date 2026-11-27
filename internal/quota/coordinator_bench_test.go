// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package quota

import (
	"fmt"
	"sort"
	"sync/atomic"
	"testing"
	"time"
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

func BenchmarkProtectedAdmitWithFullCallerStore(b *testing.B) {
	for _, saturated := range []bool{false, true} {
		name := "idle"
		if saturated {
			name = "public-scan"
		}
		b.Run(name, func(b *testing.B) {
			policy := Policy{
				Application: Limit{Rate: 1_000_000_000, Burst: 1_000_000_000},
				Classes: map[string]Class{
					"public":    {Shared: Limit{Rate: 400_000_000, Burst: 400_000_000}, PerCaller: Limit{Rate: 1, Burst: 1}},
					"protected": {Shared: Limit{Rate: 600_000_000, Burst: 600_000_000}, PerCaller: Limit{Rate: 600_000_000, Burst: 600_000_000}},
				},
			}
			c, err := NewCoordinator(policy, 4096)
			if err != nil {
				b.Fatal(err)
			}
			now := time.Unix(100, 0)
			c.now = func() time.Time { return now }
			for i := 0; i < 4096; i++ {
				decision, err := c.Admit("public", fmt.Sprint(i))
				if err != nil || !decision.Allowed {
					b.Fatalf("populate caller store: %+v, %v", decision, err)
				}
			}
			if decision, err := c.Admit("protected", "protected"); err != nil || !decision.Allowed {
				b.Fatalf("initial protected admission: %+v, %v", decision, err)
			}

			type result struct {
				scans int
				err   error
			}
			stop := make(chan struct{})
			done := make(chan result, 1)
			ready := make(chan struct{})
			if saturated {
				go func() {
					var r result
					for {
						select {
						case <-stop:
							done <- r
							return
						default:
						}
						decision, err := c.Admit("public", "new-caller")
						if err != nil || decision.Allowed || decision.Reason != ReasonCallerCapacity {
							r.err = fmt.Errorf("public admission: %+v, %v", decision, err)
							if r.scans == 0 {
								close(ready)
							}
							done <- r
							return
						}
						r.scans++
						if r.scans == 1 {
							close(ready)
						}
					}
				}()
				<-ready
			}
			// Sample one in 16 admissions, bounded to the first 16,384 samples.
			samples := make([]int64, 0, 16384)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sample := i%16 == 0 && len(samples) < cap(samples)
				var started time.Time
				if sample {
					started = time.Now()
				}
				decision, err := c.Admit("protected", "protected")
				if sample {
					samples = append(samples, time.Since(started).Nanoseconds())
				}
				if err != nil || !decision.Allowed {
					b.Errorf("protected admission: %+v, %v", decision, err)
					break
				}
			}
			b.StopTimer()
			if saturated {
				close(stop)
				r := <-done
				if r.err != nil {
					b.Fatal(r.err)
				}
				b.ReportMetric(float64(r.scans)/b.Elapsed().Seconds(), "public-scans/s")
			}
			if len(samples) != 0 {
				sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
				b.ReportMetric(float64(samples[(len(samples)*99+99)/100-1]), "protected-p99-ns")
			}
		})
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
