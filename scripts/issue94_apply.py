#!/usr/bin/env python3
from pathlib import Path


def replace(path, old, new, count=1):
    p = Path(path)
    s = p.read_text()
    actual = s.count(old)
    if actual != count:
        raise SystemExit(f"{path}: expected {count} occurrence(s), found {actual}: {old[:100]!r}")
    p.write_text(s.replace(old, new, count))

# ---- auth: generation-owned HTTP pools -------------------------------------
replace("internal/auth/auth.go",
'''\tlogger     *slog.Logger
\tonDecision func(method, result string)
}''',
'''\tlogger     *slog.Logger
\tonDecision func(method, result string)

\t// ownedClient is non-nil only for the default JWT/forward-auth client this
\t// Authenticator created. It is generation-owned and its idle H1/H2 pools are
\t// retired when the handler generation drains (#94). A caller-supplied client
\t// remains caller-owned and is never closed here.
\townedClient *http.Client
}''')
replace("internal/auth/auth.go",
'''\tcase cfg.JWT != nil:
\t\tclient := opts.HTTPClient
\t\tif client == nil {
\t\t\tclient = jwksHTTPClient(opts.DialContext, cfg.JWT.Timeout.Std())
\t\t}
\t\ta.jwt = newJWTAuth(cfg.JWT.JWKSURL, cfg.JWT.Issuer, cfg.JWT.Audience, cfg.JWT.Algorithms, client, opts.JWKSPool)
\tcase cfg.ForwardAuth != nil:
\t\tclient := opts.HTTPClient
\t\tif client == nil {
\t\t\tclient = forwardHTTPClient(opts.DialContext, cfg.ForwardAuth.Timeout.Std())
\t\t}
\t\ta.forward = newForwardAuth(cfg.ForwardAuth.URL, cfg.ForwardAuth.AuthResponseHeaders, client, opts.ForwardPool)
''',
'''\tcase cfg.JWT != nil:
\t\tclient := opts.HTTPClient
\t\tif client == nil {
\t\t\tclient = jwksHTTPClient(opts.DialContext, cfg.JWT.Timeout.Std())
\t\t\ta.ownedClient = client
\t\t}
\t\ta.jwt = newJWTAuth(cfg.JWT.JWKSURL, cfg.JWT.Issuer, cfg.JWT.Audience, cfg.JWT.Algorithms, client, opts.JWKSPool)
\tcase cfg.ForwardAuth != nil:
\t\tclient := opts.HTTPClient
\t\tif client == nil {
\t\t\tclient = forwardHTTPClient(opts.DialContext, cfg.ForwardAuth.Timeout.Std())
\t\t\ta.ownedClient = client
\t\t}
\t\ta.forward = newForwardAuth(cfg.ForwardAuth.URL, cfg.ForwardAuth.AuthResponseHeaders, client, opts.ForwardPool)
''')
replace("internal/auth/auth.go",
'''\treturn a, nil
}

// Wrap returns middleware''',
'''\treturn a, nil
}

// Close retires idle connections owned by this authenticator's generation.
// Active exchanges are not cancelled: GenerationResources calls Close only
// after the old handler generation has drained. Caller-supplied HTTP clients
// remain caller-owned and are deliberately untouched.
func (a *Authenticator) Close() error {
\tif a != nil && a.ownedClient != nil {
\t\ta.ownedClient.CloseIdleConnections()
\t}
\treturn nil
}

// Wrap returns middleware''')

