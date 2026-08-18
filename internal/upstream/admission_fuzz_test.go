// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/resilience"
)

// FuzzAdmissionInterleavings drives randomised interleavings of acquire,
// release, cancel, policy reload, backend update and pool retirement against the
// accounting invariants from ADR 0017:
//
//	0 <= Q_p <= max_pending_requests
//	0 <= A_p
//	sum(A_b) <= k * A_p   (k = 1)
//
// The seed bytes choose the operation order, so the corpus explores orderings a
// hand-written test would not think to try. The invariants are checked
// continuously rather than only at the end, because an accounting bug that
// cancels itself out before quiesce is still a bug: it would have rejected or
// admitted real traffic while it lasted.
func FuzzAdmissionInterleavings(f *testing.F) {
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{255, 128, 64, 32, 16, 8, 4, 2, 1})
	f.Add([]byte{3, 3, 3, 7, 7, 7, 1, 1})

	f.Fuzz(func(t *testing.T, seed []byte) {
		if len(seed) == 0 {
			return
		}
		const (
			limit      = 8
			maxPending = 16
		)
		p, err := NewPool(config.UpstreamConfig{
			Name:     "fuzz",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "10.0.0.1:80", Weight: 1},
				{Address: "10.0.0.2:80", Weight: 1},
			},
			MaxFails: 3,
			Resilience: &config.ResilienceConfig{
				MaxActiveRequests:  limit,
				MaxPendingRequests: maxPending,
				PendingTimeout:     config.Duration(2 * time.Millisecond),
			},
		}, "http")
		if err != nil {
			t.Fatalf("NewPool: %v", err)
		}
		defer p.Close()
		adm := p.Admission()

		var violations atomic.Int64
		var firstViolation atomic.Value

		check := func(where string) {
			pending := adm.Pending()
			active := adm.Active()
			if pending < 0 || pending > maxPending {
				if violations.Add(1) == 1 {
					firstViolation.Store(where + ": pending out of range")
				}
			}
			if active < 0 {
				if violations.Add(1) == 1 {
					firstViolation.Store(where + ": active went negative")
				}
			}
			var backendSum int64
			for _, b := range p.Backends() {
				if in := b.Inflight(); in < 0 {
					if violations.Add(1) == 1 {
						firstViolation.Store(where + ": backend in-flight went negative")
					}
				} else {
					backendSum += in
				}
			}
			// k = 1: a request holds pool admission for its whole life but a
			// backend slot only during an attempt, so the sum can trail but
			// never lead.
			if backendSum > active {
				if violations.Add(1) == 1 {
					firstViolation.Store(where + ": sum(A_b) exceeded A_p")
				}
			}
		}

		rng := rand.New(rand.NewSource(int64(len(seed))))
		for _, b := range seed {
			rng.Seed(int64(b) * 2654435761)
		}

		var wg sync.WaitGroup
		stop := make(chan struct{})

		// Workers replay the seed as an operation stream.
		for w := 0; w < 4; w++ {
			wg.Add(1)
			go func(worker int) {
				defer wg.Done()
				for i, op := range seed {
					select {
					case <-stop:
						return
					default:
					}
					switch (int(op) + worker + i) % 5 {
					case 0, 1: // admit and release
						ctx, cancel := context.WithCancel(context.Background())
						release, err := adm.Admit(ctx, nil)
						if err == nil {
							check("after admit")
							release()
						}
						cancel()
					case 2: // admit then abandon
						ctx, cancel := context.WithCancel(context.Background())
						cancel()
						if release, err := adm.Admit(ctx, nil); err == nil {
							release()
						}
					case 3: // admit, then select a backend, mirroring the real order
						// Admission is always taken before selection in every
						// protocol adapter, and sum(A_b) <= A_p only holds
						// because of that ordering: a backend slot is held by an
						// already-admitted request. Picking without admitting
						// first would violate the invariant's precondition
						// rather than test it.
						ctx, cancel := context.WithCancel(context.Background())
						if release, err := adm.Admit(ctx, nil); err == nil {
							if backend, perr := p.Pick(); perr == nil {
								check("after pick")
								backend.Release()
							}
							release()
						}
						cancel()
					case 4: // reload the policy
						next, rerr := resilience.Resolve(resilience.Options{
							MaxActiveRequests:  int(op)%limit + 1,
							MaxPendingRequests: maxPending,
							PendingTimeout:     2 * time.Millisecond,
						})
						if rerr == nil {
							p.SetPolicy(next)
						}
					}
					check("after op")
				}
			}(w)
		}

		// A concurrent backend-set churner, which is where reuse and replacement
		// race the counters.
		wg.Add(1)
		go func() {
			defer wg.Done()
			sets := [][]config.UpstreamServer{
				{{Address: "10.0.0.1:80", Weight: 1}, {Address: "10.0.0.2:80", Weight: 1}},
				{{Address: "10.0.0.1:80", Weight: 5}},
				{{Address: "10.0.0.2:80", Weight: 1}, {Address: "10.0.0.3:80", Weight: 2}},
			}
			for i, op := range seed {
				select {
				case <-stop:
					return
				default:
				}
				p.UpdateBackends(sets[(int(op)+i)%len(sets)])
				check("after backend update")
			}
		}()

		wg.Wait()
		close(stop)

		// Restore a policy that permits admission, then quiesce.
		restored, err := resilience.Resolve(resilience.Options{MaxActiveRequests: limit, MaxPendingRequests: maxPending})
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		p.SetPolicy(restored)

		deadline := time.Now().Add(3 * time.Second)
		for adm.Active() != 0 || adm.Pending() != 0 {
			if time.Now().After(deadline) {
				t.Fatalf("did not quiesce: active=%d pending=%d", adm.Active(), adm.Pending())
			}
			time.Sleep(time.Millisecond)
		}

		if n := violations.Load(); n != 0 {
			t.Fatalf("%d invariant violations, first: %v", n, firstViolation.Load())
		}
	})
}
