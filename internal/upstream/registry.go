// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/resilience"
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

	// startHealthChecks is the per-registry activation seam. Production
	// uses Pool.StartHealthChecks; tests replace it to observe activation
	// directly instead of inferring worker creation from probe timing.
	startHealthChecks func(*Pool, config.HealthCheckConfig, HealthHook, ProbeHook)
	// startHealthChecksTLS is the policy-aware activation seam used in
	// production. The plain seam above is kept for tests that replace it.
	startHealthChecksTLS func(*Pool, config.HealthCheckConfig, *backendtls.Policy, HealthHook, ProbeHook)

	mu     sync.Mutex
	live   map[poolKey]*poolEntry // committed pools currently serving, keyed by (name, scheme)
	staged map[poolKey]*poolEntry // pools assembled by the in-progress build
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
	// NewDiscoverer overrides discoverer construction. When nil the default
	// provider selection is used. Tests inject this to avoid real network
	// lookups while exercising Registry.For.
	NewDiscoverer func(config.DiscoveryConfig, DialFunc) (Discoverer, error)
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

	// policy is the staged resolved resilience policy, applied to a reused pool
	// at Commit. A freshly built pool resolves its own in NewPool.
	policy *resilience.Policy

	// circuit is the staged breaker configuration, applied to a reused pool at
	// Commit. It is deliberately not part of upstreamMeta: retuning a threshold
	// used to rebuild the pool, which discarded the record of which backends
	// were currently out of rotation and put all of them back under full load
	// at once — during a reload, which is when that record matters most.
	circuit circuitParams

	// activation state is set on freshly built staged pools and consumed by
	// Activate after Commit promotes the pool to live (R9-07).
	needsHealth bool
	healthCfg   config.HealthCheckConfig
	// healthTLS is the pool's resolved backend trust policy, applied to the
	// probe client so a backend is never called healthy under weaker
	// verification than live traffic uses.
	healthTLS  *backendtls.Policy
	discoverer Discoverer
	discoCfg   config.DiscoveryConfig
}

// upstreamMeta captures the fields that determine a pool's identity. When any of
// them change the pool is rebuilt rather than updated in place, because they are
// fixed at construction (balancer strategy, scheme) or define the checker
// (health-check settings).
type upstreamMeta struct {
	scheme   string
	strategy string
	health   config.HealthCheckConfig
	// discoverySig captures the discovery config so a changed provider rebuilds
	// the pool (and its refresher) rather than being reused in place.
	discoverySig string
	// backendTLSSig is the resolved backend trust policy's fingerprint. It is
	// part of the pool's identity so a changed policy — including a certificate
	// rotated in place, which changes the fingerprint without changing the
	// configured paths — rebuilds the pool and with it the probe client. That
	// is what makes the whole field hot-reloadable rather than restart-bound.
	backendTLSSig string
}

// equal reports whether two metas describe the same pool shape. It cannot use
// == because HealthCheckConfig carries a slice (ExpectStatus).
func (m upstreamMeta) equal(o upstreamMeta) bool {
	return m.scheme == o.scheme &&
		m.strategy == o.strategy &&
		m.discoverySig == o.discoverySig &&
		m.backendTLSSig == o.backendTLSSig &&
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

// poolKey uniquely identifies a live/staged pool by upstream name and scheme.
type poolKey struct {
	name   string
	scheme string
}

// NewRegistry creates an empty pool registry.
func NewRegistry(opts RegistryOptions) *Registry {
	return &Registry{
		opts: opts,
		// Only the policy-aware seam is wired by default; startHealthChecks
		// stays nil so a test that replaces it is still observed, while
		// production always passes the pool's resolved trust policy to the
		// probe client.
		startHealthChecksTLS: (*Pool).StartHealthChecksWithTLS,
		live:                 make(map[poolKey]*poolEntry),
		staged:               make(map[poolKey]*poolEntry),
	}
}

// Begin starts a new build. It must be paired with exactly one of Commit or
// Abort.
func (r *Registry) Begin() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.staged = make(map[poolKey]*poolEntry)
}

