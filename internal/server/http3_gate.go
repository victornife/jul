// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build http3

package server

import (
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
}

func newH3ConnGate(limit int) *h3ConnGate {
	g := &h3ConnGate{}
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
	g.active++
	return true
}

func (g *h3ConnGate) release() {
	g.mu.Lock()
	if g.active > 0 {
		g.active--
	}
	g.cond.Broadcast()
	g.mu.Unlock()
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
