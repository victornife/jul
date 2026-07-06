// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"errors"
	"fmt"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/config"
)

// ErrNoAvailableBackend is returned by Pick when every backend is in cooldown.
var ErrNoAvailableBackend = errors.New("no available upstream backend")

// Pool is a named set of backends fronted by a load-balancing strategy and
// passive health checking.
type Pool struct {
	name        string
	scheme      string
	balancer    Balancer
	maxFails    int
	failTimeout time.Duration

	// backends holds the current backend set as an immutable slice published
	// via an atomic pointer, so Pick performs a lock-free read on the hot path
	// while UpdateBackends swaps the whole set atomically.
	backends atomic.Pointer[[]*Backend]

	// updateMu serializes UpdateBackends so concurrent updates (config reload
	// and dynamic service discovery) each merge against a stable snapshot.
	updateMu sync.Mutex

	// done is closed by Close to stop pool-owned goroutines (active health
	// checks in Y1-05, discovery refreshers in Y2-05).
	done      chan struct{}
	closeOnce sync.Once
}

// NewPool builds a Pool from an upstream config. scheme is the proxy scheme
// (http or https) applied to each backend address.
func NewPool(cfg config.UpstreamConfig, scheme string) (*Pool, error) {
	if len(cfg.Servers) == 0 && !discoveryEnabled(cfg.Discovery) {
		return nil, fmt.Errorf("upstream %q has no servers", cfg.Name)
	}
	maxFails := cfg.MaxFails
	if maxFails < 1 {
		maxFails = 1
	}
	failTimeout := cfg.FailTimeout.Std()
	if failTimeout <= 0 {
		failTimeout = 10 * time.Second
	}
	p := &Pool{
		name:        cfg.Name,
		scheme:      scheme,
		balancer:    newBalancer(cfg.Strategy),
		maxFails:    maxFails,
		failTimeout: failTimeout,
		done:        make(chan struct{}),
	}
	bs := buildBackends(cfg.Servers, scheme)
	p.backends.Store(&bs)
	return p, nil
}

// buildBackends constructs backend instances from server configs.
func buildBackends(servers []config.UpstreamServer, scheme string) []*Backend {
	backends := make([]*Backend, 0, len(servers))
	for _, s := range servers {
		backends = append(backends, newBackend(s, scheme))
	}
	return backends
}

// newBackend builds a single backend with a normalized weight (minimum 1).
func newBackend(s config.UpstreamServer, scheme string) *Backend {
	weight := s.Weight
	if weight < 1 {
		weight = 1
	}
	b := &Backend{
		Address: s.Address,
		Weight:  weight,
		URL:     &url.URL{Scheme: scheme, Host: s.Address},
	}
	// A backend is healthy until an active checker (if any) proves otherwise.
	b.activeHealthy.Store(true)
	return b
}

// Name returns the pool name.
func (p *Pool) Name() string { return p.name }

// Backends returns the current backend set (for inspection/representative URL).
// The returned slice is shared and must not be modified by callers.
func (p *Pool) Backends() []*Backend { return *p.backends.Load() }

// Pick selects an available backend and increments its in-flight counter. The
// caller must call Release exactly once when the request completes.
func (p *Pool) Pick() (*Backend, error) {
	now := time.Now().UnixNano()
	backends := *p.backends.Load()
	avail := make([]*Backend, 0, len(backends))
	for _, b := range backends {
		if b.available(now) {
			avail = append(avail, b)
		}
	}
	if len(avail) == 0 {
		return nil, ErrNoAvailableBackend
	}
	b := p.balancer.pick(avail)
	if b == nil {
		return nil, ErrNoAvailableBackend
	}
	b.acquire()
	return b, nil
}

// Release decrements a backend's in-flight counter.
func (p *Pool) Release(b *Backend) {
	if b != nil {
		b.release()
	}
}

// MarkSuccess clears a backend's failure state.
func (p *Pool) MarkSuccess(b *Backend) {
	b.fails.Store(0)
	b.downUntil.Store(0)
}

// MarkFailure records a backend failure; after maxFails consecutive failures
// the backend is placed in cooldown for failTimeout.
func (p *Pool) MarkFailure(b *Backend) {
	if int(b.fails.Add(1)) >= p.maxFails {
		b.downUntil.Store(time.Now().Add(p.failTimeout).UnixNano())
		b.fails.Store(0)
	}
}

// UpdateBackends atomically replaces the pool's backend set, preserving the
// runtime state (in-flight count, passive-failure cooldown) of any backend
// whose address and weight are unchanged. New addresses get fresh backends;
// removed addresses are dropped (in-flight requests already holding a pointer
// keep working until they Release). This is the seam used by config reload and
// dynamic service discovery to update upstreams without a restart.
//
// A backend is reused only when both address and weight match, which keeps
// Backend.Weight effectively immutable for a backend's lifetime, so the
// weighted balancer can read it without synchronizing against updates.
func (p *Pool) UpdateBackends(servers []config.UpstreamServer) {
	p.updateMu.Lock()
	defer p.updateMu.Unlock()

	type key struct {
		addr   string
		weight int
	}
	current := p.Backends()
	prev := make(map[key]*Backend, len(current))
	for _, b := range current {
		prev[key{b.Address, b.Weight}] = b
	}

	next := make([]*Backend, 0, len(servers))
	for _, s := range servers {
		b := newBackend(s, p.scheme)
		k := key{b.Address, b.Weight}
		if existing, ok := prev[k]; ok {
			b = existing
			delete(prev, k) // reuse each surviving backend at most once
		}
		next = append(next, b)
	}
	p.backends.Store(&next)
}

// Close stops the pool's background goroutines. It is idempotent and safe to
// call from reload paths that replace a pool with a freshly built one.
func (p *Pool) Close() {
	p.closeOnce.Do(func() { close(p.done) })
}

// Done returns a channel closed when the pool is closed. Pool-owned goroutines
// (active health checks, discovery refreshers) select on it to exit.
func (p *Pool) Done() <-chan struct{} { return p.done }