// For returns the pool for a named upstream within the current build, creating
// it (and starting its health checker when enabled) on first reference, or
// reusing the live pool from a previous build when its shape is unchanged. When
// the shape changed, a fresh pool is staged and the previous one is closed at
// Commit. The returned pool is ready for Pick immediately. ctx bounds the
// initial discovery resolution; request-time cancellations do not affect
// already-built pools.
func (r *Registry) For(ctx context.Context, up config.UpstreamConfig, scheme string) (*Pool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := poolKey{name: up.Name, scheme: scheme}
	if e, ok := r.staged[key]; ok {
		// Already staged by an earlier location referencing the same upstream.
		return e.pool, nil
	}

	meta := metaOf(up, scheme)
	// Resolved before the reuse check so a malformed policy fails the staged
	// build — and with it the reload — whether or not the pool is being reused.
	// It is deliberately not part of upstreamMeta: a policy change swaps a
	// pointer and must never rebuild the pool, because rebuilding would discard
	// exactly the admission counters and backend state the policy governs.
	resPolicy, perr := resilience.Resolve(up.Resilience.Options())
	if perr != nil {
		return nil, fmt.Errorf("upstream %q: %w", up.Name, perr)
	}
	pending := up.Servers
	if e, ok := r.live[key]; ok && e.meta.equal(meta) {
		// Same shape: keep the running pool (and its checker/refresher). The backend
		// set is refreshed at Commit (not here) so an aborted build leaves the live
		// pool untouched, preserving an atomic reload. A discovery pool's backends
		// are owned by its refresher, so its static seed is not re-applied.
		//
		// For discovery-only upstreams the static seed is empty, so CandidateSnapshot
		// must build from the currently discovered backend set instead (R12-01).
		disco := discoveryEnabled(up.Discovery)
		if disco {
			pending = backendsToServers(e.pool.Backends())
		}
		r.staged[key] = &poolEntry{pool: e.pool, meta: meta, reused: true, pending: pending, discovery: disco, policy: resPolicy, circuit: circuitParamsOf(up)}
		return e.pool, nil
	}

	// Resolved here so a malformed policy fails the staged build — and with it
	// the reload — rather than surfacing as an unhealthy backend later.
	var policy *backendtls.Policy
	if up.BackendTLS != nil {
		resolved, rerr := backendtls.Resolve(up.BackendTLS.Options(), up.Name)
		if rerr != nil {
			return nil, fmt.Errorf("upstream %q: %w", up.Name, rerr)
		}
		policy = resolved
	}

	pool, err := NewPool(up, scheme)
	if err != nil {
		return nil, err
	}
	// Wired unconditionally, not only when health_check is enabled: a passive
	// (dial-triggered) transition deserves the same gauge/history entry as an
	// active-checker one even on a pool with no active checks configured.
	pool.SetHealthHook(r.opts.OnHealth)
	disco := discoveryEnabled(up.Discovery)
	var d Discoverer
	if disco {
		newDisco := r.opts.NewDiscoverer
		if newDisco == nil {
			newDisco = newDiscoverer
		}
		d, err = newDisco(*up.Discovery, r.opts.DialContext)
		if err != nil {
			pool.Close()
			return nil, err
		}
		// One-shot discovery resolution during preflight/build so the pool
		// has an initial backend set before it serves traffic. The periodic
		// refresher started by Activate keeps the set converged afterwards
		// (R10-03).
		resolveCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
		targets, resolveErr := d.Resolve(resolveCtx)
		cancel()
		if resolveErr == nil && len(targets) > 0 {
			pool.UpdateTargets(targets)
			servers := targetsToServers(targets)
			// CandidateSnapshot builds from pending; for discovery-only
			// upstreams the static seed is empty, so store the resolved
			// targets so build-time reflection sees the new backends
			// (R11-04).
			pending = servers
			if r.opts.OnBackends != nil {
				r.opts.OnBackends(up.Name, len(targets))
			}
		} else {
			if r.opts.OnDiscoveryError != nil {
				r.opts.OnDiscoveryError(up.Name)
			}
			if r.opts.Logger != nil {
				r.opts.Logger.Warn("discovery initial resolve failed; starting with empty pool",
					"upstream", up.Name, "discoverer", d.Describe(), "error", resolveErr)
			}
		}
	}
	entry := &poolEntry{
		pool:        pool,
		meta:        meta,
		pending:     pending,
		discovery:   disco,
		needsHealth: up.HealthCheck != nil && up.HealthCheck.Enabled,
		healthCfg:   healthCfgOrZero(up.HealthCheck),
		healthTLS:   policy,
		discoverer:  d,
		discoCfg:    discoveryCfgOrZero(up.Discovery),
	}
	r.staged[key] = entry
	return pool, nil
}

