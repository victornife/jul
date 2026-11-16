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
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/logthrottle"
	"jul/internal/reqdeadline"
)

// wasmPageSize is the WebAssembly linear-memory page size (64 KiB).
const wasmPageSize = config.PluginMemoryPageSize

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

var (
	errGuestWorkInFlight        = errors.New("plugin guest work is still in flight")
	errGuestTeardownUnconfirmed = errors.New("plugin guest teardown could not be confirmed")
)

// Outcomes of a call that found every allowed instance busy and had to wait.
const (
	instanceWaitAcquired = "acquired"
	instanceWaitRejected = "rejected"
)

// pooledModule pairs a pooled WASM module instance with its lifetime call
// count so acquire/release can retire it once maxInstanceInvocations is hit.
type pooledModule struct {
	mod    api.Module
	ticket *inspection.GuestTicket
	calls  int
}

type guestOwnership struct {
	active bool
	failed error
}

type guestPotential struct {
	maxBytes     uint64
	maxInstances uint64
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
	// OnRequestBodyFailure, when set, is called when preparing a request body
	// for a body-reading plugin fails before any guest invocation, with reason
	// "too_large", "read_error", "canceled" or "retired" (#699).
	OnRequestBodyFailure func(plugin, reason string)
	// KV overrides the key/value backing store. Defaults to an in-memory store.
	KV KVStore
	// EgressWrap, when set, wraps a plugin fetch dialer with the global egress
	// allow-list so a plugin fetch must satisfy both its own allowed_hosts/SSRF
	// guard and the server-wide [egress] policy. It is nil when egress is
	// disabled, leaving plugin fetches guarded only by their local rules.
	EgressWrap func(base DialFunc) DialFunc
	// Inspection is the process-owned aggregate inspection governor (ADR
	// 0032). Every plugin request takes or reuses its request lease and
	// charges host-held request allocations to it. Required unless
	// MetadataOnly is set: a missing governor is a wiring error, never a
	// silent bypass.
	Inspection *inspection.Governor
	// MetadataOnly builds a Manager that only compiles plugins and reads their
	// static facts (the offline `jul check` path). It never instantiates
	// guests or serves requests, so it needs no governor.
	MetadataOnly bool
}

// errNoGovernor reports a Manager built for requests without the process
// inspection governor.
var errNoGovernor = errors.New("plugins: inspection governor is required")

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
	onBodyErr func(string, string)
	// live holds every compiled plugin not yet closed, across generations, so
	// InstanceStats can report live instances at scrape time.
	liveMu     sync.Mutex
	live       map[*plugin]struct{}
	potential  map[*guestPotential]struct{}
	retainedMu sync.Mutex
	retained   map[*plugin]struct{}
	// egressWrap composes the global egress guard beneath each plugin's fetch
	// SSRF guard; nil when egress is disabled.
	egressWrap func(base DialFunc) DialFunc
	// gov is the process inspection governor; nil only for MetadataOnly.
	gov          *inspection.Governor
	metadataOnly bool
	setID        atomic.Uint64
}

// NewManager creates a Manager. It fails when a request-serving Manager has no
// inspection governor.
func NewManager(opts Options) (*Manager, error) {
	if opts.Inspection == nil && !opts.MetadataOnly {
		return nil, errNoGovernor
	}
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
	onBodyErr := opts.OnRequestBodyFailure
	if onBodyErr == nil {
		onBodyErr = func(string, string) {}
	}
	return &Manager{
		log:          opts.Logger,
		cache:        wazero.NewCompilationCache(),
		kv:           kv,
		kvUsage:      make(map[string]*kvLedger),
		onInvoke:     onInvoke,
		onPanic:      onPanic,
		onRespInv:    onRespInv,
		onNoBody:     onNoBody,
		onWait:       onWait,
		onBodyErr:    onBodyErr,
		live:         make(map[*plugin]struct{}),
		potential:    make(map[*guestPotential]struct{}),
		retained:     make(map[*plugin]struct{}),
		egressWrap:   opts.EgressWrap,
		gov:          opts.Inspection,
		metadataOnly: opts.MetadataOnly,
	}, nil
}

// InstanceStats is one plugin's live module-instance count, summed over every
// generation still holding the plugin (a draining one included).
type InstanceStats struct {
	Plugin string
	Live   int
}

// GuestCapacityStats separates configured guest memory capacity from current
// instance counts. Wazero does not expose a process-wide actual-allocation
// measurement through this manager.
type GuestCapacityStats struct {
	MaxPermittedBytes uint64
	LiveInstances     uint64
	MaxInstances      uint64
}

