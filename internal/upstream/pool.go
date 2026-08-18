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
	"jul/internal/logthrottle"
	"jul/internal/resilience"
)

// ErrNoAvailableBackend is returned by Pick when every backend is in cooldown.
var ErrNoAvailableBackend = errors.New("no available upstream backend")

// ErrBackendAtCapacity is returned by Pick when every otherwise-eligible backend
// is already at max_active_per_backend. It is deliberately distinct from
// ErrNoAvailableBackend: both are 503 to a client, but one asks the operator to
// raise a limit and the other to look at backend health.
var ErrBackendAtCapacity = errors.New("all upstream backends are at capacity")

// dialFailureLogInterval bounds how often AllowDialFailureLog admits a
// heartbeat for one pool, independent of request/connection volume.
const dialFailureLogInterval = 10 * time.Second

// Pool is a named set of backends fronted by a load-balancing strategy and
// passive health checking.
type Pool struct {
	name        string
	scheme      string
	strategy    string
	balancer    Balancer
	maxFails    int
	failTimeout time.Duration

	// healthHook, when set, is called on a passive-health transition (a dial or
	// request failure tripping a backend's cooldown, or a success clearing it).
	// It shares the active checker's HealthHook so a transition looks the same
	// on the gauge and the Console health-history panel regardless of which
	// mechanism caused it. It is set once by the registry and never by Pick's
	// callers directly, so it survives UpdateBackends and reuse across reload.
	healthHook HealthHook

	// dialLog throttles the per-pool dial-failure log heartbeat (AllowDialFailureLog).
	// It lives on the pool rather than a caller-local variable so stream and HTTP
	// share one bounded signal per backend pool, and — for a named upstream, which
	// the registry reuses across reload — a reload cannot reset it.
	dialLog logthrottle.Limiter

	// dynamic is true when the pool's backend set is owned by a discovery
	// refresher rather than a static server list. Dynamic snapshots read from
	// the live pool at request time so discovery convergence is visible to
	// in-flight requests; static snapshots freeze the backend set at commit.
	dynamic bool

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

	// admission bounds the pool's in-flight logical work and owns the resolved
	// resilience policy. It is created with the pool and never replaced, which is
	// what preserves counters and parked waiters across a policy swap.
	admission *Admission

	// budget is the pool-scoped retry allowance. Like admission it is created
	// once and never replaced: a policy swap changes the percentage and leaves
	// the accumulated window alone, so a reload cannot grant a fresh retry burst.
	budget *Budget
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
	policy, err := resilience.Resolve(cfg.Resilience.Options())
	if err != nil {
		return nil, fmt.Errorf("upstream %q: %w", cfg.Name, err)
	}
	p := &Pool{
		name:        cfg.Name,
		scheme:      scheme,
		strategy:    cfg.Strategy,
		balancer:    newBalancer(cfg.Strategy),
		maxFails:    maxFails,
		failTimeout: failTimeout,
		dynamic:     discoveryEnabled(cfg.Discovery),
		done:        make(chan struct{}),
		admission:   NewAdmission(policy),
		budget:      NewBudget(policy.RetryBudgetPercent()),
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

// newBackend builds a single backend with a normalized weight (minimum 1). A
// unix-socket address produces a backend with no URL: there is nothing
// meaningful to put in one, and consumers read Network and Address instead.
func newBackend(s config.UpstreamServer, scheme string) *Backend {
	weight := s.Weight
	if weight < 1 {
		weight = 1
	}
	network, address := ParseSocketAddress(s.Address)
	b := &Backend{
		Address: address,
		Network: network,
		scheme:  scheme,
	}
	if network == NetworkTCP {
		b.URL = &url.URL{Scheme: scheme, Host: address}
	}
	b.setWeight(weight)
	// A backend is healthy until an active checker (if any) proves otherwise.
	b.activeHealthy.Store(true)
	return b
}

// Name returns the pool name.
func (p *Pool) Name() string { return p.name }

// Admission returns the pool's admission owner. Callers admit before doing any
// upstream work and release exactly once when that work ends.
func (p *Pool) Admission() *Admission { return p.admission }

// Policy returns the pool's resolved resilience policy. It never returns nil.
func (p *Pool) Policy() *resilience.Policy { return p.admission.Policy() }

// SetPolicy swaps the pool's resolved resilience policy. This is the whole of a
// resilience reload: no pool is rebuilt, so admission counters, parked waiters
// and backend state all survive.
func (p *Pool) SetPolicy(policy *resilience.Policy) {
	p.admission.SetPolicy(policy)
	p.budget.SetPercent(policy.RetryBudgetPercent())
}

// Backends returns the current backend set (for inspection/representative URL).
// The returned slice is shared and must not be modified by callers.
func (p *Pool) Backends() []*Backend { return *p.backends.Load() }

// Pick selects an available backend and increments its in-flight counter. The
// caller must call Release exactly once when the request completes.
func (p *Pool) Pick() (*Backend, error) {
	return p.pickExcluding(nil)
}

// Release decrements a backend's in-flight counter.
func (p *Pool) Release(b *Backend) {
	if b != nil {
		b.Release()
	}
}

// MarkSuccess clears a backend's failure state. It reports whether the backend
// was in cooldown beforehand, so a caller can log the recovery transition
// exactly once instead of on every subsequent success.
func (p *Pool) MarkSuccess(b *Backend) bool {
	wasDown := b.downUntil.Load() != 0
	b.fails.Store(0)
	b.downUntil.Store(0)
	if wasDown && p.healthHook != nil {
		p.healthHook(p.name, b.Address, true)
	}
	return wasDown
}

// MarkFailure records a backend failure; after maxFails consecutive failures
// the backend is placed in cooldown for failTimeout. It reports whether this
// call is the one that tripped the cooldown, so a caller can log that
// transition once instead of on every subsequent failure against an
// already-down backend.
func (p *Pool) MarkFailure(b *Backend) bool {
	if int(b.fails.Add(1)) >= p.maxFails {
		b.downUntil.Store(time.Now().Add(p.failTimeout).UnixNano())
		b.fails.Store(0)
		if p.healthHook != nil {
			p.healthHook(p.name, b.Address, false)
		}
		return true
	}
	return false
}

// SetHealthHook wires the passive-health transition hook. It is called once by
// the registry when a pool is built, using the same HealthHook as the active
// checker (RegistryOptions.OnHealth), so passive and active transitions feed
// one gauge and one Console health-history entry per backend.
func (p *Pool) SetHealthHook(h HealthHook) { p.healthHook = h }

// AllowDialFailureLog reports whether a throttled dial-failure heartbeat may be
// logged now for this pool. Callers still count every failure regardless of
// this result; it only bounds the log line, matching the counter-plus-throttle
// shape used elsewhere in the codebase (internal/logthrottle) rather than
// dropping the signal entirely.
func (p *Pool) AllowDialFailureLog() bool { return p.dialLog.Allow(dialFailureLogInterval) }

// UpdateBackends atomically replaces the pool's backend set, preserving the
// runtime state (in-flight count, passive-failure cooldown) of any backend
// whose address is unchanged. New addresses get fresh backends; removed
// addresses are dropped (in-flight requests already holding a pointer keep
// working until they Release). This is the seam used by config reload and
// dynamic service discovery to update upstreams without a restart.
//
// The reuse key is the address alone. Including the weight, as it once did,
// meant a Consul or DNS-SRV weight flap silently discarded that backend's
// in-flight accounting and failure history — exactly when an operator is
// watching them. A changed weight is now applied in place, which is safe
// because Backend.weight is atomic and the only hot-path reader holds
// weightedRR's own mutex.
func (p *Pool) UpdateBackends(servers []config.UpstreamServer) {
	p.updateMu.Lock()
	defer p.updateMu.Unlock()

	current := p.Backends()
	type key struct{ network, address string }
	prev := make(map[key]*Backend, len(current))
	for _, b := range current {
		prev[key{b.Network, b.Address}] = b
	}

	next := make([]*Backend, 0, len(servers))
	for _, s := range servers {
		b := newBackend(s, p.scheme)
		k := key{b.Network, b.Address}
		if existing, ok := prev[k]; ok {
			existing.setWeight(b.Weight())
			b = existing
			delete(prev, k) // reuse each surviving backend at most once
		}
		next = append(next, b)
	}
	p.backends.Store(&next)
	p.balancer.updateBackends(next)
}

// Close stops the pool's background goroutines. It is idempotent and safe to
// call from reload paths that replace a pool with a freshly built one. Parked
// waiters are woken and rejected rather than left holding a pool that no longer
// has a health checker or a discovery refresher behind it.
func (p *Pool) Close() {
	p.closeOnce.Do(func() {
		close(p.done)
		p.admission.Retire()
	})
}

// Done returns a channel closed when the pool is closed. Pool-owned goroutines
// (active health checks, discovery refreshers) select on it to exit.
func (p *Pool) Done() <-chan struct{} { return p.done }
