#!/usr/bin/env python3
from pathlib import Path


def replace(path, old, new, count=1):
    p = Path(path)
    s = p.read_text()
    actual = s.count(old)
    if actual != count:
        raise SystemExit(f"{path}: expected {count}, found {actual} for {old[:100]!r}")
    p.write_text(s.replace(old, new, count))

# Pool gets an independently replaceable/fenceable discovery worker generation.
replace("internal/upstream/pool.go",
'''\tdone      chan struct{}
\tcloseOnce sync.Once
''',
'''\tdone      chan struct{}
\tcloseOnce sync.Once

\t// Discovery is an auxiliary Boundary-C worker whose policy generation can
\t// change while this backend pool stays alive for already-admitted requests.
\t// Keep its cancellation/fencing separate from Close: a policy Publish must
\t// stop generation A from starting another refresh immediately, without
\t// retiring the pool admission/circuit state still referenced by old handlers.
\tdiscoveryMu     sync.Mutex
\tdiscoveryEpoch  atomic.Uint64
\tdiscoveryCancel context.CancelFunc
''')
replace("internal/upstream/pool.go", 'import (\n', 'import (\n\t"context"\n', 1)
replace("internal/upstream/pool.go",
'''func (p *Pool) Close() {
\tp.closeOnce.Do(func() {
\t\tclose(p.done)
\t\tp.admission.Retire()
\t})
}
''',
'''func (p *Pool) Close() {
\tp.closeOnce.Do(func() {
\t\tp.StopDiscovery()
\t\tclose(p.done)
\t\tp.admission.Retire()
\t})
}

// StopDiscovery fences and cancels only the current discovery worker. It does
// not retire the pool or its admission state. This is the #94 Publish seam: a
// Consul/Kubernetes worker built under policy A cannot begin another refresh
// after policy B is committed, while old handler-generation requests can keep
// using this pool until their normal generation retirement.
func (p *Pool) StopDiscovery() {
\tif p == nil {
\t\treturn
\t}
\tp.discoveryMu.Lock()
\tp.discoveryEpoch.Add(1) // fence any result already in flight
\tif p.discoveryCancel != nil {
\t\tp.discoveryCancel()
\t\tp.discoveryCancel = nil
\t}
\tp.discoveryMu.Unlock()
}

func (p *Pool) beginDiscoveryGeneration() (context.Context, uint64) {
\tp.discoveryMu.Lock()
\tdefer p.discoveryMu.Unlock()
\tif p.discoveryCancel != nil {
\t\tp.discoveryCancel()
\t}
\tctx, cancel := context.WithCancel(context.Background())
\tp.discoveryCancel = cancel
\tepoch := p.discoveryEpoch.Add(1)
\treturn ctx, epoch
}

func (p *Pool) discoveryGenerationCurrent(epoch uint64) bool {
\treturn p != nil && p.discoveryEpoch.Load() == epoch
}
''')

