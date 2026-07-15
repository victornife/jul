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
	"strings"
	"sync"

	"jul/internal/auth"
	"jul/internal/cache"
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
}

// Build rebuilds the per-listen-address handler tree from c. It is invoked at
// startup and on every reload so all routing state (router, upstream pools,
// content handlers) is reconstructed atomically.
//
// When commit is true the freshly built generation is promoted (staged pools
// committed, the previous generation's handler closers torn down after drain).
// When false the build is a throwaway preflight: staged pools and closers are
// released by the deferred abort, leaving the live serving state untouched.
func (f *HandlerFactory) Build(c *config.Config, commit bool) (map[string]http.Handler, func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// Resolve secret references (${env:NAME}, ${file:/path}) in place before
	// the runtime reads any credential. Resolved values register with redact
	// for log masking. The admin/on-disk views keep the unresolved references,
	// so secrets never reach the console or the config file (SEC-1).
	if err := config.ExpandSecrets(c); err != nil {
		return nil, nil, fmt.Errorf("secrets: %w", err)
	}
	upstreams := IndexUpstreams(c.Upstreams)

	// Open a staging generation for this build: genRes.Begin stages a new
	// upstream-pool generation, each proxy location's resolvePool stages or
	// reuses its pool through poolReg, and gen.Commit (on success) promotes
	// them and closes pools that were removed or reshaped. The deferred
	// gen.Abort closes freshly created staged pools and any staged handler
	// closers on any non-committing return (preflight or failure) so a rejected
	// reload leaks no goroutines while the previous pools keep serving; it is a
	// no-op once gen.Commit has promoted the generation.
	gen := f.GenRes.Begin()
	defer gen.Abort()

	// Build this generation's WASM plugin set. A lean build (or a malformed
	// module) fails here, rejecting the reload. The set owns per-plugin wazero
	// runtimes; register it for generational teardown so the previous set is
	// closed only after the new handlers are live.
	pluginSet, err := f.PluginMgr.Build(c.Plugins)
	if err != nil {
		return nil, nil, fmt.Errorf("plugins: %w", err)
	}
	gen.Stage(pluginSet)

	withCache := func(loc config.LocationConfig, h http.Handler) http.Handler {
		if loc.Cache && f.Cache != nil {
			return f.Cache.Handler(h)
		}
		return h
	}

	// Static locations may serve precompressed .br/.gz sidecars when the
	// compression feature has precompressed enabled.
	var staticOpts handler.StaticOptions
	if c.Compression.Enabled && c.Compression.Precompressed {
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
				return handler.NewGRPCProxy(srv, loc, upstreams, f.PoolReg, f.Log, f.Metrics.ObserveGRPCProxyStream)
			}
			h, err := handler.NewProxy(srv, loc, upstreams, f.PoolReg, f.Log)
			if err != nil {
				return nil, err
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
			h, err := handler.NewGRPCTranscode(srv, loc, upstreams, f.PoolReg, f.Log, f.Metrics.ObserveGRPCTranscode, f.Metrics.ObserveGRPCTranscodeStreamMsg)
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
			a, err := auth.New(*loc.Auth, auth.Options{
				Logger:      f.Log,
				OnDecision:  f.Metrics.ObserveAuthDecision,
				DialContext: f.EgressDial,
			})
			if err != nil {
				return nil, nil, fmt.Errorf("location %s: %w", key, err)
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
			fw, err := waf.New(wcfg, waf.Options{
				Logger: f.Log,
				Hooks:  waf.Hooks{OnEvent: f.Metrics.ObserveWAFEvent},
			})
			if err != nil {
				return nil, nil, fmt.Errorf("location %s: %w", WAFScope(c.Servers[i], loc), err)
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
			cc = middleware.ClientCert(loc.RequireClientCert)
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
		return nil, nil, err
	}

	// Compression is built once per reload and shared across listeners. It
	// is the innermost middleware so Metrics and the access log observe the
	// bytes actually sent on the wire. A configured encoder that is not
	// compiled into this build fails the reload with a clear error.
	var compress middleware.Middleware
	if c.Compression.Enabled {
		cm, err := middleware.NewCompression(middleware.CompressionOptions{
			Encoders:   c.Compression.Encoders,
			Level:      c.Compression.Level,
			MinSize:    c.Compression.MinSize.Bytes(),
			Types:      c.Compression.Types,
			OnCompress: f.Metrics.ObserveCompression,
		})
		if err != nil {
			return nil, nil, err
		}
		compress = cm
	}

	handlers := make(map[string]http.Handler)
	for _, addr := range UniqueListenAddrs(c.Servers) {
		// Global middleware chain, outermost to innermost:
		//   RequestID  — assigns/propagates the request id first so every
		//                inner layer (observers, Recover's panic log) sees it.
		//   Tracing    — starts the server span and extracts W3C tracecontext
		//                just inside RequestID, so the trace id is in context
		//                before the observers record it. No-op unless built
		//                with the "otel" tag and enabled.
		//   metrics    — observer: counts requests + latency by final status.
		//   AccessLog  — observer: builds one record per request and fans it
		//                out to the configured sinks (stdout/file/syslog).
		//   Recover    — converts a panic into a 500. It sits INSIDE the
		//                observers on purpose: it returns normally after
		//                recovering, so metrics and the access log still
		//                record the request as a 500 instead of it vanishing.
		//   Compression— innermost wrapper so the observers' recorder counts
		//                the compressed (on-the-wire) byte count.
		// Per-location concerns (rate limiting, body limit) are applied inside
		// the router via LocationModifier, closer to the handler.
		mws := []middleware.Middleware{
			middleware.RequestID(),
			f.RT.Tracer.Middleware,
			f.Metrics.Middleware,
			middleware.AccessLog(f.AccessSinks...),
			middleware.Recover(f.Log),
		}
		if compress != nil {
			mws = append(mws, compress)
		}
		h := middleware.Chain(rtr.For(addr), mws...)
		// On plain HTTP listeners, answer ACME HTTP-01 challenges outermost so
		// certificate issuance/renewal works even when the listener otherwise
		// redirects to HTTPS. Non-challenge requests fall through to h.
		if f.RT.ACME != nil && !AddrServesTLS(c.Servers, addr) {
			h = f.RT.ACME.ChallengeHandler(h)
		}
		handlers[addr] = h
	}

	// The build succeeded. A committing build promotes the staged pools and
	// closes any pools the previous generation no longer needs (the deferred
	// gen.Abort becomes a no-op), then adopts this generation's handler closers
	// as the live set and returns a retire callback that closes the PREVIOUS
	// generation's closers. The server invokes that callback only after the
	// previous generation has drained, so gRPC backend connections, WASM plugin
	// runtimes, and static-file directory handles are never closed while an
	// in-flight request is still served by the old handlers. A preflight build
	// (commit == false) never calls gen.Commit, so the deferred gen.Abort
	// releases the staged pools and closers and the live generation is
	// untouched; the returned handlers and nil retire are discarded.
	var retirePrev func()
	if commit {
		retirePrev = gen.Commit()
	}
	return handlers, retirePrev, nil
}
