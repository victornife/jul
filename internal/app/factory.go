// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"jul/internal/auth"
	"jul/internal/cache"
	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/handler"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/router"
	"jul/internal/upstream"
	"jul/internal/waf"
)

// HandlerFactory holds the process-lifetime dependencies for the per-reload
// handler-tree builder. All fields are set once at startup and read on every
// reload; the factory itself is safe for concurrent use via its internal mutex.
//
// Create a HandlerFactory after all process-lifetime resources are initialised
// and pass Build to server.New and app.Preflight. Extracting the per-reload
// build logic here makes it unit-testable without a full process boot (ADR-0007).
type HandlerFactory struct {
	Log         *slog.Logger
	Metrics     *observability.Metrics
	Cache       *cache.Cache // nil when caching is disabled
	AccessSinks []middleware.AccessSink
	RLStore     *middleware.RateLimiterStore
	EgressDial  func(context.Context, string, string) (net.Conn, error)
	PoolReg     *upstream.Registry
	PluginMgr   *plugins.Manager
	GenRes      *GenerationResources
	RT          *Runtime // Tracer.Middleware, ACME

	mu sync.Mutex // serialises every build (startup, reload, preflight)

	// genCounter assigns monotonically increasing IDs to handler generations.
	// It is used to tag generation-scoped resources (redaction, pool snapshots)
	// so the server can retire them safely.
	genCounter atomic.Uint64
}

// Build rebuilds the per-listen-address handler tree from c. It is used for
// preflight (dry-run) validation by Preflight.Apply. When commit is false the
// staged generation is aborted before returning and the live serving state is
// unchanged. When commit is true the generation is committed inline (legacy
// live path). For the three-phase live-reload path use Prepare instead.
//
// c is expected to be the secret-expanded effective configuration; Build does
// not resolve secrets again (R7-05).
func (f *HandlerFactory) Build(ctx context.Context, c *config.Config, commit bool) (map[string]http.Handler, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	upstreams := IndexUpstreams(c.Upstreams)
	gen := f.GenRes.Begin()
	defer gen.Abort()

	handlers, err := f.buildHandlers(ctx, c, gen, upstreams)
	if err != nil {
		return nil, nil, err
	}
	var retirePrev func()
	if commit {
		retirePrev = gen.Commit()
		f.PoolReg.Activate()
	}
	return handlers, retirePrev, nil
}

// Prepare stages a new handler generation for cfg without committing it. The
// returned commitFn promotes the staged resources (upstream pools, closers) to
// live, captures the generation-scoped pool snapshots from the now-live
// registry, and returns a retire callback for the previous generation. The
// returned abortFn discards staged resources, leaving the live generation
// untouched. The returned genID is the unique identifier for this generation,
// used by the server to retire its redaction entry. The factory does not
// return a redact.State: the caller resolves secrets exactly once into a
// config.Candidate and installs its redaction state at the publish boundary
// (R7-05, R9-01).
// Exactly one of commitFn or abortFn must be called; both release the factory
// mutex so no concurrent build can start while a staged generation is pending.
func (f *HandlerFactory) Prepare(ctx context.Context, c *config.Config) (handlers map[string]http.Handler, genID uint64, commitFn func() (upstream.SnapshotMap, func()), abortFn func(), err error) {
	f.mu.Lock()
	// Mutex is NOT deferred here: it is released by commitFn or abortFn.

	upstreams := IndexUpstreams(c.Upstreams)
	gen := f.GenRes.Begin()

	handlers, err = f.buildHandlers(ctx, c, gen, upstreams)
	if err != nil {
		gen.Abort()
		f.mu.Unlock()
		return nil, 0, nil, nil, err
	}

	usedUpstreamKeys := upstreamKeysUsed(c, upstreams)
	genID = f.genCounter.Add(1)

	committed := false
	commitFn = func() (upstream.SnapshotMap, func()) {
		if committed {
			return nil, func() {}
		}
		committed = true
		ret := gen.Commit()
		// Snapshots must be captured AFTER pools commit so the generation
		// carries the backend view of the configuration it represents (R8-01).
		snapshots := f.PoolReg.SnapshotPools(usedUpstreamKeys)
		// Background workers start only after Commit so a build that aborts
		// does not trigger discovery/health side effects for a candidate that
		// never goes live (R9-07).
		f.PoolReg.Activate()
		f.mu.Unlock()
		return snapshots, ret
	}
	abortFn = func() {
		if committed {
			return
		}
		committed = true
		gen.Abort()
		f.mu.Unlock()
	}
	return handlers, genID, commitFn, abortFn, nil
}

