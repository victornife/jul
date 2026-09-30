// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"jul/internal/config"
	"jul/internal/logthrottle"
)

// wasmPageSize is the WebAssembly linear-memory page size (64 KiB).
const wasmPageSize = 1 << 16

// instantiateTimeout bounds module instantiation (running the guest's
// _initialize), independent of the much shorter per-request call timeout.
const instantiateTimeout = 10 * time.Second

// defaultMaxInstanceInvocations bounds how many guest calls a single pooled
// module instance serves before it is retired. WebAssembly linear memory only
// grows (memory.grow is monotonic) — an instance reused indefinitely can
// accumulate unbounded heap even with zero errors, since nothing else ever
// shrinks it back down. Retiring and re-instantiating periodically bounds the
// per-instance high-water mark; instantiation cost is amortized across this
// many calls.
const defaultMaxInstanceInvocations = 1000

// poolCapacity bounds how many idle module instances a plugin keeps ready for
// reuse. A plain sync.Pool is not safe here: wazero's Runtime keeps its own
// internal reference to every instantiated module (so Close can tear them all
// down at once), so an instance sync.Pool silently drops during GC's victim-
// cache eviction is never garbage collected and never explicitly Closed either
// — it leaks for the life of the process. A fixed-capacity channel means every
// instance is always either reused or explicitly closed, never silently
// dropped.

const poolCapacity = 64

// defaultMaxInstances bounds the live module instances (idle plus in use) of
// one plugin when max_instances is unset (#506). Each may hold up to
// memory_limit of linear memory, so the default caps a plugin at
// 64 × memory_limit (1 GiB with the 16 MiB default).
const defaultMaxInstances = 64

// instanceLimitLogInterval throttles the warning logged when callers are
// turned away at the instance cap; request concurrency is client-chosen.
const instanceLimitLogInterval = 10 * time.Second

// errInstanceLimit reports that every instance max_instances allows was busy
// for the whole bounded wait. Callers answer 503 with Retry-After.
var errInstanceLimit = errors.New("plugin instance limit reached")

// Outcomes of a call that found every allowed instance busy and had to wait.
const (
	instanceWaitAcquired = "acquired"
	instanceWaitRejected = "rejected"
)

// pooledModule pairs a pooled WASM module instance with its lifetime call
// count so acquire/release can retire it once maxInstanceInvocations is hit.
type pooledModule struct {
	mod   api.Module
	calls int
}

// Options configures a Manager.
type Options struct {
	// Logger receives guest log messages and host diagnostics. Required.
	Logger *slog.Logger
	// OnInvocation, when set, is called after each guest invocation with the
	// plugin name, result ("continue"/"stop"/"error"), and wall-clock duration.
	OnInvocation func(plugin, result string, d time.Duration)
	// OnPanic, when set, is called when a guest trap, panic, or timeout is
	// contained by the host.
	OnPanic func(plugin string)
	// OnResponseInvocation, when set, is called after each jul-abi/v2
	// handle_response invocation with the plugin name, result
	// ("continue"/"reject"/"error") and wall-clock duration.
	OnResponseInvocation func(plugin, result string, d time.Duration)
	// OnResponseBodyUnavailable, when set, is called when a body subscription
	// is presented without a body, with the closed reason label.
	OnResponseBodyUnavailable func(plugin, reason string)
	// OnInstanceWait, when set, is called once for each call that found every
	// instance max_instances allows in use, with result "acquired" or
	// "rejected" (#506).
	OnInstanceWait func(plugin, result string)
	// KV overrides the key/value backing store. Defaults to an in-memory store.
	KV KVStore
	// EgressWrap, when set, wraps a plugin fetch dialer with the global egress
	// allow-list so a plugin fetch must satisfy both its own allowed_hosts/SSRF
	// guard and the server-wide [egress] policy. It is nil when egress is
	// disabled, leaving plugin fetches guarded only by their local rules.
	EgressWrap func(base DialFunc) DialFunc
}