# ---- plugins: select egress guard per handler generation -------------------
replace("internal/plugins/runtime.go",
'''func (m *Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
\ts := &Set{plugins: make(map[string]*plugin, len(cfg))}''',
'''func (m *Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
\treturn m.BuildWithEgress(ctx, cfg, m.egressWrap)
}

// BuildWithEgress is Build with an explicit generation-scoped global egress
// wrapper. HandlerFactory uses it so every newly published plugin Set captures
// the candidate egress generation while the process-lifetime Manager keeps its
// compilation cache and KV store. A nil wrapper preserves plugin-local SSRF and
// allowed_hosts enforcement without a global egress policy.
func (m *Manager) BuildWithEgress(ctx context.Context, cfg map[string]config.PluginConfig, egressWrap func(base DialFunc) DialFunc) (*Set, error) {
\ts := &Set{plugins: make(map[string]*plugin, len(cfg))}''')
replace("internal/plugins/runtime.go",
'''\t\tp, err := m.compilePlugin(ctx, name, pc)''',
'''\t\tp, err := m.compilePlugin(ctx, name, pc, egressWrap)''')
replace("internal/plugins/runtime.go",
'''func (m *Manager) compilePlugin(ctx context.Context, name string, pc config.PluginConfig) (*plugin, error) {''',
'''func (m *Manager) compilePlugin(ctx context.Context, name string, pc config.PluginConfig, egressWrap func(base DialFunc) DialFunc) (*plugin, error) {''')
replace("internal/plugins/runtime.go",
'''\t\tegressWrap:   m.egressWrap,''',
'''\t\tegressWrap:   egressWrap,''')
replace("internal/plugins/stub.go",
'''func (*Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
\tif len(cfg) > 0 {
\t\treturn nil, errors.New("plugins are configured but this build was compiled without the \\"wasmplugins\\" tag")
\t}
\treturn &Set{}, nil
}''',
'''func (m *Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
\treturn m.BuildWithEgress(ctx, cfg, nil)
}

// BuildWithEgress mirrors the compiled build. The wrapper is intentionally
// ignored because this build cannot instantiate plugins.
func (*Manager) BuildWithEgress(ctx context.Context, cfg map[string]config.PluginConfig, _ func(base DialFunc) DialFunc) (*Set, error) {
\tif len(cfg) > 0 {
\t\treturn nil, errors.New("plugins are configured but this build was compiled without the \\"wasmplugins\\" tag")
\t}
\treturn &Set{}, nil
}''')

# ---- discovery: worker/pool identity includes egress generation -----------
replace("internal/upstream/discovery.go", 'import (\n\t"context"', 'import (\n\t"context"\n\t"io"')
replace("internal/upstream/discovery.go",
'''\tgo func() {
\t\tp.refreshOnce(d, hooks, log)''',
'''\tgo func() {
\t\tdefer closeDiscoverer(d)
\t\tp.refreshOnce(d, hooks, log)''')
replace("internal/upstream/discovery.go",
'''func targetsToServers(targets []Target) []config.UpstreamServer {''',
'''func closeDiscoverer(d Discoverer) {
\tif closer, ok := d.(io.Closer); ok {
\t\t_ = closer.Close()
\t}
}

func targetsToServers(targets []Target) []config.UpstreamServer {''')
replace("internal/upstream/disco_consul.go",
'''func (d *consulDiscoverer) Describe() string { return d.describe }''',
'''func (d *consulDiscoverer) Describe() string { return d.describe }

// Close retires the discoverer's idle HTTP/1.1 and HTTP/2 connections.
func (d *consulDiscoverer) Close() error {
\tif d != nil && d.client != nil {
\t\td.client.CloseIdleConnections()
\t}
\treturn nil
}''')
replace("internal/upstream/disco_k8s.go",
'''func (d *k8sDiscoverer) Describe() string { return d.describe }''',
'''func (d *k8sDiscoverer) Describe() string { return d.describe }

// Close retires the discoverer's idle HTTP/1.1 and HTTP/2 connections.
func (d *k8sDiscoverer) Close() error {
\tif d != nil && d.client != nil {
\t\td.client.CloseIdleConnections()
\t}
\treturn nil
}''')

