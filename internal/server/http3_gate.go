// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
	"context"
	"sync"

	"github.com/quic-go/quic-go"
)

// h3ErrorNoError is H3_NO_ERROR (RFC 9114 §8.1), used when closing a connection
// that the listener will not serve because it is shutting down.
const h3ErrorNoError = quic.ApplicationErrorCode(0x100)

// h3ConnGate is the QUIC counterpart of dynamicConnLimiter: acquire blocks the
// accept loop while limit connections are being served; 0 is unlimited.
type h3ConnGate struct {
	mu     sync.Mutex
	cond   *sync.Cond
	limit  int
	active int
	closed bool
	idle   chan struct{}
}

func newH3ConnGate(limit int) *h3ConnGate {
	idle := make(chan struct{})
	close(idle)
	g := &h3ConnGate{idle: idle}
	g.cond = sync.NewCond(&g.mu)
	g.setLimit(limit)
	return g
}

func (g *h3ConnGate) setLimit(limit int) {
	if limit < 0 {
		limit = 0
	}
	g.mu.Lock()
	g.limit = limit
	g.cond.Broadcast()
	g.mu.Unlock()
}

// acquire waits for a slot and reports false once the gate is closed.
func (g *h3ConnGate) acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	for !g.closed && g.limit > 0 && g.active >= g.limit {
		g.cond.Wait()
	}
	if g.closed {
		return false
	}
	if g.active == 0 {
		g.idle = make(chan struct{})
	}
	g.active++
	return true
}

func (g *h3ConnGate) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
		if g.active == 0 {
			close(g.idle)
		}
	}
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *h3ConnGate) waitIdle(ctx context.Context) error {
	g.mu.Lock()
	if g.active == 0 {
		g.mu.Unlock()
		return nil
	}
	idle := g.idle
	g.mu.Unlock()

	select {
	case <-idle:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *h3ConnGate) close() {
	g.mu.Lock()
	g.closed = true
	g.cond.Broadcast()
	g.mu.Unlock()
}

func (g *h3ConnGate) snapshot() (limit, active int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.limit, g.active
}