# Discovery worker uses its own cancellable epoch and ignores stale results.
replace("internal/upstream/discovery.go",
'''// StartDiscovery launches the pool's discovery refresher goroutine. It performs
// an immediate first resolve, then re-resolves every refresh interval until the
// pool is Closed (via Done). A failed or empty resolve keeps the last-good
// backend set in place, so a provider blip or a transient empty response does
// not black-hole traffic. It must be called at most once per pool.
func (p *Pool) StartDiscovery(d Discoverer, refresh time.Duration, hooks DiscoveryHooks, log *slog.Logger) {
''',
'''// StartDiscovery installs one discovery-worker generation. Installing a new
// generation cancels and fences the previous one without closing the backend
// pool. That distinction is required by #94: policy B must stop policy-A
// Consul/Kubernetes refreshes at Publish while old handler work may still use
// the same pool until it drains. A stale resolve result is ignored even if its
// provider returns after cancellation.
func (p *Pool) StartDiscovery(d Discoverer, refresh time.Duration, hooks DiscoveryHooks, log *slog.Logger) {
''')
replace("internal/upstream/discovery.go",
'''\tif ld, ok := d.(loggingDiscoverer); ok && log != nil {
\t\tld.SetLogger(log)
\t}
\tgo func() {
\t\tdefer closeDiscoverer(d)
\t\tp.refreshOnce(d, hooks, log)
''',
'''\tif ld, ok := d.(loggingDiscoverer); ok && log != nil {
\t\tld.SetLogger(log)
\t}
\tworkerCtx, epoch := p.beginDiscoveryGeneration()
\tgo func() {
\t\tdefer closeDiscoverer(d)
\t\tp.refreshOnce(workerCtx, epoch, d, hooks, log)
''')
replace("internal/upstream/discovery.go",
'''\t\t\tselect {
\t\t\tcase <-p.Done():
\t\t\t\tif log != nil {
\t\t\t\t\tlog.Warn("stopping discovery refresher", "upstream", p.name)
\t\t\t\t}
\t\t\t\treturn
\t\t\tcase <-timer.C:
\t\t\t\tp.refreshOnce(d, hooks, log)
''',
'''\t\t\tselect {
\t\t\tcase <-workerCtx.Done():
\t\t\t\tif log != nil {
\t\t\t\t\tlog.Warn("stopping discovery refresher", "upstream", p.name)
\t\t\t\t}
\t\t\t\treturn
\t\t\tcase <-p.Done():
\t\t\t\treturn
\t\t\tcase <-timer.C:
\t\t\t\tp.refreshOnce(workerCtx, epoch, d, hooks, log)
''')
replace("internal/upstream/discovery.go",
'''func (p *Pool) refreshOnce(d Discoverer, hooks DiscoveryHooks, log *slog.Logger) {
''',
'''func (p *Pool) refreshOnce(workerCtx context.Context, epoch uint64, d Discoverer, hooks DiscoveryHooks, log *slog.Logger) {
''')
replace("internal/upstream/discovery.go",
'''\tctx, cancel := context.WithTimeout(context.Background(), discoveryTimeout)
''',
'''\tctx, cancel := context.WithTimeout(workerCtx, discoveryTimeout)
''')
replace("internal/upstream/discovery.go",
'''\ttargets, err := d.Resolve(ctx)
\tif log != nil {
''',
'''\ttargets, err := d.Resolve(ctx)
\t// A policy-generation change may cancel an in-flight provider request. Even
\t// if the provider ignores cancellation and returns later, its A-generation
\t// result must never overwrite the B-generation backend view.
\tif !p.discoveryGenerationCurrent(epoch) {
\t\treturn
\t}
\tif log != nil {
''')

# Registry stages discovery-client replacement on the existing pool instead of
# replacing/retiring the pool itself.
replace("internal/upstream/registry.go",
'''\tdiscoverer Discoverer
\tdiscoCfg   config.DiscoveryConfig
\tegressGen  uint64
''',
'''\tdiscoverer      Discoverer
\tdiscoCfg        config.DiscoveryConfig
\tegressGen       uint64
\treplaceDiscovery bool
\tpendingTargets   []Target
''')

old_reuse = '''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) && (!egressSensitiveDiscovery || e.egressGen == r.stagedEgressID) {
\t\t// Same shape: keep the running pool (and its checker/refresher). The backend
\t\t// set is refreshed at Commit (not here) so an aborted build leaves the live
\t\t// pool untouched, preserving an atomic reload. A discovery pool's backends
\t\t// are owned by its refresher, so its static seed is not re-applied.
\t\t//
\t\t// For discovery-only upstreams the static seed is empty, so CandidateSnapshot
\t\t// must build from the currently discovered backend set instead (R12-01).
\t\tif disco {
\t\t\tpending = backendsToServers(e.pool.Backends())
\t\t}
\t\tr.staged[key] = &poolEntry{pool: e.pool, meta: meta, reused: true, pending: pending, discovery: disco, policy: resPolicy, circuit: circuitParamsOf(up), egressGen: e.egressGen}
\t\treturn e.pool, nil
\t}
'''
new_reuse = '''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) {
\t\t// Same pool shape: preserve admission/circuit/in-flight state. An egress
\t\t// generation change replaces only the Consul/Kubernetes discovery worker
\t\t// and its HTTP transport; rebuilding the pool would reject old-generation
\t\t// waiters before their handler generation drained (#94).
\t\tif disco {
\t\t\tpending = backendsToServers(e.pool.Backends())
\t\t}
\t\tentry := &poolEntry{pool: e.pool, meta: meta, reused: true, pending: pending, discovery: disco, policy: resPolicy, circuit: circuitParamsOf(up), egressGen: e.egressGen}
\t\tif egressSensitiveDiscovery && e.egressGen != r.stagedEgressID {
\t\t\tnewDisco := r.opts.NewDiscoverer
\t\t\tif newDisco == nil {
\t\t\t\tnewDisco = newDiscoverer
\t\t\t}
\t\t\td, derr := newDisco(*up.Discovery, r.stagedDial)
\t\t\tif derr != nil {
\t\t\t\treturn nil, derr
\t\t\t}
\t\t\tentry.discoverer = d
\t\t\tentry.discoCfg = *up.Discovery
\t\t\tentry.egressGen = r.stagedEgressID
\t\t\tentry.replaceDiscovery = true

\t\t\tresolveCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
\t\t\ttargets, resolveErr := d.Resolve(resolveCtx)
\t\t\tcancel()
\t\t\tif resolveErr == nil && len(targets) > 0 {
\t\t\t\tentry.pendingTargets = targets
\t\t\t\tentry.pending = targetsToServers(targets)
\t\t\t} else if r.opts.Logger != nil {
\t\t\t\tr.opts.Logger.Warn("candidate discovery generation initial resolve failed; keeping last-good backends",
\t\t\t\t\t"upstream", up.Name, "discoverer", d.Describe(), "error", resolveErr)
\t\t\t}
\t\t}
\t\tr.staged[key] = entry
\t\treturn e.pool, nil
\t}
'''
replace("internal/upstream/registry.go", old_reuse, new_reuse)