replace("internal/upstream/registry.go",
'''\tmu     sync.Mutex
\tlive   map[poolKey]*poolEntry // committed pools currently serving, keyed by (name, scheme)
\tstaged map[poolKey]*poolEntry // pools assembled by the in-progress build
}''',
'''\tmu     sync.Mutex
\tlive   map[poolKey]*poolEntry // committed pools currently serving, keyed by (name, scheme)
\tstaged map[poolKey]*poolEntry // pools assembled by the in-progress build

\t// stagedDial/stagedEgressID belong to the in-progress handler generation.
\t// HandlerFactory sets them immediately after Begin. A discovery pool can be
\t// reused only when this ID matches the generation that created its worker;
\t// otherwise Consul/Kubernetes gets a fresh client/transport and worker so a
\t// tightened egress policy cannot inherit an old H1/H2 pool (#94).
\tstagedDial     DialFunc
\tstagedEgressID uint64
}''')
replace("internal/upstream/registry.go",
'''\tdiscoverer Discoverer
\tdiscoCfg   config.DiscoveryConfig
}''',
'''\tdiscoverer Discoverer
\tdiscoCfg   config.DiscoveryConfig
\tegressGen  uint64
}''')
replace("internal/upstream/registry.go",
'''func (r *Registry) Begin() {
\tr.mu.Lock()
\tdefer r.mu.Unlock()
\tr.staged = make(map[poolKey]*poolEntry)
}''',
'''func (r *Registry) Begin() {
\tr.mu.Lock()
\tdefer r.mu.Unlock()
\tr.staged = make(map[poolKey]*poolEntry)
\tr.stagedDial = r.opts.DialContext
\tr.stagedEgressID = 0
}

// SetEgressGeneration binds the current staged build to one immutable egress
// generation. It must be called after Begin and before For. ID 0 preserves the
// legacy RegistryOptions.DialContext behavior for callers that do not use the
// process-level egress generation manager.
func (r *Registry) SetEgressGeneration(id uint64, dial DialFunc) {
\tr.mu.Lock()
\tdefer r.mu.Unlock()
\tr.stagedEgressID = id
\tr.stagedDial = dial
}''')
replace("internal/upstream/registry.go",
'''\tmeta := metaOf(up, scheme)
\t// Resolved before the reuse check''',
'''\tmeta := metaOf(up, scheme)
\tdisco := discoveryEnabled(up.Discovery)
\t// Resolved before the reuse check''')
replace("internal/upstream/registry.go",
'''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) {''',
'''\tif e, ok := r.live[key]; ok && e.meta.equal(meta) && (!disco || e.egressGen == r.stagedEgressID) {''')
replace("internal/upstream/registry.go",
'''\t\tdisco := discoveryEnabled(up.Discovery)
\t\tif disco {''',
'''\t\tif disco {''')
replace("internal/upstream/registry.go",
'''\t\tr.staged[key] = &poolEntry{pool: e.pool, meta: meta, reused: true, pending: pending, discovery: disco, policy: resPolicy, circuit: circuitParamsOf(up)}''',
'''\t\tr.staged[key] = &poolEntry{pool: e.pool, meta: meta, reused: true, pending: pending, discovery: disco, policy: resPolicy, circuit: circuitParamsOf(up), egressGen: e.egressGen}''')
replace("internal/upstream/registry.go",
'''\tdisco := discoveryEnabled(up.Discovery)
\tvar d Discoverer''',
'''\tvar d Discoverer''')
replace("internal/upstream/registry.go",
'''\t\td, err = newDisco(*up.Discovery, r.opts.DialContext)''',
'''\t\td, err = newDisco(*up.Discovery, r.stagedDial)''')
replace("internal/upstream/registry.go",
'''\t\tdiscoCfg:    discoveryCfgOrZero(up.Discovery),
\t}''',
'''\t\tdiscoCfg:    discoveryCfgOrZero(up.Discovery),
\t\tegressGen:   r.stagedEgressID,
\t}''')
replace("internal/upstream/registry.go",
'''\tfor _, e := range r.staged {
\t\tif !e.reused {
\t\t\te.pool.Close()
\t\t}
\t}''',
'''\tfor _, e := range r.staged {
\t\tif !e.reused {
\t\t\t// Activate has not run on an aborted generation, so ownership of the
\t\t\t// candidate discoverer's HTTP transport is still here rather than in
\t\t\t// the refresher goroutine.
\t\t\tcloseDiscoverer(e.discoverer)
\t\t\te.pool.Close()
\t\t}
\t}''')