// DialFunc matches net.Dialer.DialContext. The plugin fetch client composes an
// SSRF-validating dialer beneath the optional global egress guard.
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// Manager owns the process-wide plugin runtime resources: the shared
// compilation cache (so unchanged modules are recompiled cheaply across
// reloads) and the key/value store. It is created once and closed at shutdown.
type Manager struct {
	log   *slog.Logger
	cache wazero.CompilationCache
	kv    KVStore
	// kvUsage is keyed by plugin name (the KV namespace). It shares the KV
	// store's process lifetime so a reload cannot reset quota usage the store
	// still holds.
	kvUsageMu sync.Mutex
	kvUsage   map[string]*kvLedger
	onInvoke  func(string, string, time.Duration)
	onPanic   func(string)
	onRespInv func(string, string, time.Duration)
	onNoBody  func(string, string)
	onWait    func(string, string)
	// live holds every compiled plugin not yet closed, across generations, so
	// InstanceStats can report live instances at scrape time.
	liveMu sync.Mutex
	live   map[*plugin]struct{}
	// egressWrap composes the global egress guard beneath each plugin's fetch
	// SSRF guard; nil when egress is disabled.
	egressWrap func(base DialFunc) DialFunc
}

// NewManager creates a Manager. It never fails in the compiled build, but
// returns an error type for symmetry with the stub build.
func NewManager(opts Options) (*Manager, error) {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	kv := opts.KV
	if kv == nil {
		kv = newMemKV()
	}
	onInvoke := opts.OnInvocation
	if onInvoke == nil {
		onInvoke = func(string, string, time.Duration) {}
	}
	onPanic := opts.OnPanic
	if onPanic == nil {
		onPanic = func(string) {}
	}
	onRespInv := opts.OnResponseInvocation
	if onRespInv == nil {
		onRespInv = func(string, string, time.Duration) {}
	}
	onNoBody := opts.OnResponseBodyUnavailable
	if onNoBody == nil {
		onNoBody = func(string, string) {}
	}
	onWait := opts.OnInstanceWait
	if onWait == nil {
		onWait = func(string, string) {}
	}
	return &Manager{
		log:        opts.Logger,
		cache:      wazero.NewCompilationCache(),
		kv:         kv,
		kvUsage:    make(map[string]*kvLedger),
		onInvoke:   onInvoke,
		onPanic:    onPanic,
		onRespInv:  onRespInv,
		onNoBody:   onNoBody,
		onWait:     onWait,
		live:       make(map[*plugin]struct{}),
		egressWrap: opts.EgressWrap,
	}, nil
}

// InstanceStats is one plugin's live module-instance count, summed over every
// generation still holding the plugin (a draining one included).
type InstanceStats struct {
	Plugin string
	Live   int
}

