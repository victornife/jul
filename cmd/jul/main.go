// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Command jul is an NGINX-inspired HTTP edge server configured via TOML.
//
// serve() has four logical sections:
//  1. Init         — logging, secrets, cache, metrics, tracing, ACME,
//     stream proxy, WAF, rate limiter, upstream registry,
//     plugin manager.
//  2. buildHandlers — per-reload handler tree (static, proxy, FastCGI,
//     gRPC transcoding, plugins) with middleware chain.
//  3. Preflight     — admin-write validation gate (6-gate sequence via
//     app.Preflight.Apply: validate → TLS → handler dry-run
//     → stream dry-run → bind probes → restart checks).
//  4. Admin deps    — web-console wiring (app.BuildAdminDeps) and listener
//     startup.
//
// Pure wiring helpers (scope keys, indexing, preflight, admin deps) live in
// internal/app/ so they can be unit-tested without a full process boot
// (see docs/architecture.md#composition-root-helpers).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/admin"
	"jul/internal/app"
	"jul/internal/atomicfile"
	"jul/internal/auth"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/handler"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/redact"
	"jul/internal/router"
	"jul/internal/server"
	"jul/internal/signals"
	"jul/internal/stream"
	"jul/internal/upstream"
	"jul/internal/waf"
)

// version is overridable at build time via -ldflags "-X main.version=...".
var version = "unknown (build without -ldflags)"

// productName is the commercial product name shown to users.
const productName = "Jul.IA"

func main() {
	// When launched by the Windows Service Control Manager, run under the
	// service protocol; otherwise fall through to normal foreground execution.
	if handled, code := runService(); handled {
		os.Exit(code)
	}
	os.Exit(run())
}

func run() int {
	// Subcommands (lint/fmt/run) are additive; when none matches we fall back to
	// the original flag-based behavior so existing invocations are unchanged.
	if handled, code := dispatchSubcommand(os.Args[1:]); handled {
		return code
	}

	var (
		configPath  string
		checkOnly   bool
		showVersion bool
	)
	flag.Usage = usage
	flag.StringVar(&configPath, "config", "server.toml", "path to the TOML configuration file")
	flag.BoolVar(&checkOnly, "check", false, "validate the configuration and exit")
	flag.BoolVar(&showVersion, "version", false, "print version and exit")
	flag.Parse()

	if showVersion {
		fmt.Printf("%s %s\n", productName, version)
		return 0
	}

	src := config.NewTOMLSource(configPath)
	cfg, err := src.Load()
	if err != nil {
		// A missing config file on a bare `jul` is the most common first-run
		// stumble; point the operator at zero-config mode and the docs instead of
		// only surfacing a raw open error.
		if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
			fmt.Fprintf(os.Stderr, "error: no configuration file at %q\n\n", configPath)
			fmt.Fprintln(os.Stderr, "Start without a config file using zero-config mode:")
			fmt.Fprintln(os.Stderr, "  jul run --serve .              # serve the current directory")
			fmt.Fprintln(os.Stderr, "  jul run --proxy http://:3000   # reverse-proxy a local app")
			fmt.Fprintln(os.Stderr, "\nOr create a server.toml and run `jul`. See `jul --help` and docs/getting-started.md.")
			return 1
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid configuration in %s:\n%v\n", src.Name(), err)
		return 1
	}
	if checkOnly {
		fmt.Printf("configuration %s is valid\n", src.Name())
		return 0
	}

	ctx, reloadSig, stop := signals.Listen(context.Background())
	defer stop()
	return serve(ctx, reloadSig, src, cfg)
}

