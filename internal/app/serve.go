// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"jul/internal/admin"
	"jul/internal/atomicfile"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/egress"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/redact"
	"jul/internal/server"
	"jul/internal/stream"
	"jul/internal/upstream"
	"jul/internal/waf"
)

// Serve builds the runtime from a validated configuration and runs until
// baseCtx is cancelled. It is the composition root for the Jul.IA server:
//
//  1. Init — logging, secrets, cache, metrics, and the process-lifetime
//     runtime subsystems (tracing, ACME, stream server, build-tag feature
//     gates) built once via RuntimeBuilder, plus the rate limiter, upstream
//     registry, plugin manager, and generational teardown resources.
//  2. HandlerFactory — per-reload handler tree (static, proxy, FastCGI,
//     gRPC transcoding, plugins) with middleware chain, whose generational
//     teardown lifecycle is owned by GenerationResources.
//  3. Preflight — admin-write validation gate (6-gate sequence via
//     Preflight.Apply: validate → TLS → handler dry-run → stream dry-run
//     → bind probes → restart checks).
//  4. Admin deps — web-console wiring (BuildAdminDeps) and listener startup.
//
// productName and version are shown in startup logs and the admin console.
func Serve(baseCtx context.Context, sigReload <-chan struct{}, src config.Source, cfg *config.Config, productName, version string) int {
	// ── Section 1: Init ────────────────────────────────────────────────────
	//
	// Wrap the log sink so any secret value resolved from a ${env:}/${file:}
	// reference (SEC-1) is masked even if a message or attribute interpolates it.
	log := observability.NewLogger(redact.Writer(os.Stderr), cfg.Global.LogLevel, cfg.Global.LogFormat)
	log.Info("starting "+productName, "version", version, "config", src.Name())

	// Clone the raw startup config before secret expansion so PendingRestartCheck
	// can compare effective startup-time values against the current on-disk config.
	// Both use the same unexpanded references for structural fields (cache, egress,
	// admin) so secret-ref changes do not mask real structural differences.
	var startupCfg *config.Config
	if clone, cerr := cfg.Clone(); cerr == nil {
		startupCfg = clone
	}

	// Resolve secret references (${env:NAME}, ${file:/path}) once, up front,
	// so every one-time consumer below (admin token, ACME account, tracing)
	// reads resolved credentials. The handler factory re-resolves on each
	// reload; the admin/on-disk views keep the unresolved references (SEC-1).
	if err := config.ExpandSecrets(cfg); err != nil {
		log.Error("failed to resolve secret references", "error", err)
		return 1
	}

	// The response cache persists across reloads so counters and LRU state
	// survive config edits. Created once and captured by the handler factory.
	responseCache, err := cache.New(cfg.Cache, log)
	if err != nil {
		log.Error("failed to initialize cache", "error", err)
		return 1
	}

	// Metrics persist across reloads so counters are not reset on config edits.
	metrics := observability.NewMetrics(observability.WithHostLabel(cfg.Observability.Metrics.HostLabel))

	// The process-lifetime runtime subsystems (tracing, ACME, stream server,
	// build-tag feature gates) are built once at startup and outlive every
	// reload. rt.Close (deferred) shuts them down on graceful exit.
	rt, err := RuntimeBuilder{Config: cfg, Logger: log, Metrics: metrics}.Build()
	if err != nil {
		log.Error("failed to initialize server runtime", "error", err)
		return 1
	}
	defer rt.Close()

	// Access-log sinks are built once at startup: the "file" sink owns a
	// rotating file handle and the "syslog" sink a system-log connection.
	// Changing [observability.access_log] takes effect only after a restart.
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

	// The Console Operations Log tail is an extra access-log sink: a bounded,
	// privacy-preserving in-memory ring buffer that fans new entries to live
	// SSE followers.
	logTail := observability.NewLogTail(0)
	accessSinks = append(accessSinks, logTail)

	// The rate-limiter store persists across reloads (its janitor is bound to
	// baseCtx) so token buckets survive config edits.
	rlStore := middleware.NewRateLimiterStore(baseCtx, 0, 0)

	// The optional egress allow-list guards the server's config-driven
	// auxiliary fetches (JWKS, forward-auth, Consul/Kubernetes discovery).
	// Changing [egress] takes effect after a restart.
	egressPolicy, err := egress.New(cfg.Egress)
	if err != nil {
		log.Error("failed to build egress allow-list", "error", err)
		return 1
	}
	egressDial := egressPolicy.DialContext(&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})

	// The upstream pool registry persists across reloads so named-upstream
	// pools (and their health-check goroutines) have a defined lifetime.
	poolReg := upstream.NewRegistry(upstream.RegistryOptions{
		Logger:           log,
		OnHealth:         metrics.ObserveBackendHealth,
		OnProbe:          metrics.ObserveProbe,
		OnBackends:       metrics.ObserveUpstreamBackends,
		OnDiscoveryError: metrics.ObserveDiscoveryError,
		DialContext:      egressDial,
	})
	defer poolReg.CloseAll()

	// The WASM plugin manager persists across reloads so the compilation
	// cache and KV store survive config edits.
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

	// genRes owns the generational teardown lifecycle of the handler tree.
	genRes := NewGenerationResources(poolReg)
	defer genRes.CloseLive()

	// ── Section 2: HandlerFactory ──────────────────────────────────────────
	//
	// The factory holds the process-lifetime dependencies and rebuilds the
	// per-listen-address handler tree on every reload. See factory.go.
	f := &HandlerFactory{
		Log:         log,
		Metrics:     metrics,
		Cache:       responseCache,
		AccessSinks: accessSinks,
		RLStore:     rlStore,
		EgressDial:  egressDial,
		PoolReg:     poolReg,
		PluginMgr:   pluginMgr,
		GenRes:      genRes,
		RT:          rt,
	}

	// factory adapts HandlerFactory.Build to the server reload hook: a live
	// reload always commits the new generation.
	factory := func(c *config.Config) (map[string]http.Handler, func(), error) {
		return f.Build(c, true)
	}

	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	// adminReload lets the admin /reload endpoint trigger a reload through the
	// same path as SIGHUP and file-watch events.
	adminReload := make(chan struct{}, 1)
	triggerReload := func() {
		select {
		case adminReload <- struct{}{}:
		default:
		}
	}

	// Only file-backed sources can be watched; zero-config (in-memory) sources
	// have no file on disk, so file-watching is skipped for them.
	var fileWatch <-chan struct{}
	if ts, ok := src.(*config.TOMLSource); ok {
		fileWatch = watchConfig(ctx, ts.Path, log)
	}
	// Merge SIGHUP (when present), config file-watch, and admin-triggered
	// reloads into a single channel.
	reload := MergeReload(ctx, sigReload, fileWatch, adminReload)

	// ── Section 3: Preflight ───────────────────────────────────────────────
	//
	// preflight is the truthfulness gate for every config write: a config
	// that passes it is guaranteed to build, so the asynchronous reload cannot
	// fail for configuration reasons. See preflight.go.
	pf := Preflight{
		BuildHandlers: f.Build,
		Stream:        rt.Stream,
	}

	// ── Section 4: Admin deps ──────────────────────────────────────────────
	//
	// Configuration GUI wiring. File read/write is only available when the
	// source is a TOML file on disk; other sources leave these deps nil and
	// the GUI degrades to read-only / disabled accordingly.
	subsystems := Subsystems{
		ResponseCache:    responseCache,
		Metrics:          metrics,
		PoolReg:          poolReg,
		LogTail:          logTail,
		PluginsCompiled:  plugins.Compiled,
		StreamCompiled:   stream.Compiled,
		WAFCompiled:      waf.Compiled,
		LastStreamReload: &rt.LastStreamReload,
	}
	deps := BuildAdminDeps(productName, version, src, subsystems)
	deps.Reload = triggerReload
	var readyFlag admin.Readiness
	deps.Ready = readyFlag.Ready
	deps.LoadConfig = src.Load
	deps.TrafficSources = metrics.TrafficSnapshot

	if ts, ok := src.(*config.TOMLSource); ok {
		path := ts.Path
		deps.ReadConfigRaw = func() ([]byte, error) { return os.ReadFile(path) }
		deps.WriteConfigRaw = func(data []byte) error {
			cfg, err := config.Parse(data)
			if err != nil {
				return err
			}
			var prevCfg *config.Config
			if prevData, rerr := os.ReadFile(path); rerr == nil {
				if prev, perr := config.Parse(prevData); perr == nil {
					prevCfg = prev
				}
			}
			if err := pf.Apply(cfg, prevCfg); err != nil {
				return err
			}
			if err := atomicfile.Write(path, data, 0o600); err != nil {
				return err
			}
			triggerReload()
			return nil
		}
		deps.SaveConfig = func(c *config.Config) error {
			// Load the current on-disk config as prev so that Preflight.Apply runs
			// all restart-required checks (ACME, listener rebind, tracing, access
			// log) on the structured-patch write path, matching WriteConfigRaw.
			var prevCfg *config.Config
			if prevData, rerr := os.ReadFile(path); rerr == nil {
				if prev, perr := config.Parse(prevData); perr == nil {
					prevCfg = prev
				}
			}
			if err := pf.Apply(c, prevCfg); err != nil {
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

	// Wire PendingRestartCheck using the startup config snapshot. It reports
	// which startup-bound subsystems differ between what we were built from
	// and what is currently on disk, so the Console can show a persistent
	// "restart required" banner when saved changes are not yet active.
	if startupCfg != nil && deps.LoadConfig != nil {
		loadFn := deps.LoadConfig
		deps.PendingRestartCheck = func() []string {
			current, err := loadFn()
			if err != nil || current == nil {
				return nil
			}
			var pending []string
			if _, need := server.CacheRestartRequired(startupCfg, current); need {
				pending = append(pending, "cache")
			}
			if _, need := server.EgressRestartRequired(startupCfg, current); need {
				pending = append(pending, "egress")
			}
			if _, need := server.AdminRestartRequired(startupCfg, current); need {
				pending = append(pending, "admin")
			}
			if _, need := server.MetricsRestartRequired(startupCfg, current); need {
				pending = append(pending, "metrics")
			}
			return pending
		}
	}

	// Construct the server and wire LastReload into deps BEFORE creating the
	// admin server. admin.New copies deps by value, so any callback assigned
	// after that call is invisible to the admin server's apply handlers.
	srv := server.New(cfg, log, factory, src, ValidateRuntimeConfig)
	srv.ConnStateHook = metrics.ConnState
	srv.ACME = rt.ACME
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

	if adminSrv := admin.New(cfg.Admin, log, deps); adminSrv != nil {
		go func() {
			if err := adminSrv.Run(ctx); err != nil {
				log.Error("admin listener failed", "error", err)
			}
		}()
	}

	readyFlag.Set(true)

	srv.HTTP3ConnHook = metrics.HTTP3ConnDelta
	srv.MTLSResultHook = metrics.ObserveMTLSHandshake
	// Drive L4 stream-proxy reloads from the same validated config as the HTTP
	// listeners. Stream binding errors are logged and do not roll back the HTTP
	// reload (the listener sets are independent).
	srv.OnReloaded = func(c *config.Config) {
		if err := rt.Stream.Reload(c.Streams, IndexUpstreams(c.Upstreams)); err != nil {
			log.Error("stream proxy reload failed", "error", err)
			msg := "failed: " + err.Error()
			rt.LastStreamReload.Store(&msg)
			return
		}
		ok := ""
		if len(c.Streams) > 0 {
			ok = "ok"
		}
		rt.LastStreamReload.Store(&ok)
	}
	if err := srv.Run(ctx, reload); err != nil {
		log.Error("server exited with error", "error", err)
		return 1
	}
	return 0
}

// watchConfig starts a debounced file watcher for path, returning a reload
// channel. On failure it logs and returns nil (file-watch disabled).
func watchConfig(ctx context.Context, path string, log *slog.Logger) <-chan struct{} {
	ch, err := config.WatchFile(ctx, path, 300*time.Millisecond, log)
	if err != nil {
		log.Warn("config file watch disabled", "error", err)
		return nil
	}
	return ch
}