# ---- HandlerFactory: candidate egress generation is part of transaction ----
replace("internal/app/factory.go", '\t"sync/atomic"\n', '\t"sync/atomic"\n\t"time"\n')
replace("internal/app/factory.go",
'''\t"jul/internal/config"
\t"jul/internal/handler"''',
'''\t"jul/internal/config"
\t"jul/internal/egress"
\t"jul/internal/handler"''')
replace("internal/app/factory.go",
'''\tRLStore       *middleware.RateLimiterStore
\tEgressDial    func(context.Context, string, string) (net.Conn, error)
\tPoolReg       *upstream.Registry''',
'''\tRLStore       *middleware.RateLimiterStore
\tEgress        *egress.Manager
\tPoolReg       *upstream.Registry''')

# Build path.
replace("internal/app/factory.go",
'''\tupstreams := IndexUpstreams(c.Upstreams)
\tgen := f.GenRes.Begin()
\tdefer gen.Abort()

\thandlers, err := f.buildHandlers(ctx, c, gen, upstreams)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\tvar retirePrev func()
\tif commit {
\t\tretirePrev = gen.Commit()
\t\tf.PoolReg.Activate()
\t}
\treturn handlers, retirePrev, nil''',
'''\tupstreams := IndexUpstreams(c.Upstreams)
\tcandidateEgress, egressChanged, err := f.prepareEgress(c)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\tgen := f.GenRes.Begin()
\tf.stageEgress(candidateEgress)
\tcommitted := false
\tdefer func() {
\t\tgen.Abort()
\t\tif !committed && egressChanged && candidateEgress != nil {
\t\t\tcandidateEgress.CloseIdleConnections()
\t\t}
\t}()

\thandlers, err := f.buildHandlers(ctx, c, gen, upstreams, candidateEgress)
\tif err != nil {
\t\treturn nil, nil, err
\t}
\tvar retirePrev func()
\tif commit {
\t\tretireHandlers := gen.Commit()
\t\tvar retiredEgress *egress.Generation
\t\tif egressChanged && f.Egress != nil {
\t\t\tretiredEgress = f.Egress.Publish(candidateEgress)
\t\t}
\t\tf.PoolReg.Activate()
\t\tcommitted = true
\t\tretirePrev = combineRetirement(retireHandlers, retiredEgress)
\t}
\treturn handlers, retirePrev, nil''')

