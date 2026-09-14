// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"net"
	"sync"

	"jul/internal/config"
)

// dynamicConnLimiter is a stable listener-lifetime admission wrapper whose
// cap can change without rebinding the socket. The limit applies when Accept
// returns a connection to net/http: connections already admitted remain alive
// when the cap is lowered, while a pending accept waits until the active count
// falls below the current cap. A limit of zero is unlimited.
//
// The underlying socket may have accepted at most one connection that is
// waiting for admission in net/http's single accept loop. That connection has
// not entered TLS or HTTP processing yet and is closed if the listener shuts
// down before admission.
type dynamicConnLimiter struct {
	net.Listener

	mu     sync.Mutex
	cond   *sync.Cond
	limit  int
	active int
	closed bool

	closeOnce sync.Once
	closeErr  error
}

func newDynamicConnLimiter(ln net.Listener, limit int) *dynamicConnLimiter {
	if limit < 0 {
		limit = 0
	}
	l := &dynamicConnLimiter{Listener: ln, limit: limit}
	l.cond = sync.NewCond(&l.mu)
	return l
}

// SetLimit publishes the cap for subsequent admissions. Lowering the cap never
// closes an admitted connection; raising it (or setting zero/unlimited) wakes a
// waiter immediately.
func (l *dynamicConnLimiter) SetLimit(limit int) {
	if l == nil {
		return
	}
	if limit < 0 {
		limit = 0
	}
	l.mu.Lock()
	l.limit = limit
	l.cond.Broadcast()
	l.mu.Unlock()
}

func (l *dynamicConnLimiter) Limit() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.limit
}

func (l *dynamicConnLimiter) Active() int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active
}

func (l *dynamicConnLimiter) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	for !l.closed && l.limit > 0 && l.active >= l.limit {
		l.cond.Wait()
	}
	if l.closed {
		l.mu.Unlock()
		_ = conn.Close()
		return nil, net.ErrClosed
	}
	l.active++
	l.mu.Unlock()

	return &countedConn{Conn: conn, release: l.release}, nil
}

func (l *dynamicConnLimiter) release() {
	l.mu.Lock()
	if l.active > 0 {
		l.active--
	}
	l.cond.Broadcast()
	l.mu.Unlock()
}

func (l *dynamicConnLimiter) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.mu.Lock()
		l.closed = true
		l.cond.Broadcast()
		l.mu.Unlock()
		l.closeErr = l.Listener.Close()
	})
	return l.closeErr
}

type countedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
	return err
}

// effectiveConnectionCap preserves the existing master-switch semantics while
// making them live: disabling rate_limit makes the listener cap unlimited;
// enabling it activates max_conns immediately. max_conns == 0 is unlimited.
func effectiveConnectionCap(cfg *config.Config) int {
	if cfg == nil || !cfg.RateLimit.Enabled || cfg.RateLimit.MaxConns <= 0 {
		return 0
	}
	return cfg.RateLimit.MaxConns
}

// updateConnectionLimits is a no-fail Publish operation. Retained listeners are
// updated before the candidate config/runtime snapshot is published; newly
// staged listeners were already built with the candidate cap.
func (s *Server) updateConnectionLimits(cfg *config.Config) {
	limit := effectiveConnectionCap(cfg)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.listeners {
		if entry != nil && entry.connLimiter != nil {
			entry.connLimiter.SetLimit(limit)
		}
	}
}