// InstanceStats reports live instances per plugin name, sorted by name. It is
// read at scrape time.
func (m *Manager) InstanceStats() []InstanceStats {
	if m == nil {
		return nil
	}
	plugins := m.pluginsSnapshot()
	by := make(map[string]int)
	for _, p := range plugins {
		by[p.name] += len(p.slots)
	}
	out := make([]InstanceStats, 0, len(by))
	for name, n := range by {
		out = append(out, InstanceStats{Plugin: name, Live: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Plugin < out[j].Plugin })
	return out
}

// GuestCapacityStats reports configured maximum guest memory capacity and
// instance counts across candidates being prepared and compiled generations
// not yet closed.
func (m *Manager) GuestCapacityStats() (GuestCapacityStats, error) {
	var out GuestCapacityStats
	if m == nil {
		return out, nil
	}
	plugins, pending := m.guestCapacitySnapshot()
	for _, potential := range pending {
		if out.MaxPermittedBytes > ^uint64(0)-potential.maxBytes ||
			out.MaxInstances > ^uint64(0)-potential.maxInstances {
			return GuestCapacityStats{}, fmt.Errorf("aggregate plugin guest capacity overflows uint64")
		}
		out.MaxPermittedBytes += potential.maxBytes
		out.MaxInstances += potential.maxInstances
	}
	for _, p := range plugins {
		maxInstances := uint64(cap(p.slots))
		if p.memoryLimitBytes != 0 && maxInstances > ^uint64(0)/p.memoryLimitBytes {
			return GuestCapacityStats{}, fmt.Errorf("plugin %q guest capacity overflows uint64", p.name)
		}
		maxBytes := maxInstances * p.memoryLimitBytes
		if out.MaxPermittedBytes > ^uint64(0)-maxBytes ||
			out.LiveInstances > ^uint64(0)-uint64(len(p.slots)) ||
			out.MaxInstances > ^uint64(0)-maxInstances {
			return GuestCapacityStats{}, fmt.Errorf("aggregate plugin guest capacity overflows uint64")
		}
		out.MaxPermittedBytes += maxBytes
		out.LiveInstances += uint64(len(p.slots))
		out.MaxInstances += maxInstances
	}
	return out, nil
}

func (m *Manager) guestCapacitySnapshot() ([]*plugin, []guestPotential) {
	m.liveMu.Lock()
	defer m.liveMu.Unlock()
	m.retainedMu.Lock()
	defer m.retainedMu.Unlock()

	seen := make(map[*plugin]struct{}, len(m.live)+len(m.retained))
	plugins := make([]*plugin, 0, len(m.live)+len(m.retained))
	for p := range m.live {
		seen[p] = struct{}{}
		plugins = append(plugins, p)
	}
	for p := range m.retained {
		if _, ok := seen[p]; !ok {
			plugins = append(plugins, p)
		}
	}
	pending := make([]guestPotential, 0, len(m.potential))
	for potential := range m.potential {
		pending = append(pending, *potential)
	}
	return plugins, pending
}

func (m *Manager) pluginsSnapshot() []*plugin {
	seen := make(map[*plugin]struct{})
	m.liveMu.Lock()
	m.retainedMu.Lock()
	plugins := make([]*plugin, 0, len(m.live))
	for p := range m.live {
		seen[p] = struct{}{}
		plugins = append(plugins, p)
	}
	for p := range m.retained {
		if _, ok := seen[p]; !ok {
			plugins = append(plugins, p)
		}
	}
	m.retainedMu.Unlock()
	m.liveMu.Unlock()
	return plugins
}

func (m *Manager) track(p *plugin) {
	m.liveMu.Lock()
	delete(m.potential, p.potential)
	m.live[p] = struct{}{}
	m.liveMu.Unlock()
}

func (m *Manager) trackPotential(p *guestPotential) {
	m.liveMu.Lock()
	m.potential[p] = struct{}{}
	m.liveMu.Unlock()
}

func (m *Manager) forget(p *plugin) {
	m.liveMu.Lock()
	delete(m.potential, p.potential)
	delete(m.live, p)
	m.liveMu.Unlock()
}

func (m *Manager) retain(p *plugin) {
	m.retainedMu.Lock()
	m.retained[p] = struct{}{}
	m.retainedMu.Unlock()
}

func (m *Manager) forgetRetained(p *plugin) {
	m.retainedMu.Lock()
	delete(m.retained, p)
	m.retainedMu.Unlock()
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

// Close tears down retained plugins before releasing the shared compilation
// cache. A failed runtime close cannot be retried safely by wazero.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	m.retainedMu.Lock()
	retained := make([]*plugin, 0, len(m.retained))
	for p := range m.retained {
		retained = append(retained, p)
	}
	m.retainedMu.Unlock()
	var errs []error
	for _, p := range retained {
		if err := p.closeRuntime(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 && len(m.pluginsSnapshot()) != 0 {
		errs = append(errs, errors.New("plugin manager still owns live runtimes"))
	}
	if len(errs) == 0 && m.cache != nil {
		if err := m.cache.Close(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
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
	generation := m.setID.Add(1)
	return m.buildWithEgress(ctx, cfg, egressWrap, generation)
}

func (m *Manager) buildWithEgress(ctx context.Context, cfg map[string]config.PluginConfig, egressWrap func(base DialFunc) DialFunc, generation uint64) (_ *Set, err error) {
	if m.gov == nil {
		return nil, errNoGovernor
	}
	s := &Set{plugins: make(map[string]*plugin, len(cfg))}
	ok := false
	defer func() {
		if !ok {
			if closeErr := s.Close(); closeErr != nil {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	for name, pc := range cfg {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		p, err := m.compilePlugin(ctx, name, pc, egressWrap, generation)
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
	slots            chan struct{}
	generation       uint64
	guestMu          sync.Mutex
	guestTickets     map[*inspection.GuestTicket]*guestOwnership
	guestAcquires    int           // includes pool receivers before their ticket becomes busy
	retired          chan struct{} // wakes instance waiters when the generation closes
	runtimeCloseMu   sync.Mutex
	runtimeClosed    bool
	runtimeCloseErr  error
	memoryLimitBytes uint64
	timeout          time.Duration
	isHandler        bool
	// abi is the configured (and negotiated) ABI; hasResponse reports that a
	// jul-abi/v2 module exports handle_response.
	abi         string
	hasResponse bool
	// readsBody reports that the module imports read_request_body, so each
	// request's body is prepared before an instance is acquired (#699).
	readsBody bool
	// facts sizes this plugin's worst-case inspection charge (ADR 0032 §7).
	facts inspection.PluginFacts

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
	// onBodyFail counts request-body preparations that failed before any
	// guest invocation (#699).
	onBodyFail func(string, string)
	// limitLog throttles the instance-cap warning.
	limitLog logthrottle.Limiter
	// trapLog throttles the contained-trap warning.
	trapLog logthrottle.Limiter
	// mgr tracks this plugin for InstanceStats until close.
	mgr       *Manager
	potential *guestPotential
	// gov is the process inspection governor every request is admitted to.
	gov *inspection.Governor
	// closed is set when the plugin's generation is torn down; preparing
	// holds the requests reading their body for it, so close can abort them.
	closed    atomic.Bool
	prepMu    sync.Mutex
	preparing map[*reqdeadline.Owner]int
}

// afterModuleRead is a test seam between the single module read and
// compilation; production leaves it nil.
var afterModuleRead func(name string)

func (m *Manager) compilePlugin(ctx context.Context, name string, pc config.PluginConfig, egressWrap func(base DialFunc) DialFunc, generation uint64) (*plugin, error) {
	module, err := ReadModule(pc)
	if err != nil {
		return nil, err
	}
	wasm := module.Bytes

	cfgJSON := []byte("{}")
	if len(pc.Config) > 0 {
		if b, err := json.Marshal(pc.Config); err == nil {
			cfgJSON = b
		}
	}

	p, pages, err := newPluginLimits(name, pc)
	if err != nil {
		return nil, err
	}
	p.identity = module.Identity
	p.generation = generation
	p.guestTickets = make(map[*inspection.GuestTicket]*guestOwnership)
	p.pool = make(chan *pooledModule, poolCapacity)
	p.capKV = pc.KV
	p.allowedHosts = pc.AllowedHosts
	p.configJSON = cfgJSON
	p.kv = m.kv
	p.fetchTimeout = pc.FetchTimeout.Std()
	p.kvMaxEntries = pc.KVMaxEntries
	p.kvMaxBytes = sizeOr(pc.KVMaxBytes, 1<<20)
	p.kvUsage = m.kvLedgerFor(name)
	p.log = m.log
	p.onInvoke = m.onInvoke
	p.onPanic = m.onPanic
	p.onRespInv = m.onRespInv
	p.onNoBody = m.onNoBody
	p.onWait = m.onWait
	p.onBodyFail = m.onBodyErr
	p.egressWrap = egressWrap
	maxInstances := pc.MaxInstances
	if maxInstances <= 0 {
		maxInstances = defaultMaxInstances
	}
	p.slots = make(chan struct{}, maxInstances)
	p.gov = m.gov
	p.mgr = m
	p.potential = &guestPotential{
		maxBytes:     uint64(maxInstances) * p.memoryLimitBytes,
		maxInstances: uint64(maxInstances),
	}
	m.trackPotential(p.potential)
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
	p.runtime = r

	closeOnErr := func(err error) (*plugin, error) {
		return nil, errors.Join(err, p.close())
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

	if afterModuleRead != nil {
		afterModuleRead(name)
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
	p.readsBody = decl.readsBody
	p.facts = p.inspectionFacts(decl)

	p.compiled = compiled
	m.track(p)

	// Eagerly instantiate one instance so a broken module fails the build (and
	// thus the reload) rather than the first request.
	p.slots <- struct{}{}
	pm, err, _ := p.instantiateGuest(ctx, inspection.GuestIdle)
	if err != nil {
		return closeOnErr(fmt.Errorf("instantiate module: %w", err))
	}
	p.pool <- pm

	return p, nil
}

// newPluginLimits returns a plugin carrying only the configuration-derived
// fields that size its inspection charge, and the guest memory limit in
// pages. compilePlugin and InspectionFacts share it so both report the same
// limits and reject invalid values.
func newPluginLimits(name string, pc config.PluginConfig) (*plugin, uint32, error) {
	pages, err := config.EffectivePluginMemoryPages(pc.MemoryLimit)
	if err != nil {
		return nil, 0, err
	}
	return &plugin{
		name:             name,
		memoryLimitBytes: uint64(pages) * wasmPageSize,
		timeout:          pc.Timeout.Std(),
		isHandler:        pc.Type == "handler",
		abi:              config.EffectivePluginABI(pc),
		capFetch:         pc.Fetch,
		maxReqBody:       sizeOr(pc.MaxRequestBody, maxRequestBodyBuffer),
		maxRespBody:      sizeOr(pc.MaxResponseBody, maxResponseBodyBuffer),
		maxFetchResp:     sizeOr(pc.MaxFetchResponse, 1<<20),
	}, pages, nil
}

// InspectionFacts returns every declared plugin's inspection facts without
// running guest code: each module is read, compiled and ABI-negotiated
// exactly as Build does, but never instantiated, so neither a WebAssembly
// start function nor _initialize executes. The offline `jul check` budget
// uses it; Build remains the only path that instantiates guests.
func (m *Manager) InspectionFacts(ctx context.Context, cfg map[string]config.PluginConfig) (map[string]inspection.PluginFacts, error) {
	out := make(map[string]inspection.PluginFacts, len(cfg))
	for name, pc := range cfg {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		facts, err := m.inspectPlugin(ctx, name, pc)
		if err != nil {
			return nil, fmt.Errorf("plugin %q: %w", name, err)
		}
		out[name] = facts
	}
	return out, nil
}

func (m *Manager) inspectPlugin(ctx context.Context, name string, pc config.PluginConfig) (inspection.PluginFacts, error) {
	module, err := ReadModule(pc)
	if err != nil {
		return inspection.PluginFacts{}, err
	}
	p, pages, err := newPluginLimits(name, pc)
	if err != nil {
		return inspection.PluginFacts{}, err
	}
	if _, ok := abiRegistry[p.abi]; !ok {
		return inspection.PluginFacts{}, fmt.Errorf("unknown ABI %q", p.abi)
	}
	r := wazero.NewRuntimeWithConfig(ctx, newRuntimeConfig().
		WithCompilationCache(m.cache).
		WithMemoryLimitPages(pages).
		WithCloseOnContextDone(true))
	defer func() { _ = r.Close(context.Background()) }()
	// Compilation decodes and validates the module; imports are resolved and
	// start functions run only at instantiation, which this path never does.
	compiled, err := r.CompileModule(ctx, module.Bytes)
	if err != nil {
		return inspection.PluginFacts{}, fmt.Errorf("compile module: %w", err)
	}
	decl, err := negotiateABI(p.abi, compiled)
	if err != nil {
		return inspection.PluginFacts{}, err
	}
	return p.inspectionFacts(decl), nil
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
	// Startup must not inherit wazero's experimental allocators, import
	// resolvers or sockets from a request context; those can retain a module
	// after an unsuccessful instantiation.
	var guestCtx context.Context
	var cancel context.CancelFunc
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		guestCtx, cancel = context.WithDeadline(context.Background(), deadline)
	} else {
		guestCtx, cancel = context.WithCancel(context.Background())
	}
	stop := context.AfterFunc(ctx, func() {
		if !hasDeadline || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			cancel()
		}
	})
	defer func() {
		stop()
		cancel()
	}()
	cfg := wazero.NewModuleConfig().
		WithName("").
		WithSysWalltime().
		WithStartFunctions("_initialize")
	return p.runtime.InstantiateModule(guestCtx, p.compiled, cfg)
}

// acquire returns an idle instance, or instantiates one while fewer than
// max_instances are live. At the cap it waits up to one call timeout (or until
// ctx ends) for an instance to come back or be retired, then fails with
// errInstanceLimit (#506).
func (p *plugin) acquire(ctx context.Context) (*pooledModule, error) {
	p.guestMu.Lock()
	if p.closed.Load() {
		p.guestMu.Unlock()
		return nil, errPluginClosed
	}
	if p.retired == nil {
		p.retired = make(chan struct{})
	}
	retired := p.retired
	p.guestAcquires++
	p.guestMu.Unlock()
	defer p.finishGuestAcquire()

	select {
	case pm := <-p.pool:
		return p.acquirePooled(pm)
	default:
	}
	select {
	case p.slots <- struct{}{}:
		if p.closed.Load() {
			p.freeSlot()
			return nil, errPluginClosed
		}
		return p.newInstance(ctx)
	default:
	}
	wait := time.NewTimer(p.timeout)
	defer wait.Stop()
	select {
	case pm := <-p.pool:
		p.onWait(p.name, instanceWaitAcquired)
		return p.acquirePooled(pm)
	case p.slots <- struct{}{}:
		p.onWait(p.name, instanceWaitAcquired)
		if p.closed.Load() {
			p.freeSlot()
			return nil, errInstanceLimit
		}
		return p.newInstance(ctx)
	case <-retired:
		p.onWait(p.name, instanceWaitRejected)
		return nil, errInstanceLimit
	case <-ctx.Done():
	case <-wait.C:
	}
	p.onWait(p.name, instanceWaitRejected)
	if p.limitLog.Allow(instanceLimitLogInterval) {
		p.log.Warn("plugin instance limit reached; request rejected", "plugin", p.name, "max_instances", cap(p.slots))
	}
	return nil, errInstanceLimit
}

func (p *plugin) finishGuestAcquire() {
	p.guestMu.Lock()
	p.guestAcquires--
	retired := p.closed.Load()
	p.guestMu.Unlock()
	if retired {
		p.finishGuestClose()
	}
}

func (p *plugin) acquirePooled(pm *pooledModule) (*pooledModule, error) {
	p.guestMu.Lock()
	if p.closed.Load() {
		p.guestMu.Unlock()
		p.discard(pm)
		return nil, errPluginClosed
	}
	err := p.transitionGuestLocked(pm.ticket, inspection.GuestBusy)
	p.guestMu.Unlock()
	if err != nil {
		p.discard(pm)
		return nil, err
	}
	return pm, nil
}

// newInstance instantiates a module for a slot the caller already holds.
// A canceled successful start retains its slot if closing the module fails.
func (p *plugin) newInstance(parent context.Context) (*pooledModule, error) {
	ctx, cancel := context.WithTimeout(parent, instantiateTimeout)
	defer cancel()
	pm, err, retained := p.instantiateGuest(ctx, inspection.GuestBusy)
	if err != nil {
		if !retained {
			p.freeSlot()
		}
		if p.closed.Load() {
			return nil, errPluginClosed
		}
		return nil, err
	}
	if p.closed.Load() {
		p.discard(pm)
		return nil, errPluginClosed
	}
	return pm, nil
}

func (p *plugin) reserveGuest() (*inspection.GuestTicket, error) {
	if p.gov == nil {
		return nil, nil
	}
	p.guestMu.Lock()
	defer p.guestMu.Unlock()
	if p.closed.Load() {
		return nil, errPluginClosed
	}
	ticket, err := p.gov.ReserveGuestCapacity(p.generation, p.memoryLimitBytes)
	if err != nil {
		return nil, err
	}
	p.guestTickets[ticket] = &guestOwnership{active: true}
	return ticket, nil
}

func (p *plugin) transitionGuest(ticket *inspection.GuestTicket, state inspection.GuestState) error {
	p.guestMu.Lock()
	defer p.guestMu.Unlock()
	return p.transitionGuestLocked(ticket, state)
}

func (p *plugin) transitionGuestLocked(ticket *inspection.GuestTicket, state inspection.GuestState) error {
	if ticket == nil {
		return nil
	}
	owner, ok := p.guestTickets[ticket]
	if !ok {
		return &inspection.AdmissionError{Outcome: inspection.InvalidAccounting}
	}
	if err := ticket.Transition(state); err != nil {
		return err
	}
	owner.active = state != inspection.GuestIdle
	return nil
}

func (p *plugin) resolveGuest(ticket *inspection.GuestTicket, teardownErr error) (bool, error) {
	if ticket == nil {
		return teardownErr == nil, nil
	}
	p.guestMu.Lock()
	defer p.guestMu.Unlock()
	owner, ok := p.guestTickets[ticket]
	if !ok {
		return false, &inspection.AdmissionError{Outcome: inspection.InvalidAccounting}
	}
	owner.failed = errors.Join(owner.failed, teardownErr)
	if owner.failed == nil {
		// Wazero provides no join for its cancellation watcher; successful
		// close is not proof that the module's memory is unreachable.
		if err := ticket.Transition(inspection.GuestTeardownUnconfirmed); err != nil {
			return false, err
		}
		owner.active = false
		delete(p.guestTickets, ticket)
		return true, nil
	}
	if err := ticket.ResolveTeardown(owner.failed); err != nil {
		return false, err
	}
	owner.active = false
	return false, nil
}

func (p *plugin) hasGuestTickets() bool {
	if p == nil {
		return false
	}
	p.guestMu.Lock()
	defer p.guestMu.Unlock()
	return len(p.guestTickets) != 0
}

func (p *plugin) instantiateGuest(ctx context.Context, state inspection.GuestState) (*pooledModule, error, bool) {
	if err := ctx.Err(); err != nil {
		return nil, err, false
	}
	ticket, err := p.reserveGuest()
	if err != nil {
		return nil, err, false
	}
	mod, err := p.instantiate(ctx)
	if err != nil {
		if ticket != nil {
			var closeErr error
			if mod != nil {
				closeErr = mod.Close(context.Background())
			}
			transitionErr := p.transitionGuest(ticket, inspection.GuestClosing)
			released, resolveErr := p.resolveGuest(ticket, closeErr)
			p.finishGuestClose()
			return nil, errors.Join(err, closeErr, transitionErr, resolveErr), !released || resolveErr != nil
		}
		return nil, err, false
	}
	if mod.IsClosed() {
		transitionErr := p.transitionGuest(ticket, inspection.GuestClosing)
		closeErr := mod.Close(context.Background())
		released, resolveErr := p.resolveGuest(ticket, closeErr)
		p.finishGuestClose()
		return nil, errors.Join(errors.New("plugin instance closed during initialization"), closeErr, transitionErr, resolveErr),
			!released || resolveErr != nil
	}
	if err := ctx.Err(); err != nil {
		closeErr := mod.Close(context.Background())
		if ticket != nil {
			transitionErr := p.transitionGuest(ticket, inspection.GuestClosing)
			released, resolveErr := p.resolveGuest(ticket, closeErr)
			closeErr = errors.Join(closeErr, transitionErr, resolveErr)
			p.finishGuestClose()
			return nil, errors.Join(err, closeErr), !released || resolveErr != nil
		}
		p.finishGuestClose()
		return nil, errors.Join(err, closeErr), closeErr != nil
	}
	if err := p.transitionGuest(ticket, state); err != nil {
		closeErr := mod.Close(context.Background())
		transitionErr := p.transitionGuest(ticket, inspection.GuestClosing)
		released, resolveErr := p.resolveGuest(ticket, closeErr)
		p.finishGuestClose()
		return nil, errors.Join(err, closeErr, transitionErr, resolveErr), !released || resolveErr != nil
	}
	return &pooledModule{mod: mod, ticket: ticket}, nil, false
}

// discard closes an instance. A successful close frees its local slot, but
// its process reservation remains charged because last-alias teardown is unknown.
func (p *plugin) discard(pm *pooledModule) {
	if pm == nil {
		return
	}
	transitionErr := p.transitionGuest(pm.ticket, inspection.GuestClosing)
	if transitionErr != nil {
		if p.log != nil {
			p.log.Error("plugin guest lifecycle transition failed", "plugin", p.name, "error", transitionErr)
		}
	}
	mod := pm.mod
	pm.mod = nil
	var closeErr error
	if mod != nil {
		closeErr = mod.Close(context.Background())
		mod = nil
	} else {
		closeErr = errGuestTeardownUnconfirmed
	}
	released, resolveErr := p.resolveGuest(pm.ticket, closeErr)
	if resolveErr != nil && p.log != nil {
		p.log.Error("plugin guest capacity accounting failed after close", "plugin", p.name, "error", resolveErr)
	}
	if closeErr != nil {
		if p.log != nil {
			p.log.Error("plugin guest close failed; capacity remains reserved", "plugin", p.name, "error", closeErr)
		}
	} else if released && resolveErr == nil {
		p.freeSlot()
	}
	p.finishGuestClose()
}

func (p *plugin) finishGuestClose() {
	if !p.closed.Load() {
		return
	}
	if err := p.closeRuntime(context.Background()); err != nil &&
		!errors.Is(err, errGuestWorkInFlight) && p.log != nil {
		p.log.Error("plugin runtime teardown remains incomplete", "plugin", p.name, "error", err)
	}
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
	p.guestMu.Lock()
	if !p.closed.Load() && pm.calls < p.maxInstanceInvocations {
		if err := p.transitionGuestLocked(pm.ticket, inspection.GuestIdle); err == nil {
			select {
			case p.pool <- pm:
				p.guestMu.Unlock()
				return
			default:
				if err := p.transitionGuestLocked(pm.ticket, inspection.GuestBusy); err != nil && p.log != nil {
					p.log.Error("plugin guest lifecycle transition failed", "plugin", p.name, "error", err)
				}
			}
		} else if p.log != nil {
			p.log.Error("plugin guest lifecycle transition failed", "plugin", p.name, "error", err)
		}
	}
	p.guestMu.Unlock()
	p.discard(pm)
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

func shouldCountPluginPanic(err error) bool {
	return !errors.Is(err, errInstanceLimit) &&
		!errors.Is(err, errPluginClosed) &&
		!inspection.IsOverload(err) &&
		!inspection.IsOutcome(err, inspection.InvalidAccounting)
}

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
			fn = nil
			mod = nil
			p.discard(pm)
			res, dur, outcome = 0, time.Since(start), callTrapped
			p.logTrap(trapHostPanic)
		}
	}()

	results, callErr := fn.Call(ctx)
	fn = nil
	mod = nil
	dur = time.Since(start)
	if callErr != nil {
		p.discard(pm)
		// A guest that traps after a host call was refused budget still
		// fails as overload, not as a guest fault: the refusal is retained
		// in the invocation whatever the guest did with its error return.
		if inspection.IsOverload(inv.err) {
			return 0, dur, callHostError, inv.err
		}
		p.logTrap(trapReason(callErr))
		return 0, dur, callTrapped, callErr
	}

	// A host function may have rejected the request (oversize body, response
	// overflow); treat that as a contained failure so the caller returns 500
	// instead of serving a truncated request/response.
	if inv.err != nil {
		p.discard(pm)
		return 0, dur, callHostError, inv.err
	}
	res = uint32(results[0])
	if valid != nil && !valid(res) {
		p.discard(pm)
		return 0, dur, callHostError, errBadResult
	}

	p.release(pm)
	return res, dur, callOK, nil
}

// Contained-trap reasons. Only distinctions wazero exposes through its public
// API are named; everything else, including wasm runtime traps whose error
// type is internal to wazero, is "unknown".
const (
	trapTimeout   = "timeout"    // the invocation timeout expired
	trapCanceled  = "canceled"   // the request or plugin context ended
	trapGuestExit = "guest_exit" // the guest exited, e.g. a Go wasip1 panic
	trapHostPanic = "host_panic" // a Go panic recovered around the call
	trapUnknown   = "unknown"
)

// trapLogInterval spaces the contained-trap warning per plugin.
const trapLogInterval = 10 * time.Second

// trapReason classifies a failed guest call into a bounded reason.
func trapReason(err error) string {
	var exit *sys.ExitError
	if !errors.As(err, &exit) {
		return trapUnknown
	}
	switch exit.ExitCode() {
	case sys.ExitCodeDeadlineExceeded:
		return trapTimeout
	case sys.ExitCodeContextCanceled:
		return trapCanceled
	}
	return trapGuestExit
}

// logTrap emits the throttled contained-trap warning. It carries only the
// plugin name and a bounded reason, never the guest's error text, which can
// echo guest memory or request data.
func (p *plugin) logTrap(reason string) {
	if p.trapLog.Allow(trapLogInterval) {
		p.log.Warn("plugin guest failure contained; request answered 500", "plugin", p.name, "reason", reason)
	}
}

// invoke runs the guest's handle_request for one HTTP request. It returns the
// guest's action (Continue/Stop), the invocation holding any response the guest
// produced, and an error if the guest trapped, panicked, or timed out (which the
// caller turns into a 500). On error the instance is discarded, not pooled,
// because a trapped module may be in an undefined state.
//
// parent must carry the request's inspection lease; host allocations made for
// the invocation are charged to it. On error, any charge the invocation still
// holds has been released.
func (p *plugin) invoke(parent context.Context, w http.ResponseWriter, r *http.Request) (action uint32, inv *invocation, err error) {
	inv = &invocation{r: r, w: w, log: p.log, maxRespBody: p.maxRespBody, sub: -1, lease: leaseFrom(parent)}
	if inv.lease == nil {
		return 0, nil, errNoLease
	}
	own := inv
	defer func() {
		own.endCall()
		if err != nil {
			own.releaseAll()
		}
	}()
	if p.readsBody {
		if err := p.prepare(parent, w, inv); err != nil {
			return 0, nil, err
		}
	}
	var valid func(uint32) bool
	if p.abi == ABIJulV2 {
		valid = func(v uint32) bool { return v == actionStop || v == actionContinue }
	}
	action, dur, outcome, err := p.call(parent, exportHandleRequest, inv, valid)
	switch outcome {
	case callAcquireFailed:
		if shouldCountPluginPanic(err) {
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
	inv := &invocation{r: r, log: p.log, maxRespBody: p.maxRespBody,
		phase: phaseResponse, sub: -1, state: state, resp: view, lease: leaseFrom(parent)}
	if inv.lease == nil {
		return 0, errNoLease
	}
	defer inv.endCall()
	valid := func(v uint32) bool { return v == responseContinue || (v == responseReject && !view.committed) }
	if view.committed {
		parent = context.WithoutCancel(parent)
	}
	res, dur, outcome, err := p.call(parent, exportHandleResponse, inv, valid)
	switch outcome {
	case callAcquireFailed:
		if shouldCountPluginPanic(err) {
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

// close retires the plugin and wakes waiters; the runtime stays owned until
// in-flight guests and pool handoffs have finished.
func (p *plugin) close() error {
	if p == nil {
		return nil
	}
	p.guestMu.Lock()
	if !p.closed.Swap(true) && p.retired != nil {
		close(p.retired)
	}
	p.guestMu.Unlock()
	p.abortPreparations()
	if p.mgr != nil {
		p.mgr.retain(p)
		p.mgr.forget(p)
	}
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
	err := p.closeRuntime(context.Background())
	if err != nil && !errors.Is(err, errGuestWorkInFlight) && p.log != nil {
		p.log.Error("plugin runtime teardown failed", "plugin", p.name, "error", err)
	}
	return err
}

func (p *plugin) closeRuntime(ctx context.Context) error {
	p.runtimeCloseMu.Lock()
	defer p.runtimeCloseMu.Unlock()

	p.guestMu.Lock()
	if p.guestAcquires != 0 {
		p.guestMu.Unlock()
		return errGuestWorkInFlight
	}
	for _, owner := range p.guestTickets {
		if owner.active {
			p.guestMu.Unlock()
			return errGuestWorkInFlight
		}
	}
	p.guestMu.Unlock()
	if p.runtime == nil {
		if p.hasGuestTickets() {
			return fmt.Errorf("plugin %q has reserved guest capacity without a runtime owner", p.name)
		}
		if p.mgr != nil {
			p.mgr.forgetRetained(p)
		}
		return nil
	}
	if !p.runtimeClosed {
		p.runtimeCloseErr = p.runtime.Close(ctx)
		p.runtimeClosed = true
	}
	var errs []error
	if p.runtimeCloseErr != nil {
		errs = append(errs, p.runtimeCloseErr)
	}
	p.guestMu.Lock()
	for {
		select {
		case pm := <-p.pool:
			// A returning call may still hold the wrapper after pooling it.
			pm.mod = nil
		default:
			goto drained
		}
	}
drained:
	for ticket, owner := range p.guestTickets {
		if p.runtimeCloseErr != nil && !errors.Is(owner.failed, p.runtimeCloseErr) {
			owner.failed = errors.Join(owner.failed, p.runtimeCloseErr)
		}
		if owner.failed != nil {
			if transitionErr := ticket.Transition(inspection.GuestClosing); transitionErr != nil {
				errs = append(errs, transitionErr)
			} else if resolveErr := ticket.ResolveTeardown(owner.failed); resolveErr != nil {
				errs = append(errs, resolveErr)
			}
			errs = append(errs, owner.failed)
		} else {
			if err := ticket.Transition(inspection.GuestClosing); err != nil {
				errs = append(errs, err)
			} else if err := ticket.Transition(inspection.GuestTeardownUnconfirmed); err != nil {
				errs = append(errs, err)
			} else {
				delete(p.guestTickets, ticket)
				p.freeSlot()
			}
		}
	}
	remaining := len(p.guestTickets)
	p.guestMu.Unlock()
	if remaining == 0 && p.runtimeCloseErr == nil && p.mgr != nil {
		p.mgr.forgetRetained(p)
	}
	return errors.Join(errs...)
}

// inspectionFacts derives the static inputs to the route minimum budget from
// the plugin's configured limits and the host functions it imports.
func (p *plugin) inspectionFacts(decl abiDecl) inspection.PluginFacts {
	return inspection.PluginFacts{
		Name:                 p.name,
		MaxRequestBody:       uint64(max(p.maxReqBody, 0)),
		MaxResponseBody:      uint64(max(p.maxRespBody, 0)),
		MaxFetchResponse:     uint64(max(p.maxFetchResp, 0)),
		MemoryLimit:          p.memoryLimitBytes,
		ReadsBody:            decl.readsBody,
		SetsRequestState:     decl.imports["set_request_state"],
		WritesResponseBody:   decl.imports["write_response_body"],
		Fetch:                p.capFetch && decl.imports["fetch"],
		SubscribesResponse:   decl.hasResponse && !p.isHandler,
		ReplacesResponseBody: decl.hasResponse && !p.isHandler && decl.imports["resp_body_replace"],
	}
}