// InstanceStats reports live instances per plugin name, sorted by name. It is
// read at scrape time.
func (m *Manager) InstanceStats() []InstanceStats {
	if m == nil {
		return nil
	}
	m.liveMu.Lock()
	by := make(map[string]int)
	for p := range m.live {
		by[p.name] += len(p.slots)
	}
	m.liveMu.Unlock()
	out := make([]InstanceStats, 0, len(by))
	for name, n := range by {
		out = append(out, InstanceStats{Plugin: name, Live: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out
}

func (m *Manager) track(p *plugin) {
	m.liveMu.Lock()
	m.live[p] = struct{}{}
	m.liveMu.Unlock()
}

func (m *Manager) forget(p *plugin) {
	m.liveMu.Lock()
	delete(m.live, p)
	m.liveMu.Unlock()
}

// kvLedger is the quota accounting for one plugin KV namespace.
type kvLedger struct {
	mu    sync.Mutex
	keys  map[string]int
	bytes int
}

func newKVLedger() *kvLedger { return &kvLedger{keys: make(map[string]int)} }

// kvLedgerFor returns the process-lifetime ledger for a plugin namespace,
// shared by every generation that declares the plugin.
func (m *Manager) kvLedgerFor(name string) *kvLedger {
	m.kvUsageMu.Lock()
	defer m.kvUsageMu.Unlock()
	l := m.kvUsage[name]
	if l == nil {
		l = newKVLedger()
		m.kvUsage[name] = l
	}
	return l
}

// Close releases the shared compilation cache.
func (m *Manager) Close() error {
	if m == nil || m.cache == nil {
		return nil
	}
	return m.cache.Close(context.Background())
}

// Build compiles and instantiates every declared plugin into a Set for one
// configuration generation. On any error the partially built Set is closed and
// the error returned, so a rejected reload leaks no runtimes. ctx bounds the
// build and is checked between plugins so a cancelled reload stops promptly.
func (m *Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
	return m.BuildWithEgress(ctx, cfg, m.egressWrap)
}

// BuildWithEgress is Build with an explicit generation-scoped global egress
// wrapper. HandlerFactory uses it so every newly published plugin Set captures
// the candidate egress generation while the process-lifetime Manager keeps its
// compilation cache and KV store. A nil wrapper preserves plugin-local SSRF and
// allowed_hosts enforcement without a global egress policy.
func (m *Manager) BuildWithEgress(ctx context.Context, cfg map[string]config.PluginConfig, egressWrap func(base DialFunc) DialFunc) (*Set, error) {
	s := &Set{plugins: make(map[string]*plugin, len(cfg))}
	ok := false
	defer func() {
		if !ok {
			_ = s.Close()
		}
	}()
	for name, pc := range cfg {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		p, err := m.compilePlugin(ctx, name, pc, egressWrap)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		s.plugins[name] = p
	}
	ok = true
	return s, nil
}

// plugin is a compiled, ready-to-run plugin. Instances are pooled because
// instantiating a Go/wasip1 module (which boots the Go runtime) is expensive
// relative to a single call.
type plugin struct {
	name string
	// identity is the content identity of the exact bytes compiled below.
	identity ModuleIdentity
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	pool     chan *pooledModule // fixed-capacity; see poolCapacity
	// slots holds one token per live instance (idle or in use); its capacity
	// is max_instances (#506).
	slots     chan struct{}
	timeout   time.Duration
	isHandler bool
	// abi is the configured (and negotiated) ABI; hasResponse reports that a
	// jul-abi/v2 module exports handle_response.
	abi         string
	hasResponse bool

	capKV        bool
	capFetch     bool
	allowedHosts []string
	configJSON   []byte
	kv           KVStore

	maxReqBody   int
	maxRespBody  int
	fetchTimeout time.Duration
	maxFetchResp int
	kvMaxEntries int
	kvMaxBytes   int

	// resolver lets tests substitute DNS resolution to exercise the fetch SSRF
	// guard; nil uses net.DefaultResolver.
	resolver ipResolver

	// egressWrap composes the global egress allow-list beneath the SSRF guard for
	// outbound fetches; nil when egress is disabled.
	egressWrap func(base DialFunc) DialFunc

	// client is the reusable HTTP client for guarded outbound fetches.
	// Created once per plugin to enable connection pooling.
	client *http.Client

	// kvUsage tracks each key's stored size in this plugin's namespace so
	// kv_set can reject an entry or total over quota. It is Manager-owned, not
	// generation-owned: the store it accounts for survives reloads.
	kvUsage *kvLedger

	// maxInstanceInvocations bounds how many calls a pooled instance serves
	// before release() retires it instead of returning it to the pool.
	maxInstanceInvocations int

	log       *slog.Logger
	onInvoke  func(string, string, time.Duration)
	onPanic   func(string)
	onRespInv func(string, string, time.Duration)
	onNoBody  func(string, string)
	onWait    func(string, string)
	// limitLog throttles the instance-cap warning.
	limitLog logthrottle.Limiter
	// mgr tracks this plugin for InstanceStats until close.
	mgr *Manager
}

// afterModuleRead is a test seam between the single module read and
// compilation; production leaves it nil.
var afterModuleRead func(name string)

func (m *Manager) compilePlugin(ctx context.Context, name string, pc config.PluginConfig, egressWrap func(base DialFunc) DialFunc) (*plugin, error) {
	module, err := ReadModule(pc)
	if err != nil {
		return nil, err
	}
	if afterModuleRead != nil {
		afterModuleRead(name)
	}
	wasm := module.Bytes

	pages := uint32(pc.MemoryLimit.Bytes() / wasmPageSize)
	if pages == 0 {
		pages = 256 // 16 MiB
	}

	cfgJSON := []byte("{}")
	if len(pc.Config) > 0 {
		if b, err := json.Marshal(pc.Config); err == nil {
			cfgJSON = b
		}
	}

	p := &plugin{
		name:         name,
		identity:     module.Identity,
		timeout:      pc.Timeout.Std(),
		isHandler:    pc.Type == "handler",
		abi:          config.EffectivePluginABI(pc),
		pool:         make(chan *pooledModule, poolCapacity),
		capKV:        pc.KV,
		capFetch:     pc.Fetch,
		allowedHosts: pc.AllowedHosts,
		configJSON:   cfgJSON,
		kv:           m.kv,
		maxReqBody:   sizeOr(pc.MaxRequestBody, maxRequestBodyBuffer),
		maxRespBody:  sizeOr(pc.MaxResponseBody, maxResponseBodyBuffer),
		fetchTimeout: pc.FetchTimeout.Std(),
		maxFetchResp: sizeOr(pc.MaxFetchResponse, 1<<20),
		kvMaxEntries: pc.KVMaxEntries,
		kvMaxBytes:   sizeOr(pc.KVMaxBytes, 1<<20),
		kvUsage:      m.kvLedgerFor(name),
		log:          m.log,
		onInvoke:     m.onInvoke,
		onPanic:      m.onPanic,
		onRespInv:    m.onRespInv,
		onNoBody:     m.onNoBody,
		onWait:       m.onWait,
		egressWrap:   egressWrap,
	}
	maxInstances := pc.MaxInstances
	if maxInstances <= 0 {
		maxInstances = defaultMaxInstances
	}
	p.slots = make(chan struct{}, maxInstances)
	if p.fetchTimeout <= 0 {
		p.fetchTimeout = 5 * time.Second
	}
	if p.kvMaxEntries <= 0 {
		p.kvMaxEntries = 1024
	}
	if p.timeout <= 0 {
		p.timeout = 100 * time.Millisecond
	}
	p.maxInstanceInvocations = pc.MaxInvocations
	if p.maxInstanceInvocations <= 0 {
		p.maxInstanceInvocations = defaultMaxInstanceInvocations
	}

	dialer := &net.Dialer{Timeout: p.fetchTimeout}
	resolver := p.resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	p.client = &http.Client{
		Timeout:   p.fetchTimeout,
		Transport: &http.Transport{DialContext: p.fetchDial(dialer, resolver)},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if !hostAllowed(p.allowedHosts, req.URL.Hostname()) {
				return errFetchBlocked
			}
			return nil
		},
	}

	// Use the caller-supplied context so the reload deadline bounds WASM
	// compilation. WithCloseOnContextDone ensures the runtime is torn down
	// if the reload is cancelled before compilation finishes (M-01). The
	// engine is chosen explicitly (EngineMode) so it can be reported.
	rtCfg := newRuntimeConfig().
		WithCompilationCache(m.cache).
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true)
	r := wazero.NewRuntimeWithConfig(ctx, rtCfg)

	closeOnErr := func(err error) (*plugin, error) {
		_ = r.Close(ctx)
		return nil, err
	}

	if _, err := wasi_snapshot_preview1.Instantiate(ctx, r); err != nil {
		return closeOnErr(fmt.Errorf("instantiate wasi: %w", err))
	}

	registrar, ok := abiRegistry[p.abi]
	if !ok {
		return closeOnErr(fmt.Errorf("unknown ABI %q", p.abi))
	}
	if err := registrar(ctx, r, p); err != nil {
		return closeOnErr(fmt.Errorf("register host module: %w", err))
	}

	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		return closeOnErr(fmt.Errorf("compile module: %w", err))
	}
	decl, err := negotiateABI(p.abi, compiled)
	if err != nil {
		_ = compiled.Close(ctx)
		return closeOnErr(err)
	}
	p.hasResponse = decl.hasResponse

	p.runtime = r
	p.compiled = compiled

	// Eagerly instantiate one instance so a broken module fails the build (and
	// thus the reload) rather than the first request.
	mod, err := p.instantiate(ctx)
	if err != nil {
		return closeOnErr(fmt.Errorf("instantiate module: %w", err))
	}
	p.slots <- struct{}{}
	p.pool <- &pooledModule{mod: mod}

	p.mgr = m
	m.track(p)
	return p, nil
}

