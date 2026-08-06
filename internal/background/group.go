// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package background

import (
	"context"
	"sync"
	"time"
)

// Group is the concrete Lease. It owns the set of background operations started
// against one handler generation:
//
//   - every operation context descends from the group root, which descends from
//     the process context, so process shutdown cancels all of them at once;
//   - every operation carries its own bounded deadline, so no single operation
//     can delay retirement indefinitely;
//   - admit/done hooks let the owner (the server's handler generation) count
//     leased work in the SAME in-flight accounting that keeps a generation's
//     resources open, so an active refresh delays retirement exactly the way an
//     in-flight request does.
//
// The zero value is not usable; construct with NewGroup.
type Group struct {
	genID  uint64
	maxOp  time.Duration
	admit  func() bool
	done   func()
	root   context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	canceled bool
	active   int
	wg       sync.WaitGroup
}

// GroupOptions configures a Group.
type GroupOptions struct {
	// Generation is the owning handler generation id.
	Generation uint64
	// MaxOperation bounds one operation. Values <= 0 select DefaultMaxOperation.
	MaxOperation time.Duration
	// Admit is consulted before an operation is registered. Returning false
	// rejects the acquisition; the implementation is responsible for leaving no
	// accounting behind when it does. A nil Admit always admits.
	Admit func() bool
	// Done is called exactly once per admitted operation when it is released.
	// It is the counterpart of Admit. A nil Done is a no-op.
	Done func()
}

// NewGroup returns a Group whose operations are rooted in parent. A nil parent
// is treated as context.Background(), which is only correct for tests and for a
// server that has not yet published its process context.
func NewGroup(parent context.Context, opts GroupOptions) *Group {
	if parent == nil {
		parent = context.Background()
	}
	maxOp := opts.MaxOperation
	if maxOp <= 0 {
		maxOp = DefaultMaxOperation
	}
	root, cancel := context.WithCancel(parent)
	return &Group{
		genID:  opts.Generation,
		maxOp:  maxOp,
		admit:  opts.Admit,
		done:   opts.Done,
		root:   root,
		cancel: cancel,
	}
}

// Generation implements Lease.
func (g *Group) Generation() uint64 { return g.genID }

// Acquire implements Lease. The returned context is rooted in the group (hence
// in the process lifetime), carries the allow-listed values from src, and
// expires after the group's bounded operation deadline.
func (g *Group) Acquire(src context.Context, op Operation) (context.Context, func(), bool) {
	if !op.Valid() {
		return nil, nil, false
	}

	g.mu.Lock()
	if g.canceled || g.root.Err() != nil {
		g.mu.Unlock()
		return nil, nil, false
	}
	if g.admit != nil && !g.admit() {
		g.mu.Unlock()
		return nil, nil, false
	}
	g.active++
	g.wg.Add(1)
	g.mu.Unlock()

	ctx, cancel := context.WithTimeout(Detach(g.root, src), g.maxOp)
	release := sync.OnceFunc(func() {
		cancel()
		g.mu.Lock()
		g.active--
		g.mu.Unlock()
		if g.done != nil {
			g.done()
		}
		g.wg.Done()
	})
	return ctx, release, true
}

// Cancel stops admitting new operations and cancels every operation already
// running. It is idempotent. Releases still run normally, so the owner's
// accounting stays balanced.
func (g *Group) Cancel() {
	g.mu.Lock()
	already := g.canceled
	g.canceled = true
	g.mu.Unlock()
	if !already {
		g.cancel()
	}
}

// Wait blocks until every admitted operation has been released or d elapses. It
// reports whether the group drained. A non-positive d checks without waiting.
func (g *Group) Wait(d time.Duration) bool {
	drained := make(chan struct{})
	go func() {
		g.wg.Wait()
		close(drained)
	}()
	if d <= 0 {
		select {
		case <-drained:
			return true
		default:
			return false
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-drained:
		return true
	case <-t.C:
		return false
	}
}

// Active returns the number of operations currently holding the lease.
func (g *Group) Active() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}