# Prepare path before GenRes Begin and build call.
replace("internal/app/factory.go",
'''\tupstreams := IndexUpstreams(c.Upstreams)
\tgen := f.GenRes.Begin()

\thandlers, err = f.buildHandlers(ctx, c, gen, upstreams)
\tif err != nil {
\t\tgen.Abort()
\t\tf.mu.Unlock()
\t\treturn nil, 0, nil, nil, err
\t}''',
'''\tupstreams := IndexUpstreams(c.Upstreams)
\tcandidateEgress, egressChanged, prepErr := f.prepareEgress(c)
\tif prepErr != nil {
\t\tf.mu.Unlock()
\t\treturn nil, 0, nil, nil, prepErr
\t}
\tgen := f.GenRes.Begin()
\tf.stageEgress(candidateEgress)

\thandlers, err = f.buildHandlers(ctx, c, gen, upstreams, candidateEgress)
\tif err != nil {
\t\tgen.Abort()
\t\tif egressChanged && candidateEgress != nil {
\t\t\tcandidateEgress.CloseIdleConnections()
\t\t}
\t\tf.mu.Unlock()
\t\treturn nil, 0, nil, nil, err
\t}''')
replace("internal/app/factory.go",
'''\t\tcommitted = true
\t\tret := gen.Commit()
\t\t// Snapshots must be captured AFTER pools commit''',
'''\t\tcommitted = true
\t\tretireHandlers := gen.Commit()
\t\tvar retiredEgress *egress.Generation
\t\tif egressChanged && f.Egress != nil {
\t\t\tretiredEgress = f.Egress.Publish(candidateEgress)
\t\t}
\t\t// Snapshots must be captured AFTER pools commit''')
replace("internal/app/factory.go",
'''\t\tf.PoolReg.Activate()
\t\tf.mu.Unlock()
\t\treturn snapshots, ret
\t}''',
'''\t\tf.PoolReg.Activate()
\t\tf.mu.Unlock()
\t\treturn snapshots, combineRetirement(retireHandlers, retiredEgress)
\t}''')
replace("internal/app/factory.go",
'''\t\tcommitted = true
\t\tgen.Abort()
\t\tf.mu.Unlock()''',
'''\t\tcommitted = true
\t\tgen.Abort()
\t\tif egressChanged && candidateEgress != nil {
\t\t\tcandidateEgress.CloseIdleConnections()
\t\t}
\t\tf.mu.Unlock()''')
replace("internal/app/factory.go",
'''func (f *HandlerFactory) buildHandlers(ctx context.Context, c *config.Config, gen *Generation, upstreams map[string]config.UpstreamConfig) (map[string]http.Handler, error) {''',
'''func (f *HandlerFactory) buildHandlers(ctx context.Context, c *config.Config, gen *Generation, upstreams map[string]config.UpstreamConfig, egressGeneration *egress.Generation) (map[string]http.Handler, error) {''')
replace("internal/app/factory.go",
'''\tpluginSet, err := f.PluginMgr.Build(ctx, c.Plugins)''',
'''\tvar pluginEgressWrap func(plugins.DialFunc) plugins.DialFunc
\tif egressGeneration != nil && egressGeneration.Enabled() {
\t\tpluginEgressWrap = egressGeneration.For(egress.SubsystemPlugin).DialContextWith
\t}
\tpluginSet, err := f.PluginMgr.BuildWithEgress(ctx, c.Plugins, pluginEgressWrap)''')
replace("internal/app/factory.go",
'''\t// Authenticators are built once per reload, keyed by a stable location''',
'''\tvar authDial auth.DialFunc
\tif egressGeneration != nil && egressGeneration.Enabled() {
\t\tbase := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
\t\tauthDial = egressGeneration.For(egress.SubsystemAuth).DialContext(base)
\t}

\t// Authenticators are built once per reload, keyed by a stable location''')
replace("internal/app/factory.go",
'''\t\t\t\tDialContext: f.EgressDial,''',
'''\t\t\t\tDialContext: authDial,''')
replace("internal/app/factory.go",
'''\t\t\tauthByScope[key] = a''',
'''\t\t\tgen.Stage(a)
\t\t\tauthByScope[key] = a''')

# Append helper methods before buildHandlers comment.
replace("internal/app/factory.go",
'''// buildHandlers constructs the per-listen-address handler tree from c, staging''',
'''func (f *HandlerFactory) prepareEgress(c *config.Config) (*egress.Generation, bool, error) {
\tif f.Egress == nil {
\t\treturn nil, false, nil
\t}
\tgeneration, changed, err := f.Egress.Prepare(c.Egress)
\tif err != nil {
\t\treturn nil, false, fmt.Errorf("egress: %w", err)
\t}
\treturn generation, changed, nil
}

func (f *HandlerFactory) stageEgress(generation *egress.Generation) {
\tif f.PoolReg == nil {
\t\treturn
\t}
\tif generation == nil {
\t\tf.PoolReg.SetEgressGeneration(0, nil)
\t\treturn
\t}
\tvar dial upstream.DialFunc
\tif generation.Enabled() {
\t\tbase := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
\t\tdial = generation.For(egress.SubsystemDiscovery).DialContext(base)
\t}
\tf.PoolReg.SetEgressGeneration(generation.ID(), dial)
}

func combineRetirement(retireHandlers func(), retiredEgress *egress.Generation) func() {
\tif retireHandlers == nil && retiredEgress == nil {
\t\treturn nil
\t}
\treturn func() {
\t\tif retireHandlers != nil {
\t\t\tretireHandlers()
\t\t}
\t\tif retiredEgress != nil {
\t\t\tretiredEgress.CloseIdleConnections()
\t\t}
\t}
}

// buildHandlers constructs the per-listen-address handler tree from c, staging''')