// sizeOr returns the byte count of s, or def when s is non-positive.
func sizeOr(s config.Size, def int) int {
	if n := s.Bytes(); n > 0 {
		return int(n)
	}
	return def
}

// instantiate creates a fresh module instance, running its _initialize reactor
// start function. Instantiation uses its own timeout, not the per-call one.
// ctx bounds the instantiation so a cancelled reload stops promptly (M-04).
func (p *plugin) instantiate(ctx context.Context) (api.Module, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithStartFunctions("_initialize")
	return p.runtime.InstantiateModule(ctx, p.compiled, cfg)
}

// acquire returns an idle instance, or instantiates one while fewer than
// max_instances are live. At the cap it waits up to one call timeout (or until
// ctx ends) for an instance to come back or be retired, then fails with
// errInstanceLimit (#506).
func (p *plugin) acquire(ctx context.Context) (*pooledModule, error) {
	select {
	case pm := <-p.pool:
		return pm, nil
	default:
	}
	select {
	case p.slots <- struct{}{}:
		return p.newInstance()
	default:
	}
	wait := time.NewTimer(p.timeout)
	defer wait.Stop()
	select {
	case pm := <-p.pool:
		p.onWait(p.name, instanceWaitAcquired)
		return pm, nil
	case p.slots <- struct{}{}:
		p.onWait(p.name, instanceWaitAcquired)
		return p.newInstance()
	case <-ctx.Done():
	case <-wait.C:
	}
	p.onWait(p.name, instanceWaitRejected)
	if p.limitLog.Allow(instanceLimitLogInterval) {
		p.log.Warn("plugin instance limit reached; request rejected", "plugin", p.name, "max_instances", cap(p.slots))
	}
	return nil, errInstanceLimit
}