// healthCfgOrZero returns the enabled health check config or a zero value.
func healthCfgOrZero(cfg *config.HealthCheckConfig) config.HealthCheckConfig {
	if cfg != nil {
		return *cfg
	}
	return config.HealthCheckConfig{}
}

// discoveryCfgOrZero returns the discovery config or a zero value.
func discoveryCfgOrZero(cfg *config.DiscoveryConfig) config.DiscoveryConfig {
	if cfg != nil {
		return *cfg
	}
	return config.DiscoveryConfig{}
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
		// Retuned before the backend set is replaced, so a backend built by this
		// same reload starts on the new thresholds rather than the old ones.
		if e.reused {
			e.pool.setCircuitLimits(e.circuit)
		}
		// A discovery pool's backends are owned by its refresher; do not overwrite
		// them with the (possibly empty) static seed on reuse.
		if e.reused && !e.discovery {
			e.pool.UpdateBackends(e.pending)
		}
		// A freshly built pool already resolved its policy in NewPool. A reused one
		// takes it here, so the swap lands with the rest of the reload and an
		// aborted build leaves the live limits untouched. Raising a limit wakes
		// parked waiters; lowering one lets the excess drain.
		if e.reused && e.policy != nil {
			e.pool.SetPolicy(e.policy)
		}
	}
	for key, e := range r.live {
		if staged, ok := r.staged[key]; !ok || staged.pool != e.pool {
			e.pool.Close()
			if r.opts.Logger != nil {
				r.opts.Logger.Info("upstream pool stopped", "upstream", key.name, "scheme", key.scheme)
			}
		}
	}
	r.live = r.staged
	r.staged = make(map[poolKey]*poolEntry)
	// Seed the backend-count gauge for every live pool (discovery pools also
	// update it from their refresher).
	if r.opts.OnBackends != nil {
		for key, e := range r.live {
			r.opts.OnBackends(key.name, len(e.pool.Backends()))
		}
	}
}

// Activate starts background workers (active health checks and discovery
// refreshers) for every freshly built pool that was just committed. It must be
// called after Commit and before the new configuration is considered live.
// Reused pools already have running workers and are left untouched (R9-07).
func (r *Registry) Activate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	startHealthChecks := r.startHealthChecks
	if startHealthChecks == nil {
		startHealthChecks = (*Pool).StartHealthChecks
	}
	startHealthChecksTLS := r.startHealthChecksTLS
	if startHealthChecksTLS == nil {
		startHealthChecksTLS = (*Pool).StartHealthChecksWithTLS
	}
	for _, e := range r.live {
		if e.reused {
			continue
		}
		if e.needsHealth {
			if r.startHealthChecks != nil {
				// A test replaced the plain seam; keep observing it.
				startHealthChecks(e.pool, e.healthCfg, r.opts.OnHealth, r.opts.OnProbe)
			} else {
				startHealthChecksTLS(e.pool, e.healthCfg, e.healthTLS, r.opts.OnHealth, r.opts.OnProbe)
			}
		}
		if e.discovery {
			e.pool.StartDiscovery(e.discoverer, e.discoCfg.Refresh.Std(), DiscoveryHooks{
				OnBackends: r.opts.OnBackends,
				OnError:    r.opts.OnDiscoveryError,
			}, r.opts.Logger)
		}
		// Clear activation state now that it has been consumed.
		e.needsHealth = false
		e.healthTLS = nil
		e.discoverer = nil
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
	r.staged = make(map[poolKey]*poolEntry)
}

// CandidateSnapshot returns a static snapshot of the staged pool for name and
// scheme, built from the candidate config's server list. It is used by build-
// time consumers such as gRPC reflection so they see the new generation's
// backends rather than the previous generation's live set (R9-06). It returns
// nil when no pool is staged for the requested key.
func (r *Registry) CandidateSnapshot(name, scheme string) *PoolSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.staged[poolKey{name: name, scheme: scheme}]
	if !ok {
		return nil
	}
	return e.pool.staticSnapshot(e.pending)
}