// buildHandlers constructs the per-listen-address handler tree from c, staging
// all closeable resources (plugin runtimes, static-file roots, gRPC connections)
// into gen. It neither commits nor aborts gen; resource lifecycle is the
// caller's responsibility. upstreams must be IndexUpstreams(c.Upstreams).
func (f *HandlerFactory) buildHandlers(ctx context.Context, c *config.Config, gen *Generation, upstreams map[string]config.UpstreamConfig) (map[string]http.Handler, error) {

	// Check context before starting a potentially slow plugin compilation.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("build aborted before plugin compilation: %w", err)
	}

	// Build this generation's WASM plugin set. A lean build (or a malformed
	// module) fails here, rejecting the reload. The set owns per-plugin wazero
	// runtimes; register it for generational teardown so the previous set is
	// closed only after the new handlers are live.
	pluginSet, err := f.PluginMgr.Build(ctx, c.Plugins)
	if err != nil {
		return nil, fmt.Errorf("plugins: %w", err)
	}
	gen.Stage(pluginSet)

	// Check context after plugin compilation — the most expensive step.
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("build aborted after plugin compilation: %w", err)
	}

	withCache := func(loc config.LocationConfig, h http.Handler) http.Handler {
		if loc.Cache && f.Cache != nil {
			return f.Cache.Handler(h)
		}
		return h
	}

	// Static locations may serve precompressed .br/.gz sidecars when the
	// compression feature has precompressed enabled.
	var staticOpts handler.StaticOptions
	if c.Compression.IsEnabled() && c.Compression.Precompressed {
		staticOpts.Precompressed = true
		staticOpts.Encoders = c.Compression.Encoders
	}

	builders := map[string]router.Builder{
		router.ActionStatic: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			h, err := handler.NewStaticWithOptions(srv, loc, staticOpts)
			if err != nil {
				return nil, err
			}
			// The static handler holds an os.Root directory handle open for
			// its lifetime; register it for generational teardown so the
			// previous generation's root is closed when this config is
			// replaced (otherwise the FD leaks until GC finalizes it).
			if c, ok := h.(io.Closer); ok {
				gen.Stage(c)
			}
			return h, nil
		},
		router.ActionProxy: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			// grpc = true forwards native gRPC over end-to-end HTTP/2
			// (trailers preserved, no buffering); otherwise it is a plain
			// HTTP reverse proxy. gRPC passthrough is not cacheable.
			if loc.GRPC {
				h, err := handler.NewGRPCProxy(ctx, srv, loc, upstreams, f.PoolReg, f.Log, f.Metrics.ObserveGRPCProxyStream)
				if err != nil {
					return nil, err
				}
				if c, ok := h.(io.Closer); ok {
					gen.Stage(c)
				}
				return h, nil
			}
			h, err := handler.NewProxy(ctx, srv, loc, upstreams, f.PoolReg, f.Log, f.Metrics.ObserveHTTPDialFailure)
			if err != nil {
				return nil, err
			}
			// The proxy owns an http.Transport with its own connection pool.
			// Staging it means the pool is closed when this generation retires,
			// so a connection established under a superseded backend_tls policy
			// cannot be reused by a later one. Registered before the cache
			// wrapper, which would otherwise hide the io.Closer.
			if c, ok := h.(io.Closer); ok {
				gen.Stage(c)
			}
			return withCache(loc, h), nil
		},
		router.ActionFastCGI: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			h, err := handler.NewFastCGI(srv, loc, f.Log)
			if err != nil {
				return nil, err
			}
			return withCache(loc, h), nil
		},
		router.ActionGRPCTranscode: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			h, err := handler.NewGRPCTranscode(ctx, srv, loc, upstreams, f.PoolReg, f.Log, f.Metrics.ObserveGRPCTranscode, f.Metrics.ObserveGRPCTranscodeStreamMsg)
			if err != nil {
				return nil, err
			}
			// The transcoder owns a gRPC client connection; register it for
			// teardown when this configuration is replaced.
			if c, ok := h.(io.Closer); ok {
				gen.Stage(c)
			}
			return h, nil
		},
		router.ActionPlugin: func(_ config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
			h := pluginSet.Handler(loc.Plugin)
			if h == nil {
				return nil, fmt.Errorf("handler plugin %q is not loaded", loc.Plugin)
			}
			return withCache(loc, h), nil
		},
	}

	// Rate limiting is applied per location: a location's own [rate_limit]
	// overrides the global policy, otherwise the global policy applies. The
	// store persists across reloads, so a stable scope keeps each bucket's
	// state; global-policy locations share one "global" bucket per key while
	// overrides get their own scope.
	locRateLimit := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		rl := c.RateLimit
		scope := "global"
		if loc.RateLimit != nil {
			rl = *loc.RateLimit
			scope = "loc:" + srv.Listen + "|" + strings.Join(srv.ServerNames, ",") + "|" + loc.Match.Path
		}
		if !rl.Enabled {
			return nil
		}
		kind := RateKeyKind(rl.Key)
		lim := f.RLStore.Scoped(scope, rl.Rate, rl.Burst)
		return middleware.RateLimit(lim, middleware.RateKeyFunc(rl.Key), func() {
			f.Metrics.ObserveRateLimited(kind)
		})
	}

	// Authenticators are built once per reload, keyed by a stable location
	// scope, so a misconfiguration (for example, an unreadable htpasswd file)
	// fails the reload with a clear error instead of surfacing per request.
	authByScope := make(map[string]*auth.Authenticator)
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := c.Servers[i].Locations[j]
			if loc.Auth == nil {
				continue
			}
			key := AuthScope(c.Servers[i], loc)
			a, err := auth.New(ctx, *loc.Auth, auth.Options{
				Logger:      f.Log,
				OnDecision:  f.Metrics.ObserveAuthDecision,
				DialContext: f.EgressDial,
			})
			if err != nil {
				return nil, fmt.Errorf("location %s: %w", key, err)
			}
			authByScope[key] = a
		}
	}
	locAuth := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		if loc.Auth == nil {
			return nil
		}
		if a := authByScope[AuthScope(srv, loc)]; a != nil {
			return a.Wrap
		}
		return nil
	}

	// Firewalls are built once per reload, keyed by location scope. The
	// effective policy for a location is its own [waf] override when set,
	// otherwise the global [waf] policy. Building here means a rule-compile
	// error (a bad SecLang file or CRS asset) fails the reload with a clear
	// message instead of surfacing per request. In a lean build (no "waf"
	// tag) waf.New is never reached because waf.Check already rejected the
	// configuration at startup.
	wafByScope := make(map[string]*waf.Firewall)
	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := c.Servers[i].Locations[j]
			wcfg, ok := EffectiveWAF(c, loc)
			if !ok {
				continue
			}
			fw, err := waf.New(ctx, wcfg, waf.Options{
				Logger: f.Log,
				Hooks:  waf.Hooks{OnEvent: f.Metrics.ObserveWAFEvent},
			})
			if err != nil {
				return nil, fmt.Errorf("location %s: %w", WAFScope(c.Servers[i], loc), err)
			}
			wafByScope[WAFScope(c.Servers[i], loc)] = fw
		}
	}
	locWAF := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		fw := wafByScope[WAFScope(srv, loc)]
		if fw == nil {
			return nil
		}
		return fw.Middleware()
	}

	// Compose the per-location modifiers. WASM middleware plugins run
	// OUTERMOST (edge position) so they can inspect or block a request before
	// authentication; server-level plugins wrap location-level ones. Inside
	// the plugins, authentication wraps rate limiting, making validated JWT
	// claims available to a "jwt:<claim>" rate-limit key. All stay modifiers
	// that compose around the action; none is an action itself.
	locModifier := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
		au := locAuth(srv, loc)
		rl := locRateLimit(srv, loc)
		wf := locWAF(srv, loc)
		// Client-certificate handling applies when the server enables mutual
		// TLS or the location requires a client certificate. It populates the
		// $ssl_client_* identity for proxied requests and, when the location
		// sets require_client_cert, rejects a request that arrives without a
		// verified certificate with 403. It runs just inside the plugins and
		// outside authentication so the identity is available to auth and the
		// upstream.
		var cc middleware.Middleware
		if (srv.TLS != nil && srv.TLS.ClientAuth.Active()) || loc.RequireClientCert {
			forward := middleware.ForwardCertNone
			if srv.TLS != nil && srv.TLS.ClientAuth != nil {
				forward = strings.ToLower(strings.TrimSpace(srv.TLS.ClientAuth.ForwardCertificate))
			}
			cc = middleware.ClientCert(loc.RequireClientCert, forward)
		}
		var pluginMW []middleware.Middleware
		for _, name := range srv.Plugins {
			if mw := pluginSet.Middleware(name); mw != nil {
				pluginMW = append(pluginMW, mw)
			}
		}
		for _, name := range loc.Plugins {
			if mw := pluginSet.Middleware(name); mw != nil {
				pluginMW = append(pluginMW, mw)
			}
		}
		if au == nil && rl == nil && cc == nil && wf == nil && len(pluginMW) == 0 {
			return nil
		}
		return func(next http.Handler) http.Handler {
			h := next
			// The WAF runs just inside authentication and outside rate
			// limiting: an authenticated identity is available to rules,
			// while a request blocked by a rule is rejected before the
			// action runs. It sits before the global compression layer so
			// rules inspect the response body uncompressed.
			if wf != nil {
				h = wf(h)
			}
			if rl != nil {
				h = rl(h)
			}
			if au != nil {
				h = au(h)
			}
			if cc != nil {
				h = cc(h)
			}
			// Apply plugin middleware in reverse so the first-listed wraps
			// outermost (server plugins before location plugins).
			for i := len(pluginMW) - 1; i >= 0; i-- {
				h = pluginMW[i](h)
			}
			return h
		}
	}

	rtr, err := router.New(c, builders, nil, locModifier, f.Log)
	if err != nil {
		return nil, err
	}

	// Compression is built once per reload and shared across listeners. It
	// is the innermost middleware so Metrics and the access log observe the
	// bytes actually sent on the wire. A configured encoder that is not
	// compiled into this build fails the reload with a clear error.
	var compress middleware.Middleware
	if c.Compression.IsEnabled() {
		cm, err := middleware.NewCompression(middleware.CompressionOptions{
			Encoders:   c.Compression.Encoders,
			Level:      c.Compression.Level,
			MinSize:    c.Compression.MinSize.Bytes(),
			Types:      c.Compression.Types,
			OnCompress: f.Metrics.ObserveCompression,
		})
		if err != nil {
			return nil, err
		}
		compress = cm
	}

	handlers := make(map[string]http.Handler)
	for _, addr := range UniqueListenAddrs(c.Servers) {
		// The trusted-proxy policy is compiled here, during Prepare, so a
		// malformed prefix aborts the reload before anything is published.
		policy, err := ClientAddressPolicy(c.Servers, addr)
		if err != nil {
			return nil, fmt.Errorf("listen %s: client_address: %w", addr, err)
		}
		h := middleware.Chain(rtr.For(addr), f.globalChain(policy, compress)...)
		// On plain HTTP listeners, answer ACME HTTP-01 challenges outermost so
		// certificate issuance/renewal works even when the listener otherwise
		// redirects to HTTPS. Non-challenge requests fall through to h.
		if f.RT.ACME != nil && !AddrServesTLS(c.Servers, addr) {
			h = f.RT.ACME.ChallengeHandler(h)
		}
		handlers[addr] = h
	}

	return handlers, nil
}

