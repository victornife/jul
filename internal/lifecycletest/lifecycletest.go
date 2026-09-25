// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package lifecycletest is the shared test vocabulary for the runtime-resource
// ownership invariants in docs/resource-ownership.md. It is imported only from
// _test.go files; production code must not depend on it.
//
// It deliberately provides two small things instead of a framework:
//
//   - Resource, an explicit-close fake that records how often it was closed
//     and refuses acquisition once retired, so a test can state "candidate
//     closed on Abort", "old resource open until drain", "replacement closed
//     exactly once" and "retired resource cannot be newly acquired" directly;
//   - Snapshot/AwaitQuiescence, a bounded goroutine/FD baseline check for
//     repeated Prepare/Abort or reload churn.
package lifecycletest

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"
)

// ErrRetired is returned by Resource.Acquire after the resource was closed.
var ErrRetired = errors.New("lifecycletest: resource retired")

// Resource is an explicit-close resource fake. It is safe for concurrent use.
type Resource struct {
	name string

	mu       sync.Mutex
	closes   int
	acquired int
}

// NewResource returns an open Resource labelled name in failure messages.
func NewResource(name string) *Resource { return &Resource{name: name} }

// Close records one close. Unlike most production closers it does not make a
// second close a silent no-op, so AssertClosedOnce can detect double close.
func (r *Resource) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closes++
	return nil
}

// Acquire models new work taking the resource. It fails once the resource is
// retired: new work must never acquire a retired resource.
func (r *Resource) Acquire() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closes > 0 {
		return fmt.Errorf("%s: %w", r.name, ErrRetired)
	}
	r.acquired++
	return nil
}

// Closes reports how many times Close was called.
func (r *Resource) Closes() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closes
}

// Acquired reports how many acquisitions succeeded.
func (r *Resource) Acquired() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.acquired
}

// AssertOpen fails t if r has been closed (for example a live or reused
// resource that a failed candidate or a no-op must not retire).
func AssertOpen(t testing.TB, r *Resource) {
	t.Helper()
	if n := r.Closes(); n != 0 {
		t.Errorf("%s: closed %d time(s), want still open", r.name, n)
	}
}

// AssertClosedOnce fails t unless r was closed exactly once.
func AssertClosedOnce(t testing.TB, r *Resource) {
	t.Helper()
	if n := r.Closes(); n != 1 {
		t.Errorf("%s: closed %d time(s), want exactly once", r.name, n)
	}
}

// Snapshot is a process resource baseline.
type Snapshot struct {
	Goroutines int
	// FDs is the open file-descriptor count, or -1 where it is not observable.
	FDs int
}

// Take records the current goroutine and FD counts after a GC.
func Take() Snapshot {
	runtime.GC()
	return Snapshot{Goroutines: runtime.NumGoroutine(), FDs: openFDs()}
}

func openFDs() int {
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return -1
	}
	return len(entries)
}

// quiescenceWait bounds AwaitQuiescence. Retiring goroutines exit
// asynchronously, so the check waits for convergence instead of sampling once.
var quiescenceWait = 10 * time.Second

// AwaitQuiescence fails t unless goroutines and FDs return to within slack of
// base before a bounded deadline. It is for resources whose teardown is
// asynchronous (retire goroutines, transport readers); it never proves an
// interleaving and must not replace deterministic synchronization.
func AwaitQuiescence(t testing.TB, base Snapshot, slack int) {
	t.Helper()
	deadline := time.Now().Add(quiescenceWait)
	var now Snapshot
	for {
		now = Take()
		if within(base, now, slack) {
			return
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 64<<10)
	buf = buf[:runtime.Stack(buf, true)]
	t.Errorf("resources did not return to quiescence: goroutines %d -> %d, fds %d -> %d (slack %d)\n%s",
		base.Goroutines, now.Goroutines, base.FDs, now.FDs, slack, buf)
}

func within(base, now Snapshot, slack int) bool {
	if now.Goroutines > base.Goroutines+slack {
		return false
	}
	if base.FDs >= 0 && now.FDs >= 0 && now.FDs > base.FDs+slack {
		return false
	}
	return true
}