replace("internal/upstream/registry.go",
'''\t\t// A discovery pool's backends are owned by its refresher; do not overwrite
\t\t// them with the (possibly empty) static seed on reuse.
\t\tif e.reused && !e.discovery {
\t\t\te.pool.UpdateBackends(e.pending)
\t\t}
''',
'''\t\t// A discovery pool's backends are normally owned by its refresher. When
\t\t// replacing only the Consul/Kubernetes egress generation, fence/cancel A
\t\t// here and atomically seed the pool with B's preflight result if one was
\t\t// available. The pool itself stays live for old handler-generation work.
\t\tif e.reused && e.replaceDiscovery {
\t\t\te.pool.StopDiscovery()
\t\t\tif len(e.pendingTargets) > 0 {
\t\t\t\te.pool.UpdateTargets(e.pendingTargets)
\t\t\t}
\t\t} else if e.reused && !e.discovery {
\t\t\te.pool.UpdateBackends(e.pending)
\t\t}
''')

replace("internal/upstream/registry.go",
'''\tfor key, e := range r.live {
\t\tif e.reused {
\t\t\tcontinue
\t\t}
''',
'''\tfor key, e := range r.live {
\t\tif e.reused && !e.replaceDiscovery {
\t\t\tcontinue
\t\t}
''')
replace("internal/upstream/registry.go",
'''\t\tif e.needsHealth {
''',
'''\t\tif !e.reused && e.needsHealth {
''')
replace("internal/upstream/registry.go",
'''\t\tif e.discovery {
\t\t\te.pool.StartDiscovery(e.discoverer, e.discoCfg.Refresh.Std(), DiscoveryHooks{
''',
'''\t\tif e.discovery && e.discoverer != nil {
\t\t\te.pool.StartDiscovery(e.discoverer, e.discoCfg.Refresh.Std(), DiscoveryHooks{
''')
replace("internal/upstream/registry.go",
'''\t\te.needsHealth = false
\t\te.healthTLS = nil
\t\te.discoverer = nil
''',
'''\t\te.needsHealth = false
\t\te.healthTLS = nil
\t\te.discoverer = nil
\t\te.replaceDiscovery = false
\t\te.pendingTargets = nil
''')
replace("internal/upstream/registry.go",
'''\tfor _, e := range r.staged {
\t\tif !e.reused {
\t\t\t// Activate has not run on an aborted generation, so ownership of the
\t\t\t// candidate discoverer's HTTP transport is still here rather than in
\t\t\t// the refresher goroutine.
\t\t\tcloseDiscoverer(e.discoverer)
\t\t\te.pool.Close()
\t\t}
\t}
''',
'''\tfor _, e := range r.staged {
\t\tif e.replaceDiscovery {
\t\t\t// The candidate worker never became live; close its candidate HTTP
\t\t\t// transport without touching the live pool/worker generation.
\t\t\tcloseDiscoverer(e.discoverer)
\t\t\tcontinue
\t\t}
\t\tif !e.reused {
\t\t\t// Activate has not run on an aborted generation, so ownership of the
\t\t\t// candidate discoverer's HTTP transport is still here rather than in
\t\t\t// the refresher goroutine.
\t\t\tcloseDiscoverer(e.discoverer)
\t\t\te.pool.Close()
\t\t}
\t}
''')

# Update the focused tests to assert same-pool worker generation replacement.
p = Path("internal/upstream/egress_generation_test.go")
s = p.read_text()
s = s.replace('''\tif poolB == poolA {
\t\tt.Fatal("changed egress generation reused old discovery pool")
\t}
''', '''\tif poolB != poolA {
\t\tt.Fatal("egress generation change must preserve the backend pool and replace only its discovery worker")
\t}
''')
s = s.replace('''\tif candidate == live {
\t\tt.Fatal("candidate generation unexpectedly reused live discovery pool")
\t}
''', '''\tif candidate != live {
\t\tt.Fatal("candidate egress generation should reuse the live backend pool until Publish")
\t}
''')
p.write_text(s)

print("discovery worker generations patched")
