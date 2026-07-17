// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/config"
)

// Registry owns the lifecycle of named-upstream pools across configuration
// reloads. It is the wiring that gives pool-owned goroutines (active health
// checks today, service discovery later) a defined lifetime: pools are reused
// across reloads when their shape is unchanged (so passive-health state and a
// running checker are preserved and only the backend set is updated via
// UpdateBackends), and replaced or closed when they change or disappear.
//
// A concrete proxy_pass URL (for example "http://127.0.0.1:3000") builds an
// anonymous pool-of-one outside the registry: it has no name and no health
// checks, owns no goroutines, and is simply garbage-collected when no longer
// referenced. Only named upstreams are tracked here.
//
// Builds are serial (startup, then one reload at a time), so a build proceeds
// as Begin, one For per referenced upstream, then Commit on success or Abort on
// failure. The mutex guards against any incidental concurrent access and makes
// the registry safe for a future concurrent discovery refresher.
type Registry struct {
	opts RegistryOptions

	mu     sync.Mutex
	live   map[string]*poolEntry // committed pools currently serving
	staged map[string]*poolEntry // pools assembled by the in-progress build
}

// RegistryOptions configures a Registry. All fields are optional.
type RegistryOptions struct {
	Logger   *slog.Logger
	OnHealth HealthHook // backend health transitions -> gauge
	OnProbe  ProbeHook  // per-probe outcome -> counter + latency histogram
	// OnBackends reports a pool's current backend count (static pools at commit,
	// discovery pools after each successful resolve) -> gauge.
	OnBackends func(pool string, n int)
	// OnDiscoveryError reports a failed or empty discovery resolve -> counter.
	OnDiscoveryError func(pool string)
	// DialContext, when non-nil, guards the outbound connections made by the
	// Consul and Kubernetes discoverers against the [egress] allow-list. DNS
	// discovery uses the system resolver and is unaffected.
	DialContext DialFunc
}

// poolEntry pairs a live pool with the upstream shape it was built from, so a
// later reload can decide whether the pool can be reused or must be replaced.
// While staged during a build it also carries the backend update to apply, which
// is deferred to Commit so an aborted build never mutates a live pool.
type poolEntry struct {
	pool      *Pool
	meta      upstreamMeta
	reused    bool                    // staged: reuses a live pool (vs freshly built)
	pending   []config.UpstreamServer // staged: servers to apply at Commit when reused
	discovery bool                    // pool's backend set is owned by a discovery refresher
}

// upstreamMeta captures the fields that determine a pool's identity. When any of
// them change the pool is rebuilt rather than updated in place, because they are
// fixed at construction (balancer strategy, scheme) or define the checker
// (health-check settings).
type upstreamMeta struct {
	scheme      string
	strategy    string
	maxFails    int
	failTimeout time.Duration
	health      config.HealthCheckConfig
	// discoverySig captures the discovery config so a changed provider rebuilds
	// the pool (and its refresher) rather than being reused in place.
	discoverySig string
}

// equal reports whether two metas describe the same pool shape. It cannot use
// == because HealthCheckConfig carries a slice (ExpectStatus).
func (m upstreamMeta) equal(o upstreamMeta) bool {
	return m.scheme == o.scheme &&
		m.strategy == o.strategy &&
		m.maxFails == o.maxFails &&
		m.failTimeout == o.failTimeout &&
		m.discoverySig == o.discoverySig &&
		healthConfigEqual(m.health, o.health)
}

// healthConfigEqual compares two health-check blocks field by field.
func healthConfigEqual(a, b config.HealthCheckConfig) bool {
	if a.Enabled != b.Enabled ||
		a.Type != b.Type ||
		a.Path != b.Path ||
		a.Interval != b.Interval ||
		a.Timeout != b.Timeout ||
		a.HealthyThreshold != b.HealthyThreshold ||
		a.UnhealthyThreshold != b.UnhealthyThreshold ||
		a.ExpectBody != b.ExpectBody ||
		len(a.ExpectStatus) != len(b.ExpectStatus) {
		return false
	}
	for i := range a.ExpectStatus {
		if a.ExpectStatus[i] != b.ExpectStatus[i] {
			return false
		}
	}
	return true
}