// serve builds the runtime from a validated configuration and runs until
// baseCtx is cancelled. The base context and reload signal are supplied by the
// caller (console signal handling, or the Windows service control manager).
func serve(baseCtx context.Context, sigReload <-chan struct{}, src config.Source, cfg *config.Config) int {
	// Wrap the log sink so any secret value resolved from a ${env:}/${file:}
	// reference (SEC-1) is masked even if a message or attribute interpolates it.
	log := observability.NewLogger(redact.Writer(os.Stderr), cfg.Global.LogLevel, cfg.Global.LogFormat)
	log.Info("starting "+productName, "version", version, "config", src.Name())

	// Resolve secret references (${env:NAME}, ${file:/path}) once, up front, so
	// every one-time consumer below (admin token, ACME account, tracing) reads
	// resolved credentials. The handler factory re-resolves on each reload; the
	// admin/on-disk views keep the unresolved references (SEC-1).
	if err := config.ExpandSecrets(cfg); err != nil {
		log.Error("failed to resolve secret references", "error", err)
		return 1
	}

	// The response cache persists across reloads, so it is created once and
	// captured by the handler factory below.
	responseCache, err := cache.New(cfg.Cache, log)
	if err != nil {
		log.Error("failed to initialize cache", "error", err)
		return 1
	}

	// Metrics persist across reloads so counters are not reset on config edits.
	// The host label is read once at startup (like tracing): changing
	// [observability.metrics] takes effect only after a restart.
	metrics := observability.NewMetrics(observability.WithHostLabel(cfg.Observability.Metrics.HostLabel))

	// Tracing is initialized once at startup (like ACME): the OTLP pipeline and
	// global TracerProvider are fixed for the process, so changing
	// [observability.tracing] takes effect only after a restart. It is a no-op
	// unless enabled and built with the "otel" tag; an enabled block in a binary
	// without that tag is a startup error. Shutdown flushes buffered spans on
	// graceful exit.
	tracer, err := observability.NewTracer(cfg.Observability.Tracing)
	if err != nil {
		log.Error("failed to initialize tracing", "error", err)
		return 1
	}
	defer func() {
		shutCtx, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShut()
		if err := tracer.Shutdown(shutCtx); err != nil {
			log.Warn("tracing shutdown", "error", err)
		}
	}()

	// Access-log sinks are built once at startup (like tracing): the "file" sink
	// owns a rotating file handle and the "syslog" sink a system-log connection,
	// so they persist across reloads and are closed on shutdown. Changing
	// [observability.access_log] takes effect only after a restart. The default
	// set is the "stdout" sink, preserving the structured access line in the log.
	accessSinks, accessClosers, err := observability.BuildAccessSinks(cfg.Observability.AccessLog, log)
	if err != nil {
		log.Error("failed to initialize access log", "error", err)
		return 1
	}
	defer func() {
		for _, c := range accessClosers {
			_ = c.Close()
		}
	}()

	// The Console Operations Log tail (Phase 4g) is an extra access-log sink: a
	// bounded, privacy-preserving in-memory ring buffer that also fans new
	// entries out to live SSE followers. It is added to every traffic listener's
	// access-log chain below and exposed to the admin server via deps.
	logTail := observability.NewLogTail(0)
	accessSinks = append(accessSinks, logTail)

	// Build the ACME manager once from the startup configuration. It covers the
	// union of acme-enabled domains, caches certificates on disk, auto-renews,
	// and answers HTTP-01 challenges. nil means no block enables ACME; an error
	// means ACME is enabled but this binary lacks the "acme" build tag. Enabling
	// ACME after startup requires a restart (the domain set is fixed here).
	acmeMgr, err := server.NewACMEManager(cfg.Servers, metrics.ObserveCertExpiry)
	if err != nil {
		log.Error("failed to initialize ACME", "error", err)
		return 1
	}

	// HTTP/3 support is a build-time choice (the "http3" tag). Fail fast at
	// startup when the configuration enables HTTP/3 but this binary cannot serve
	// it, mirroring the ACME/tracing build-tag checks, instead of silently
	// serving only TCP. It is a no-op in http3-enabled builds.
	if err := server.CheckHTTP3(cfg.Servers); err != nil {
		log.Error("failed to initialize HTTP/3", "error", err)
		return 1
	}

	// L4 stream proxying ([[stream]]) is a build-time choice (the "stream" tag).
	// Fail fast when the configuration declares a stream but this binary cannot
	// serve it, mirroring the HTTP/3 check. It is a no-op in stream-enabled
	// builds or when no stream is configured.
	if err := stream.Check(cfg.Streams); err != nil {
		log.Error("failed to initialize stream proxy", "error", err)
		return 1
	}

	// The web application firewall ([waf]) is a build-time choice (the "waf"
	// tag). Fail fast when the configuration enables it but this binary cannot
	// enforce rules, mirroring the stream/HTTP3 checks. It is a no-op in
	// WAF-enabled builds or when no WAF is configured.
	if err := waf.Check(cfg); err != nil {
		log.Error("failed to initialize WAF", "error", err)
		return 1
	}

	// The rate-limiter store persists across reloads (its janitor is bound to
	// baseCtx) so token buckets and their accumulated state survive config
	// edits. Reloads update each bucket's rate/burst in place via a stable scope.
	rlStore := middleware.NewRateLimiterStore(baseCtx, 0, 0)

	// The upstream pool registry persists across reloads so named-upstream pools
	// (and their active health-check goroutines) have a defined lifetime: pools
	// are reused and updated in place when unchanged, and closed when removed or
	// reshaped. CloseAll stops every checker goroutine on shutdown.
	poolReg := upstream.NewRegistry(upstream.RegistryOptions{
		Logger:           log,
		OnHealth:         metrics.ObserveBackendHealth,
		OnProbe:          metrics.ObserveProbe,
		OnBackends:       metrics.ObserveUpstreamBackends,
		OnDiscoveryError: metrics.ObserveDiscoveryError,
	})
	defer poolReg.CloseAll()

	// The WASM plugin manager persists across reloads so the compilation cache
	// (and the key/value store) survive config edits: an unchanged module is
	// recompiled from cache cheaply, and KV state is retained. Each reload builds
	// a fresh plugin Set; the previous Set is closed via the generational handler
	// closers below. In a lean build (no "wasmplugins" tag) the manager is a
	// no-op and Build rejects any configured plugin, failing the reload clearly.
	pluginMgr, err := plugins.NewManager(plugins.Options{
		Logger:       log,
		OnInvocation: metrics.ObservePluginInvocation,
		OnPanic:      metrics.ObservePluginPanic,
	})
	if err != nil {
		log.Error("failed to initialize plugin manager", "error", err)
		return 1
	}
	defer func() { _ = pluginMgr.Close() }()

	// The L4 stream proxy persists across reloads so its listeners (and any
	// in-flight relayed connections) survive config edits: a reload diffs the
	// desired stream set against the running one. In a lean build (no "stream"
	// tag) the server is a no-op and Reload rejects any configured stream. The
	// initial set is applied below before serving; subsequent reloads are driven
	// by the server's OnReloaded hook.
	streamSrv := stream.NewServer(stream.Options{
		Logger: log,
		Hooks: stream.Hooks{
			OnConnDelta:          metrics.StreamConnDelta,
			OnBytes:              metrics.ObserveStreamBytes,
			OnUDPSessionEvicted:  metrics.StreamUDPEvicted,
			OnUDPSessionRejected: metrics.StreamUDPRejected,
		},
	})
	defer func() { _ = streamSrv.Close() }()
	if err := streamSrv.Reload(cfg.Streams, app.IndexUpstreams(cfg.Upstreams)); err != nil {
		log.Error("failed to start stream proxy", "error", err)
		return 1
	}
	// lastStreamReload publishes the outcome of the most recent stream-proxy
	// reload for the console Overview. The stream listener set is reloaded
	// asynchronously after the HTTP reload swap (OnReloaded), so its success or
	// failure cannot be reported in the synchronous apply response; the console
	// surfaces it by polling. Empty means no stream is configured (nothing to
	// report); "ok" means the running stream set matches the applied config; a
	// "failed: ..." value means the last reload was rejected and the previously
	// bound listeners are still serving the prior set.
	var lastStreamReload atomic.Pointer[string]
	{
		initial := ""
		if len(cfg.Streams) > 0 {
			initial = "ok"
		}
		lastStreamReload.Store(&initial)
	}

	// liveHandlerClosers holds closers for handlers in the currently serving
	// configuration that own resources needing explicit teardown on reload —
	// gRPC-transcoding backend connections, WASM plugin runtimes, and static
	// file directory handles. A successful reload adopts the new generation's
	// closers here and hands the previous generation's closers to the server as a
	// retire callback, which runs only after that generation's in-flight requests
	// drain; a failed reload closes only the rejected generation's staged
	// closers. The deferred close on shutdown tears down the final generation
	// still serving.
	var liveHandlerClosers []io.Closer
	defer func() {
		for _, c := range liveHandlerClosers {
			_ = c.Close()
		}
	}()

	// buildMu serializes every handler-tree build (startup, reload, and admin
	// preflight) so the generational pool registry's Begin..Commit span is never
	// interleaved by a concurrent build. Request serving never takes this lock —
	// handlers are swapped atomically — so it only gates the (infrequent) builds.
	var buildMu sync.Mutex

	// buildHandlers rebuilds the per-listen-address handler tree from a config.
	// It is invoked at startup and on every reload, so all routing state (router,
	// upstream pools, content handlers) is reconstructed atomically. When commit
	// is true the freshly built generation is promoted (staged pools committed,
	// the previous generation's handler closers torn down); when false the build
	// is a throwaway preflight whose staged pools and closers are released by the
	// deferred abort, leaving the live serving state untouched.
	buildHandlers := func(c *config.Config, commit bool) (map[string]http.Handler, func(), error) {
		buildMu.Lock()
		defer buildMu.Unlock()

		// Resolve secret references (${env:NAME}, ${file:/path}) in place before
		// the runtime reads any credential. Resolved values register with redact
		// for log masking. The admin/on-disk views keep the unresolved references,
		// so secrets never reach the console or the config file (SEC-1).
		if err := config.ExpandSecrets(c); err != nil {
			return nil, nil, fmt.Errorf("secrets: %w", err)
		}
		upstreams := app.IndexUpstreams(c.Upstreams)

		// Reconcile the upstream pool set for this build: Begin stages a new
		// generation, each proxy location's resolvePool stages or reuses its
		// pool through poolReg, and Commit (on success) promotes them and closes
		// pools that were removed or reshaped. Abort on any failure closes
		// freshly created staged pools so a rejected reload leaks no goroutines
		// while the previous pools keep serving.
		poolReg.Begin()
		committed := false
		// stagedHandlerClosers collects closers for handlers built in THIS
		// generation. On success they become the live set and the previous live
		// set is returned as a retire callback (closed after it drains); on
		// failure they are closed here so a rejected reload leaks no backend
		// connections.
		var stagedHandlerClosers []io.Closer
		defer func() {
			if !committed {
				poolReg.Abort()
				for _, c := range stagedHandlerClosers {
					_ = c.Close()
				}
			}
		}()

		// Build this generation's WASM plugin set. A lean build (or a malformed
		// module) fails here, rejecting the reload. The set owns per-plugin wazero
		// runtimes; register it for generational teardown so the previous set is
		// closed only after the new handlers are live.
		pluginSet, err := pluginMgr.Build(c.Plugins)
		if err != nil {
			return nil, nil, fmt.Errorf("plugins: %w", err)
		}
		stagedHandlerClosers = append(stagedHandlerClosers, pluginSet)

		withCache := func(loc config.LocationConfig, h http.Handler) http.Handler {
			if loc.Cache && responseCache != nil {
				return responseCache.Handler(h)
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
					stagedHandlerClosers = append(stagedHandlerClosers, c)
				}
				return h, nil
			},
			router.ActionProxy: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
				// grpc = true forwards native gRPC over end-to-end HTTP/2
				// (trailers preserved, no buffering); otherwise it is a plain
				// HTTP reverse proxy. gRPC passthrough is not cacheable.
				if loc.GRPC {
					return handler.NewGRPCProxy(srv, loc, upstreams, poolReg, log, metrics.ObserveGRPCProxyStream)
				}
				h, err := handler.NewProxy(srv, loc, upstreams, poolReg, log)
				if err != nil {
					return nil, err
				}
				return withCache(loc, h), nil
			},
			router.ActionFastCGI: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
				h, err := handler.NewFastCGI(srv, loc, log)
				if err != nil {
					return nil, err
				}
				return withCache(loc, h), nil
			},
			router.ActionGRPCTranscode: func(srv config.ServerConfig, loc config.LocationConfig) (http.Handler, error) {
				h, err := handler.NewGRPCTranscode(srv, loc, upstreams, poolReg, log, metrics.ObserveGRPCTranscode, metrics.ObserveGRPCTranscodeStreamMsg)
				if err != nil {
					return nil, err
				}
				// The transcoder owns a gRPC client connection; register it for
				// teardown when this configuration is replaced.
				if c, ok := h.(io.Closer); ok {
					stagedHandlerClosers = append(stagedHandlerClosers, c)
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
			kind := app.RateKeyKind(rl.Key)
			lim := rlStore.Scoped(scope, rl.Rate, rl.Burst)
			return middleware.RateLimit(lim, middleware.RateKeyFunc(rl.Key), func() {
				metrics.ObserveRateLimited(kind)
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
				key := app.AuthScope(c.Servers[i], loc)
				a, err := auth.New(*loc.Auth, auth.Options{
					Logger:     log,
					OnDecision: metrics.ObserveAuthDecision,
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
			if a := authByScope[app.AuthScope(srv, loc)]; a != nil {
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
				wcfg, ok := app.EffectiveWAF(c, loc)
				if !ok {
					continue
				}
				fw, err := waf.New(wcfg, waf.Options{
					Logger: log,
					Hooks:  waf.Hooks{OnEvent: metrics.ObserveWAFEvent},
				})
				if err != nil {
					return nil, nil, fmt.Errorf("location %s: %w", app.WAFScope(c.Servers[i], loc), err)
				}
				wafByScope[app.WAFScope(c.Servers[i], loc)] = fw
			}
		}
		locWAF := func(srv config.ServerConfig, loc config.LocationConfig) middleware.Middleware {
			fw := wafByScope[app.WAFScope(srv, loc)]
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

		rt, err := router.New(c, builders, nil, locModifier, log)
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
				OnCompress: metrics.ObserveCompression,
			})
			if err != nil {
				return nil, nil, err
			}
			compress = cm
		}

		handlers := make(map[string]http.Handler)
		for _, addr := range app.UniqueListenAddrs(c.Servers) {
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
				tracer.Middleware,
				metrics.Middleware,
				middleware.AccessLog(accessSinks...),
				middleware.Recover(log),
			}
			if compress != nil {
				mws = append(mws, compress)
			}
			h := middleware.Chain(rt.For(addr), mws...)
			// On plain HTTP listeners, answer ACME HTTP-01 challenges outermost so
			// certificate issuance/renewal works even when the listener otherwise
			// redirects to HTTPS. Non-challenge requests fall through to h.
			if acmeMgr != nil && !app.AddrServesTLS(c.Servers, addr) {
				h = acmeMgr.ChallengeHandler(h)
			}
			handlers[addr] = h
		}

		// The build succeeded. A committing build promotes the staged pools and
		// closes any pools the previous generation no longer needs (the deferred
		// Abort becomes a no-op), then adopts this generation's handler closers as
		// the live set and returns a retire callback that closes the PREVIOUS
		// generation's closers. The server invokes that callback only after the
		// previous generation has drained, so gRPC backend connections, WASM plugin
		// runtimes, and static-file directory handles are never closed while an
		// in-flight request is still served by the old handlers. A preflight build
		// (commit == false) leaves committed false, so the deferred Abort releases
		// the staged pools and closers and the live generation is untouched; the
		// returned handlers and nil retire are discarded.
		var retirePrev func()
		if commit {
			poolReg.Commit()
			prevClosers := liveHandlerClosers
			liveHandlerClosers = stagedHandlerClosers
			committed = true
			retirePrev = func() {
				for _, c := range prevClosers {
					_ = c.Close()
				}
			}
		}
		return handlers, retirePrev, nil
	}

	// factory adapts buildHandlers to the server's reload hook: a reload always
	// commits the new generation.
	factory := func(c *config.Config) (map[string]http.Handler, func(), error) {
		return buildHandlers(c, true)
	}

	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	// adminReload lets the admin /reload endpoint trigger a reload through the
	// same path as SIGHUP and file-watch events.
	adminReload := make(chan struct{}, 1)
	// Only file-backed sources can be watched; zero-config (in-memory) sources
	// have no file on disk, so file-watching is skipped for them.
	var fileWatch <-chan struct{}
	if ts, ok := src.(*config.TOMLSource); ok {
		fileWatch = watchConfig(ctx, ts.Path, log)
	}
	// Merge SIGHUP (when present), config file-watch, and admin-triggered reloads.
	reload := app.MergeReload(ctx, sigReload, fileWatch, adminReload)

	triggerReload := func() {
		select {
		case adminReload <- struct{}{}:
		default:
		}
	}

	var readyFlag admin.Readiness

	// Configuration GUI wiring. File read/write is only available when the
	// source is a TOML file on disk; other sources leave these deps nil and the
	// GUI degrades to read-only / disabled accordingly.
	subsystems := app.Subsystems{
		ResponseCache:    responseCache,
		Metrics:          metrics,
		PoolReg:          poolReg,
		LogTail:          logTail,
		PluginsCompiled:  plugins.Compiled,
		StreamCompiled:   stream.Compiled,
		WAFCompiled:      waf.Compiled,
		LastStreamReload: &lastStreamReload,
	}
	deps := app.BuildAdminDeps(productName, version, src, subsystems)
	deps.Reload = triggerReload
	deps.Ready = readyFlag.Ready
	deps.LoadConfig = src.Load
	deps.TrafficSources = metrics.TrafficSnapshot

	// preflight is the truthfulness gate for every config write: a config that
	// passes it is guaranteed to build, so the subsequent asynchronous reload
	// cannot fail for configuration reasons and "applied" stays honest. It runs the
	// cheap structural + stateless validation first (validateRuntimeConfig also
	// gates WAF config in lean builds), then preflight.Apply, which dry-runs the
	// ENTIRE composition root — the same factory the reload uses — on a clone:
	// plugins, static roots, proxy/gRPC/FastCGI handlers, auth, WAF, router and
	// compression. Any error (or panic) aborts the write before the file is
	// persisted, instead of surfacing only at the reload where the old runtime
	// keeps serving while audit/history have already recorded success.
	preflight := app.Preflight{
		BuildHandlers: buildHandlers,
		Stream:        streamSrv,
	}
	if ts, ok := src.(*config.TOMLSource); ok {
		path := ts.Path
		deps.ReadConfigRaw = func() ([]byte, error) { return os.ReadFile(path) }
		deps.WriteConfigRaw = func(data []byte) error {
			cfg, err := config.Parse(data)
			if err != nil {
				return err
			}
			// Compare the candidate against the running (on-disk) config to gate
			// changes that the asynchronous reload would otherwise apply
			// best-effort and only log on failure.
			var prevCfg *config.Config
			if prevData, rerr := os.ReadFile(path); rerr == nil {
				if prev, perr := config.Parse(prevData); perr == nil {
					prevCfg = prev
				}
			}
			if err := preflight.Apply(cfg, prevCfg); err != nil {
				return err
			}
			// Write the validated config atomically with a secure default mode.
			// A new config file is created 0o600 (it may hold inline credentials);
			// an existing file's mode is preserved, and a crash mid-write leaves the
			// previously serving file intact rather than a truncated one.
			if err := atomicfile.Write(path, data, 0o600); err != nil {
				return err
			}
			triggerReload()
			return nil
		}
		deps.SaveConfig = func(c *config.Config) error {
			if err := preflight.Apply(c, nil); err != nil {
				return err
			}
			data, err := config.Marshal(c)
			if err != nil {
				return err
			}
			if err := atomicfile.Write(path, data, 0o600); err != nil {
				return err
			}
			triggerReload()
			return nil
		}
	}

	// Start the admin listener when enabled.
	if adminSrv := admin.New(cfg.Admin, log, deps); adminSrv != nil {
		go func() {
			if err := adminSrv.Run(ctx); err != nil {
				log.Error("admin listener failed", "error", err)
			}
		}()
	}

	readyFlag.Set(true)

	srv := server.New(cfg, log, factory, src, validateRuntimeConfig)
	srv.ConnStateHook = metrics.ConnState
	srv.ACME = acmeMgr
	deps.LastReload = func() *admin.ReloadSnapshot {
		if li := srv.LastReload(); li != nil {
			return &admin.ReloadSnapshot{
				OK:       li.OK,
				TimedOut: li.TimedOut,
				Duration: li.Duration,
				At:       li.At,
				Error:    li.Error,
			}
		}
		return nil
	}
	srv.HTTP3ConnHook = metrics.HTTP3ConnDelta
	srv.MTLSResultHook = metrics.ObserveMTLSHandshake
	// Drive L4 stream-proxy reloads from the same validated config as the HTTP
	// listeners. Stream binding errors are logged and do not roll back the HTTP
	// reload (the listener sets are independent).
	srv.OnReloaded = func(c *config.Config) {
		if err := streamSrv.Reload(c.Streams, app.IndexUpstreams(c.Upstreams)); err != nil {
			log.Error("stream proxy reload failed", "error", err)
			msg := "failed: " + err.Error()
			lastStreamReload.Store(&msg)
			return
		}
		ok := ""
		if len(c.Streams) > 0 {
			ok = "ok"
		}
		lastStreamReload.Store(&ok)
	}
	if err := srv.Run(ctx, reload); err != nil {
		log.Error("server exited with error", "error", err)
		return 1
	}
	return 0
}

// watchConfig starts a debounced file watcher for the config path, returning a
// reload channel. On failure it logs and returns nil (file-watch disabled).
func watchConfig(ctx context.Context, path string, log *slog.Logger) <-chan struct{} {
	ch, err := config.WatchFile(ctx, path, 300*time.Millisecond, log)
	if err != nil {
		log.Warn("config file watch disabled", "error", err)
		return nil
	}
	return ch
}

// validateRuntimeConfig performs a deep preflight of the configuration:
// clones, expands secret references, validates structurally, and then
// dry-runs every runtime component that could fail during serve/reload
// (WAF rule compilation, auth initialisation, etc.).  The original config
// is never modified.
func validateRuntimeConfig(c *config.Config) error {
	return app.ValidateRuntimeConfig(c)
}