// newInstance instantiates a module for a slot the caller already holds,
// returning the slot if instantiation fails.
func (p *plugin) newInstance() (*pooledModule, error) {
	ctx, cancel := context.WithTimeout(context.Background(), instantiateTimeout)
	defer cancel()
	mod, err := p.instantiate(ctx)
	if err != nil {
		p.freeSlot()
		return nil, err
	}
	return &pooledModule{mod: mod}, nil
}

// discard closes an instance and frees its slot. Every instance ends here or
// in the runtime's Close.
func (p *plugin) discard(mod api.Module) {
	_ = mod.Close(context.Background())
	p.freeSlot()
}

// freeSlot never blocks, so a slot can never be leaked into a deadlock.
func (p *plugin) freeSlot() {
	select {
	case <-p.slots:
	default:
	}
}

// release returns pm to the pool for reuse, unless it has served its lifetime
// call budget or the pool is already at capacity, in which case it is closed
// instead — every instance is always either reused or explicitly closed, never
// silently dropped (see poolCapacity).
func (p *plugin) release(pm *pooledModule) {
	pm.calls++
	if pm.calls < p.maxInstanceInvocations {
		select {
		case p.pool <- pm:
			return
		default:
		}
	}
	p.discard(pm.mod)
}

// kvSet stores a value under an already-namespaced key, enforcing the plugin's
// per-namespace quota: it rejects (returns false) a value that would push the
// total byte size or the distinct-key count over the configured caps.
func (p *plugin) kvSet(key string, val []byte) bool {
	u := p.kvUsage
	u.mu.Lock()
	defer u.mu.Unlock()
	prev, exists := u.keys[key]
	newTotal := u.bytes - prev + len(val)
	if newTotal > p.kvMaxBytes {
		return false
	}
	if !exists && len(u.keys) >= p.kvMaxEntries {
		return false
	}
	p.kv.Set(key, val)
	u.keys[key] = len(val)
	u.bytes = newTotal
	return true
}

// callOutcome classifies how a guest call ended.
type callOutcome uint8

const (
	callOK callOutcome = iota
	// callAcquireFailed: no instance could be acquired or instantiated.
	callAcquireFailed
	// callTrapped: the guest trapped, panicked or timed out.
	callTrapped
	// callHostError: a host function failed the invocation, or the guest
	// returned a value its ABI reserves.
	callHostError
)