// NewRegistry creates an empty pool registry.
func NewRegistry(opts RegistryOptions) *Registry {
	return &Registry{
		opts:   opts,
		live:   make(map[string]*poolEntry),
		staged: make(map[string]*poolEntry),
	}
}

// Begin starts a new build. It must be paired with exactly one of Commit or
// Abort.
func (r *Registry) Begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staged = make(map[string]*poolEntry)
}

// For returns the pool for a named upstream within the current build, creating
// it (and starting its health checker when enabled) on first reference, or
// reusing the live pool from a previous build when its shape is unchanged. When
// the shape changed, a fresh pool is staged and the previous one is closed at
// Commit. The returned pool is ready for Pick immediately.
func (r *Registry) For(up config.UpstreamConfig, scheme string) (*Pool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.staged[up.Name]; ok {
		// Already staged by an earlier location referencing the same upstream.
		return e.pool, nil
	}

	meta := metaOf(up, scheme)
	if e, ok := r.live[up.Name]; ok && e.meta.equal(meta) {
		// Same shape: keep the running pool (and its checker/refresher). The backend
		// set is refreshed at Commit (not here) so an aborted build leaves the live
		// pool untouched, preserving an atomic reload. A discovery pool's backends
		// are owned by its refresher, so its static seed is not re-applied.
		r.staged[up.Name] = &poolEntry{pool: e.pool, meta: meta, reused: true, pending: up.Servers, discovery: discoveryEnabled(up.Discovery)}
		return e.pool, nil
	}

	pool, err := NewPool(up, scheme)
	if err != nil {
		return nil, err
	}
	if up.HealthCheck != nil && up.HealthCheck.Enabled {
		pool.StartHealthChecks(*up.HealthCheck, r.opts.OnHealth, r.opts.OnProbe)
	}
	disco := discoveryEnabled(up.Discovery)
	if disco {
		d, err := newDiscoverer(*up.Discovery, r.opts.DialContext)
		if err != nil {
			// Stop any health checker already started on this fresh pool so the
			// rejected build leaks no goroutine, then fail the build.
			pool.Close()
			return nil, err
		}
		pool.StartDiscovery(d, up.Discovery.Refresh.Std(), DiscoveryHooks{
			OnBackends: r.opts.OnBackends,
			OnError:    r.opts.OnDiscoveryError,
		}, r.opts.Logger)
	}
	r.staged[up.Name] = &poolEntry{pool: pool, meta: meta, discovery: disco}
	return pool, nil
}

// Commit promotes the staged pools to live. It first applies the deferred
// backend update to each reused pool (preserving its passive-health and
// in-flight state), then closes every previously live pool that was not reused
// (removed upstreams and replaced pools), stopping their goroutines. It is
// called only after a successful build.
func (r *Registry) Commit() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.staged {
		// A discovery pool's backends are owned by its refresher; do not overwrite
		// them with the (possibly empty) static seed on reuse.
		if e.reused && !e.discovery {
			e.pool.UpdateBackends(e.pending)
		}
	}
	for name, e := range r.live {
		if staged, ok := r.staged[name]; !ok || staged.pool != e.pool {
			e.pool.Close()
			if r.opts.Logger != nil {
				r.opts.Logger.Info("upstream pool stopped", "upstream", name)
			}
		}
	}
	r.live = r.staged
	r.staged = make(map[string]*poolEntry)
	// Seed the backend-count gauge for every live pool (discovery pools also
	// update it from their refresher).
	if r.opts.OnBackends != nil {
		for name, e := range r.live {
			r.opts.OnBackends(name, len(e.pool.Backends()))
		}
	}
}