// globalChain returns the per-listen-address middleware chain, outermost to
// innermost:
//
//	RequestID  — assigns/propagates the request id first so every inner layer
//	             (observers, Recover's panic log) sees it.
//	ClientAddr — derives the canonical client address from the trusted-proxy
//	             policy of THIS listen address. It sits at index 1: outside the
//	             router, so derivation precedes every read of the Host header
//	             and an attacker-chosen vhost cannot select its own trust
//	             policy; and outside the observers, so metrics, access logging,
//	             tracing and every per-location middleware read one identity.
//	Tracing    — starts the server span and extracts W3C tracecontext just
//	             inside RequestID, so the trace id is in context before the
//	             observers record it. No-op unless built with the "otel" tag
//	             and enabled.
//	metrics    — observer: counts requests + latency by final status.
//	AccessLog  — observer: builds one record per request and fans it out to the
//	             configured sinks (stdout/file/syslog).
//	Recover    — converts a panic into a 500. It sits INSIDE the observers on
//	             purpose: it returns normally after recovering, so metrics and
//	             the access log still record the request as a 500 instead of it
//	             vanishing.
//	Compression— innermost wrapper so the observers' recorder counts the
//	             compressed (on-the-wire) byte count.
//
// Per-location concerns (rate limiting, body limit) are applied inside the
// router via LocationModifier, closer to the handler.
func (f *HandlerFactory) globalChain(policy *clientaddr.Policy, compress middleware.Middleware) []middleware.Middleware {
	mws := []middleware.Middleware{
		middleware.RequestID(),
		middleware.ClientAddress(policy, f.Log, f.Metrics.ObserveClientAddrDerivation),
		f.RT.Tracer.Middleware,
		f.Metrics.Middleware,
	}
	if len(f.AccessSinks) > 0 {
		mws = append(mws, middleware.AccessLog(f.AccessSinks...))
	}
	mws = append(mws, middleware.Recover(f.Log))
	if compress != nil {
		mws = append(mws, compress)
	}
	return mws
}