// call runs one guest export on a pooled instance. On any outcome but callOK
// the instance is discarded, not pooled, because a failed module may be in an
// undefined state. valid, when non-nil, rejects reserved result values.
func (p *plugin) call(parent context.Context, export string, inv *invocation, valid func(uint32) bool) (res uint32, dur time.Duration, outcome callOutcome, err error) {
	pm, err := p.acquire(parent)
	if err != nil {
		return 0, 0, callAcquireFailed, err
	}
	mod := pm.mod

	ctx, cancel := context.WithTimeout(withInvocation(parent, inv), p.timeout)
	defer cancel()

	start := time.Now()
	fn := mod.ExportedFunction(export)

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("plugin %q panicked: %v", p.name, rec)
			p.discard(mod)
			res, dur, outcome = 0, time.Since(start), callTrapped
		}
	}()

	results, callErr := fn.Call(ctx)
	dur = time.Since(start)
	if callErr != nil {
		p.discard(mod)
		return 0, dur, callTrapped, callErr
	}

	// A host function may have rejected the request (oversize body, response
	// overflow); treat that as a contained failure so the caller returns 500
	// instead of serving a truncated request/response.
	if inv.err != nil {
		p.discard(mod)
		return 0, dur, callHostError, inv.err
	}
	res = uint32(results[0])
	if valid != nil && !valid(res) {
		p.discard(mod)
		return 0, dur, callHostError, errBadResult
	}

	p.release(pm)
	return res, dur, callOK, nil
}

// invoke runs the guest's handle_request for one HTTP request. It returns the
// guest's action (Continue/Stop), the invocation holding any response the guest
// produced, and an error if the guest trapped, panicked, or timed out (which the
// caller turns into a 500). On error the instance is discarded, not pooled,
// because a trapped module may be in an undefined state.
func (p *plugin) invoke(parent context.Context, w http.ResponseWriter, r *http.Request) (action uint32, inv *invocation, err error) {
	inv = &invocation{r: r, w: w, log: p.log, maxReqBody: p.maxReqBody, maxRespBody: p.maxRespBody, sub: -1}
	var valid func(uint32) bool
	if p.abi == ABIJulV2 {
		valid = func(v uint32) bool { return v == actionStop || v == actionContinue }
	}
	action, dur, outcome, err := p.call(parent, exportHandleRequest, inv, valid)
	switch outcome {
	case callAcquireFailed:
		if !errors.Is(err, errInstanceLimit) {
			p.onPanic(p.name)
		}
		return 0, nil, err
	case callTrapped:
		p.onPanic(p.name)
		p.onInvoke(p.name, "error", dur)
		return 0, inv, err
	case callHostError:
		p.onInvoke(p.name, "error", dur)
		return 0, inv, err
	}
	result := "stop"
	if action == 1 {
		result = "continue"
	}
	p.onInvoke(p.name, result, dur)
	return action, inv, nil
}

// invokeResponse runs a jul-abi/v2 guest's handle_response on view. A
// committed view (a protocol switch) only accepts CONTINUE and runs detached
// from the request's cancellation, since the request is already over.
func (p *plugin) invokeResponse(parent context.Context, r *http.Request, view *responseView, state []byte) (uint32, error) {
	inv := &invocation{r: r, log: p.log, maxReqBody: p.maxReqBody, maxRespBody: p.maxRespBody,
		phase: phaseResponse, sub: -1, state: state, resp: view}
	valid := func(v uint32) bool { return v == responseContinue || (v == responseReject && !view.committed) }
	if view.committed {
		parent = context.WithoutCancel(parent)
	}
	res, dur, outcome, err := p.call(parent, exportHandleResponse, inv, valid)
	switch outcome {
	case callAcquireFailed:
		if !errors.Is(err, errInstanceLimit) {
			p.onPanic(p.name)
		}
		p.onRespInv(p.name, "error", 0)
		return 0, err
	case callTrapped:
		p.onPanic(p.name)
		p.onRespInv(p.name, "error", dur)
		return 0, err
	case callHostError:
		p.onRespInv(p.name, "error", dur)
		return 0, err
	}
	result := "continue"
	if res == responseReject {
		result = "reject"
	}
	p.onRespInv(p.name, result, dur)
	return res, nil
}

// close tears down the plugin's runtime, which closes every instance (pooled or
// in flight) and the compiled module.
func (p *plugin) close() {
	if p == nil {
		return
	}
	if p.mgr != nil {
		p.mgr.forget(p)
	}
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
	if p.runtime == nil {
		return
	}
	_ = p.runtime.Close(context.Background())
}