// Abort discards a failed build, closing every freshly created staged pool (a
// reused entry shares a live pool and is left running) so checker goroutines do
// not leak. Because backend updates are deferred to Commit, live pools are
// untouched and keep serving the previous configuration.
func (r *Registry) Abort() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.staged {
		if !e.reused {
			e.pool.Close()
		}
	}
	r.staged = make(map[string]*poolEntry)
}

// SnapshotPool returns an immutable snapshot of the live pool for name, or nil
// if the pool is not currently live. It is used by the factory's
// generation-scoped snapshot middleware to capture the backend set at
// generation commit time.
func (r *Registry) SnapshotPool(name string) *PoolSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.live[name]; ok {
		return e.pool.Snapshot()
	}
	return nil
}

// SnapshotPools returns the snapshots for each named live pool. Pools not
// currently live are omitted. It is called by HandlerFactory.Prepare to capture
// the generation-scoped pool view.
func (r *Registry) SnapshotPools(names []string) map[string]*PoolSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*PoolSnapshot, len(names))
	for _, name := range names {
		if e, ok := r.live[name]; ok {
			out[name] = e.pool.Snapshot()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// CloseAll stops every live pool's goroutines. It is called once at shutdown.
func (r *Registry) CloseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.live {
		e.pool.Close()
	}
	r.live = make(map[string]*poolEntry)
}

// PoolStatus is a point-in-time view of one live pool for operational
// inspection (the admin console upstream panel).
type PoolStatus struct {
	Name     string
	Strategy string
	Backends []BackendStatus
}

// BackendStatus is a point-in-time view of one backend within a pool.
type BackendStatus struct {
	Address  string
	Weight   int
	Healthy  bool
	Inflight int64
}

// Snapshot returns the current live pools and their backend health, sorted by
// pool name for stable presentation. It reads committed pools only, so an
// in-progress reload never exposes half-built state.
func (r *Registry) Snapshot() []PoolStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]PoolStatus, 0, len(r.live))
	for name, e := range r.live {
		ps := PoolStatus{Name: name, Strategy: e.meta.strategy}
		for _, b := range e.pool.Backends() {
			ps.Backends = append(ps.Backends, BackendStatus{
				Address:  b.Address,
				Weight:   b.Weight,
				Healthy:  b.Available(),
				Inflight: b.Inflight(),
			})
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// metaOf extracts the identity fields of an upstream.
func metaOf(up config.UpstreamConfig, scheme string) upstreamMeta {
	m := upstreamMeta{
		scheme:       scheme,
		strategy:     up.Strategy,
		maxFails:     up.MaxFails,
		failTimeout:  up.FailTimeout.Std(),
		discoverySig: discoverySignature(up.Discovery),
	}
	if up.HealthCheck != nil {
		m.health = *up.HealthCheck
	}
	return m
}

// discoverySignature builds a stable string identifying a discovery config, so a
// reload that changes any provider field rebuilds the pool (and restarts its
// refresher) rather than reusing the old one. It returns "" for static/no
// discovery so existing static pools keep being reused unchanged.
func discoverySignature(d *config.DiscoveryConfig) string {
	if !discoveryEnabled(d) {
		return ""
	}
	parts := []string{d.Type, d.Target, d.Refresh.Std().String()}
	if c := d.Consul; c != nil {
		passing := true
		if c.PassingOnly != nil {
			passing = *c.PassingOnly
		}
		parts = append(parts, "consul", c.Address, c.Service, c.Tag, c.Datacenter, c.Token, strconv.FormatBool(passing))
	}
	if k := d.Kubernetes; k != nil {
		parts = append(parts, "k8s", k.Namespace, k.Service, k.Port, k.APIServer, k.Token, k.CAFile, strconv.FormatBool(k.InsecureSkipTLSVerify))
	}
	return strings.Join(parts, "\x1f")
}