// SnapshotPool returns an immutable snapshot of the live pool for name and
// scheme, or nil if the pool is not currently live.
func (r *Registry) SnapshotPool(name, scheme string) *PoolSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.live[poolKey{name: name, scheme: scheme}]; ok {
		return e.pool.Snapshot()
	}
	return nil
}

// PoolSnapshotKey identifies a named upstream together with the scheme used to
// reach it. It is the public key for SnapshotPools.
type PoolSnapshotKey struct {
	Name   string
	Scheme string
}

// SnapshotPools returns the snapshots for each named/schemed live pool. Pools
// not currently live are omitted. It is called by HandlerFactory.Prepare to
// capture the generation-scoped pool view.
func (r *Registry) SnapshotPools(keys []PoolSnapshotKey) SnapshotMap {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(SnapshotMap, len(keys))
	for _, k := range keys {
		if e, ok := r.live[poolKey{name: k.Name, scheme: k.Scheme}]; ok {
			out[k] = e.pool.Snapshot()
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
	r.live = make(map[poolKey]*poolEntry)
}

// PoolStatus is a point-in-time view of one live pool for operational
// inspection (the admin console upstream panel).
type PoolStatus struct {
	Name     string
	Scheme   string
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
	for key, e := range r.live {
		ps := PoolStatus{Name: key.name, Scheme: key.scheme, Strategy: e.meta.strategy}
		for _, b := range e.pool.Backends() {
			ps.Backends = append(ps.Backends, BackendStatus{
				Address:  b.Address,
				Weight:   b.Weight(),
				Healthy:  b.Available(),
				Inflight: b.Inflight(),
			})
		}
		out = append(out, ps)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// backendsToServers converts the current pool backend set into upstream server
// configs. It is used when staging a reused discovery pool so CandidateSnapshot
// can reflect the discovered backends even though the static servers list is
// empty (R12-01).
func backendsToServers(backends []*Backend) []config.UpstreamServer {
	servers := make([]config.UpstreamServer, 0, len(backends))
	for _, b := range backends {
		// Re-prefix a unix backend so the round trip through newBackend derives
		// the same network again.
		addr := b.Address
		if b.Network == NetworkUnix {
			addr = "unix:" + addr
		}
		servers = append(servers, config.UpstreamServer{Address: addr, Weight: b.Weight()})
	}
	return servers
}

// circuitParamsOf resolves an upstream's breaker thresholds, applying the same
// defaults NewPool does.
func circuitParamsOf(up config.UpstreamConfig) circuitParams {
	maxFails := up.MaxFails
	if maxFails < 1 {
		maxFails = 1
	}
	failTimeout := up.FailTimeout.Std()
	if failTimeout <= 0 {
		failTimeout = 10 * time.Second
	}
	return circuitParams{
		maxFails:       maxFails,
		failTimeout:    failTimeout,
		halfOpenProbes: up.Resilience.HalfOpenProbes(),
	}
}

// metaOf extracts the identity fields of an upstream.
func metaOf(up config.UpstreamConfig, scheme string) upstreamMeta {
	m := upstreamMeta{
		scheme:        scheme,
		strategy:      up.Strategy,
		discoverySig:  discoverySignature(up.Discovery),
		backendTLSSig: backendTLSSignature(up),
	}
	if up.HealthCheck != nil {
		m.health = *up.HealthCheck
	}
	return m
}

// backendTLSSignature returns the resolved policy's fingerprint, or "" when the
// pool declares no policy. A resolution failure yields a distinct marker rather
// than "" so a broken policy is never mistaken for an absent one; the same
// resolution runs again (and reports the error) when the probe client is built.
func backendTLSSignature(up config.UpstreamConfig) string {
	if up.BackendTLS == nil {
		return ""
	}
	policy, err := backendtls.Resolve(up.BackendTLS.Options(), up.Name)
	if err != nil {
		return "unresolvable:" + err.Error()
	}
	return policy.Fingerprint()
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