// upstreamKeysUsed returns the distinct (name, scheme) pairs referenced by
// proxy, gRPC passthrough, and gRPC transcoding locations. Only configured
// upstreams (present in the upstreams index) are included; concrete host:port
// targets are ignored because they build ad-hoc pools outside the registry.
func upstreamKeysUsed(c *config.Config, upstreams map[string]config.UpstreamConfig) []upstream.PoolSnapshotKey {
	seen := make(map[string]struct{})
	var keys []upstream.PoolSnapshotKey
	add := func(name, scheme string) {
		if _, ok := upstreams[name]; !ok {
			return
		}
		if scheme == "" {
			scheme = "http"
		}
		key := name + "\x1f" + scheme
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, upstream.PoolSnapshotKey{Name: name, Scheme: scheme})
	}

	for i := range c.Servers {
		for j := range c.Servers[i].Locations {
			loc := &c.Servers[i].Locations[j]
			if loc.ProxyPass != "" {
				if u, err := url.Parse(loc.ProxyPass); err == nil && u.Host != "" {
					add(u.Host, u.Scheme)
				}
			}
			if loc.GRPCTranscode != nil && loc.GRPCTranscode.Target != "" {
				add(loc.GRPCTranscode.Target, "http")
			}
		}
	}
	return keys
}
