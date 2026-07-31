// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"

	"jul/internal/config"
)

// wasmPageSize is the WebAssembly linear-memory page size (64 KiB).
const wasmPageSize = 1 << 16

// instantiateTimeout bounds module instantiation (running the guest's
// _initialize), independent of the much shorter per-request call timeout.
const instantiateTimeout = 10 * time.Second

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
	log      *slog.Logger
	cache    wazero.CompilationCache
	kv       KVStore
	onInvoke func(string, string, time.Duration)
	onPanic  func(string)
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
	return &Manager{
		log:        opts.Logger,
		cache:      wazero.NewCompilationCache(),
		kv:         kv,
		onInvoke:   onInvoke,
		onPanic:    onPanic,
		egressWrap: opts.EgressWrap,
	}, nil
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
		p, err := m.compilePlugin(ctx, name, pc)
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
	name      string
	runtime   wazero.Runtime
	compiled  wazero.CompiledModule
	pool      sync.Pool // of api.Module
	timeout   time.Duration
	isHandler bool

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

	// KV accounting bounds the per-plugin namespace independent of the shared
	// store: kvKeys tracks each key's stored size so kv_set can reject an entry
	// or a total that would exceed the plugin's quota.
	kvMu    sync.Mutex
	kvKeys  map[string]int
	kvBytes int

	log      *slog.Logger
	onInvoke func(string, string, time.Duration)
	onPanic  func(string)
}

func (m *Manager) compilePlugin(ctx context.Context, name string, pc config.PluginConfig) (*plugin, error) {
	wasm, err := loadModule(pc)
	if err != nil {
		return nil, err
	}

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
		timeout:      pc.Timeout.Std(),
		isHandler:    pc.Type == "handler",
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
		kvKeys:       make(map[string]int),
		log:          m.log,
		onInvoke:     m.onInvoke,
		onPanic:      m.onPanic,
		egressWrap:   m.egressWrap,
	}
	if p.fetchTimeout <= 0 {
		p.fetchTimeout = 5 * time.Second
	}
	if p.kvMaxEntries <= 0 {
		p.kvMaxEntries = 1024
	}
	if p.timeout <= 0 {
		p.timeout = 100 * time.Millisecond
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
	// if the reload is cancelled before compilation finishes (M-01).
	rtCfg := wazero.NewRuntimeConfig().
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

	registrar, ok := abiRegistry[ABIJulV1]
	if !ok {
		return closeOnErr(fmt.Errorf("unknown ABI %q", ABIJulV1))
	}
	if err := registrar(ctx, r, p); err != nil {
		return closeOnErr(fmt.Errorf("register host module: %w", err))
	}

	compiled, err := r.CompileModule(ctx, wasm)
	if err != nil {
		return closeOnErr(fmt.Errorf("compile module: %w", err))
	}
	if _, ok := compiled.ExportedFunctions()["handle_request"]; !ok {
		_ = compiled.Close(ctx)
		return closeOnErr(errors.New("module does not export handle_request (build it against the Jul.IA plugin SDK)"))
	}

	p.runtime = r
	p.compiled = compiled

	// Eagerly instantiate one instance so a broken module fails the build (and
	// thus the reload) rather than the first request.
	mod, err := p.instantiate(ctx)
	if err != nil {
		return closeOnErr(fmt.Errorf("instantiate module: %w", err))
	}
	p.pool.Put(mod)

	return p, nil
}

// sizeOr returns the byte count of s, or def when s is non-positive.
func sizeOr(s config.Size, def int) int {
	if n := s.Bytes(); n > 0 {
		return int(n)
	}
	return def
}

// loadModule reads the plugin's wasm bytes from its path or inline base64.
func loadModule(pc config.PluginConfig) ([]byte, error) {
	switch {
	case pc.Path != "":
		return os.ReadFile(pc.Path)
	case pc.Inline != "":
		return base64.StdEncoding.DecodeString(pc.Inline)
	default:
		return nil, errors.New("no module source (set path or inline)")
	}
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

func (p *plugin) acquire() (api.Module, error) {
	if v := p.pool.Get(); v != nil {
		return v.(api.Module), nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), instantiateTimeout)
	defer cancel()
	return p.instantiate(ctx)
}

func (p *plugin) release(mod api.Module) { p.pool.Put(mod) }

// kvSet stores a value under an already-namespaced key, enforcing the plugin's
// per-namespace quota: it rejects (returns false) a value that would push the
// total byte size or the distinct-key count over the configured caps.
func (p *plugin) kvSet(key string, val []byte) bool {
	p.kvMu.Lock()
	defer p.kvMu.Unlock()
	prev, exists := p.kvKeys[key]
	newTotal := p.kvBytes - prev + len(val)
	if newTotal > p.kvMaxBytes {
		return false
	}
	if !exists && len(p.kvKeys) >= p.kvMaxEntries {
		return false
	}
	p.kv.Set(key, val)
	p.kvKeys[key] = len(val)
	p.kvBytes = newTotal
	return true
}

// invoke runs the guest's handle_request for one HTTP request. It returns the
// guest's action (Continue/Stop), the invocation holding any response the guest
// produced, and an error if the guest trapped, panicked, or timed out (which the
// caller turns into a 500). On error the instance is discarded, not pooled,
// because a trapped module may be in an undefined state.
func (p *plugin) invoke(parent context.Context, w http.ResponseWriter, r *http.Request) (action uint32, inv *invocation, err error) {
	mod, err := p.acquire()
	if err != nil {
		p.onPanic(p.name)
		return 0, nil, err
	}

	inv = &invocation{r: r, w: w, log: p.log, maxReqBody: p.maxReqBody, maxRespBody: p.maxRespBody}
	ctx, cancel := context.WithTimeout(withInvocation(parent, inv), p.timeout)
	defer cancel()

	start := time.Now()
	fn := mod.ExportedFunction("handle_request")

	defer func() {
		if rec := recover(); rec != nil {
			err = fmt.Errorf("plugin %q panicked: %v", p.name, rec)
			_ = mod.Close(context.Background())
			p.onPanic(p.name)
			p.onInvoke(p.name, "error", time.Since(start))
		}
	}()

	results, callErr := fn.Call(ctx)
	dur := time.Since(start)
	if callErr != nil {
		_ = mod.Close(context.Background())
		p.onPanic(p.name)
		p.onInvoke(p.name, "error", dur)
		return 0, inv, callErr
	}

	// A host function may have rejected the request (oversize body, response
	// overflow); treat that as a contained failure so the caller returns 500
	// instead of serving a truncated request/response.
	if inv.err != nil {
		_ = mod.Close(context.Background())
		p.onInvoke(p.name, "error", dur)
		return 0, inv, inv.err
	}

	p.release(mod)
	action = uint32(results[0])
	result := "stop"
	if action == 1 {
		result = "continue"
	}
	p.onInvoke(p.name, result, dur)
	return action, inv, nil
}

// close tears down the plugin's runtime, which closes every instance (pooled or
// in flight) and the compiled module.
func (p *plugin) close() {
	if p == nil {
		return
	}
	if p.client != nil {
		p.client.CloseIdleConnections()
	}
	if p.runtime == nil {
		return
	}
	_ = p.runtime.Close(context.Background())
}