# ---- composition root: one process Manager, dynamic PKI clients ------------
old = '''\tegressPolicy, err := egress.New(cfg.Egress, egress.WithObserver(func(d egress.Decision) {
\t\tmetrics.ObserveEgressDecision(d.Subsystem, string(d.Result), string(d.Reason), d.DNSAnswers)
\t\tegressBlockLog(d)
\t}))
\tif err != nil {
\t\tlog.Error("failed to build egress allow-list", "error", err)
\t\treturn 1
\t}
\t// Subsystem-scoped guards attribute blocks and metrics without call sites
\t// importing the egress enforcement internals. All guards share one base
\t// dialer (safe for concurrent use).
\tegressBase := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
\tdiscoveryDial := egressPolicy.For(egress.SubsystemDiscovery).DialContext(egressBase)
\tauthDial := egressPolicy.For(egress.SubsystemAuth).DialContext(egressBase)
\t// PKI clients are guarded only when egress is enabled; when disabled they are
\t// nil so ACME/OCSP keep their default clients (including HTTP(S)_PROXY
\t// support), preserving backward-compatible behavior.
\tvar acmeClient, ocspClient *http.Client
\tif egressPolicy.Enabled() {
\t\tacmeClient = egressPolicy.For(egress.SubsystemACME).Client(0)
\t\tocspClient = egressPolicy.For(egress.SubsystemOCSP).Client(0)
\t}
'''
new = '''\tegressManager, err := egress.NewManager(cfg.Egress, egress.WithObserver(func(d egress.Decision) {
\t\tmetrics.ObserveEgressDecision(d.Subsystem, string(d.Result), string(d.Reason), d.DNSAnswers)
\t\tegressBlockLog(d)
\t}))
\tif err != nil {
\t\tlog.Error("failed to build egress allow-list", "error", err)
\t\treturn 1
\t}
\t// ACME and OCSP owners are intentionally process-lifetime. Their stable
\t// clients select the current immutable egress generation for every HTTP
\t// exchange, so egress policy can hot-reload without rebuilding ACME state.
\t// A disabled generation clones the default transport and therefore preserves
\t// HTTP(S)_PROXY behavior; an enabled generation pins Proxy=nil.
\tacmeClient := egressManager.Client(egress.SubsystemACME, 0)
\tocspClient := egressManager.Client(egress.SubsystemOCSP, 0)
'''
replace("internal/app/serve.go", old, new)
replace("internal/app/serve.go", '\t\tDialContext:         discoveryDial,\n', '')
replace("internal/app/serve.go",
'''\t// The WASM plugin manager persists across reloads so the compilation
\t// cache and KV store survive config edits. Plugin fetches are guarded by
\t// both their local allowed_hosts/SSRF rules and, when egress is enabled, the
\t// server-wide allow-list via EgressWrap.
\tvar pluginEgressWrap func(plugins.DialFunc) plugins.DialFunc
\tif egressPolicy.Enabled() {
\t\tpluginEgressWrap = egressPolicy.For(egress.SubsystemPlugin).DialContextWith
\t}
\tpluginMgr, err := plugins.NewManager(plugins.Options{
\t\tLogger:       log,
\t\tOnInvocation: metrics.ObservePluginInvocation,
\t\tOnPanic:      metrics.ObservePluginPanic,
\t\tEgressWrap:   pluginEgressWrap,
\t})''',
'''\t// The WASM plugin manager persists across reloads so the compilation
\t// cache and KV store survive config edits. The handler factory supplies the
\t// candidate generation's egress wrapper to each plugin Set it builds.
\tpluginMgr, err := plugins.NewManager(plugins.Options{
\t\tLogger:       log,
\t\tOnInvocation: metrics.ObservePluginInvocation,
\t\tOnPanic:      metrics.ObservePluginPanic,
\t})''')
replace("internal/app/serve.go",
'''\t\tRLStore:       rlStore,
\t\tEgressDial:    authDial,
\t\tPoolReg:       poolReg,''',
'''\t\tRLStore:       rlStore,
\t\tEgress:        egressManager,
\t\tPoolReg:       poolReg,''')

print("#94 consumer integration patch applied")
