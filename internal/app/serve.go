// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"jul/internal/admin"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/egress"
	"jul/internal/lifecycle"
	"jul/internal/middleware"
	"jul/internal/observability"
	"jul/internal/plugins"
	"jul/internal/rbac"
	"jul/internal/redact"
	"jul/internal/server"
	"jul/internal/stream"
	"jul/internal/upstream"
	"jul/internal/waf"
)

// ServeOption configures optional behaviour of Serve. Existing callers are
// not required to pass any options.
type ServeOption func(*serveOptions)

type serveOptions struct {
	logOutput io.Writer
}

// WithLogOutput directs Serve's log output to w instead of os.Stderr. It is
// used by integration tests that need to inspect startup logs for secret
// leakage without touching the process-wide stderr.
func WithLogOutput(w io.Writer) ServeOption {
	return func(o *serveOptions) {
		o.logOutput = w
	}
}

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
func Serve(baseCtx context.Context, sigReload <-chan struct{}, src config.Source, cfg *config.Config, productName, version string, opts ...ServeOption) int {
	options := serveOptions{logOutput: os.Stderr}
	for _, o := range opts {
		o(&options)
	}

	// ── Section 1: Init ────────────────────────────────────────────────────
	//
	// Wrap the log sink so any secret value resolved from a ${env:}/${file:}
	// reference (SEC-1) is masked even if a message or attribute interpolates it.
	// NewDynamicLogger returns a set-level function for hot-reload of log_level
	// without rebuilding the handler. Log format (text vs json) is restart-bound.
	log, setLogLevel := observability.NewDynamicLogger(redact.Writer(options.logOutput), cfg.Global.LogLevel, cfg.Global.LogFormat)
	log.Info("starting "+productName, "version", version, "config", src.Name())

	// Apply worker_threads at startup. "auto" or empty leaves the Go runtime
	// free to set GOMAXPROCS based on available CPUs (the typical best choice).
	// A positive integer caps parallelism — useful in container environments with
	// fractional CPU allocation.
	if n := parseWorkerThreads(cfg.Global.WorkerThreads); n > 0 {
		runtime.GOMAXPROCS(n)
		log.Info("set worker threads", "gomaxprocs", n)
	}

	// Build the immutable startup candidate: one resolved effective config plus
	// its redaction state and secret digests. Every one-time consumer below
	// (cache, metrics, tracing/ACME/stream runtime, access-log sinks, egress)
	// reads the same secret generation, and the handler factory receives the
	// same effective config without re-resolving (R7-05).
	startupCand, err := config.NewCandidate(cfg)
	if err != nil {
		log.Error("failed to resolve secret references", "error", err)
		return 1
	}
	redact.Install(startupCand.Redaction)

	// Effective config alias for the rest of startup.
	cfg = startupCand.Effective

	// One warning per backend whose verification is disabled. It is a startup
	// log rather than a metric on purpose: an upstream name is unbounded
	// cardinality, so it may appear in a log line but never in a label.
	warnInsecureBackends(log, cfg)

	// The response cache persists across reloads so counters and LRU state
	// survive config edits. Created once and captured by the handler factory.
	responseCache, err := cache.New(cfg.Cache, log)
	if err != nil {
		log.Error("failed to initialize cache", "error", err)
		return 1
	}

	// Metrics persist across reloads so counters are not reset on config edits.
	metrics := observability.NewMetrics(observability.WithHostLabel(cfg.Observability.Metrics.HostLabel))

	// Background cache revalidation reports its bounded outcome through the
	// metrics registry. The cache cannot import observability, so the seam is a
	// plain callback installed here, before any traffic is served.
	responseCache.SetRevalidationObserver(metrics.ObserveCacheRevalidation)

	// The optional egress allow-list guards the server's config-driven auxiliary
	// fetches (JWKS, forward-auth, Consul/Kubernetes discovery, ACME/OCSP PKI
	// calls, and WASM plugin fetches). It is built before the process-lifetime
	// runtime so the ACME/OCSP clients can be guarded. Changing [egress] takes
	// effect after a restart.
	// A rate-limited, secret-free block logger complements the bounded metrics:
	// it names the subsystem, normalized host, optional resolved IP, and reason
	// so an operator can act on a refusal, and rate-limits identical events so a
	// retry loop cannot flood the log.
	egressBlockLog := egress.NewBlockLogObserver(log)
	egressPolicy, err := egress.New(cfg.Egress, egress.WithObserver(func(d egress.Decision) {
		metrics.ObserveEgressDecision(d.Subsystem, string(d.Result), string(d.Reason), d.DNSAnswers)
		egressBlockLog(d)
	}))
	if err != nil {
		log.Error("failed to build egress allow-list", "error", err)
		return 1
	}
	// Subsystem-scoped guards attribute blocks and metrics without call sites
	// importing the egress enforcement internals. All guards share one base
	// dialer (safe for concurrent use).
	egressBase := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	discoveryDial := egressPolicy.For(egress.SubsystemDiscovery).DialContext(egressBase)
	authDial := egressPolicy.For(egress.SubsystemAuth).DialContext(egressBase)
	// PKI clients are guarded only when egress is enabled; when disabled they are
	// nil so ACME/OCSP keep their default clients (including HTTP(S)_PROXY
	// support), preserving backward-compatible behavior.
	var acmeClient, ocspClient *http.Client
	if egressPolicy.Enabled() {
		acmeClient = egressPolicy.For(egress.SubsystemACME).Client(0)
		ocspClient = egressPolicy.For(egress.SubsystemOCSP).Client(0)
	}

	// The process-lifetime runtime subsystems (tracing, ACME, stream server,
	// build-tag feature gates) are built once at startup and outlive every
	// reload. rt.Close (deferred) shuts them down on graceful exit.
	rt, err := RuntimeBuilder{
		Config:     cfg,
		Logger:     log,
		Metrics:    metrics,
		ACMEClient: acmeClient,
		OCSPClient: ocspClient,
	}.Build()
	if err != nil {
		log.Error("failed to initialize server runtime", "error", err)
		return 1
	}
	defer rt.Close()

	// The Console Operations Log tail is a permanent, process-lifetime sink:
	// it preserves Console Operations-Log/SSE history and subscribers across
	// every reload, and is appended to each generation's candidate sink set
	// (built per-reload in HandlerFactory.buildHandlers, #98) only when access
	// logging is effectively enabled. It is never closed or recreated by an
	// access-log configuration change.
	logTail := observability.NewLogTail(0)

	// The rate-limiter store persists across reloads (its janitor is bound to
	// baseCtx) so token buckets survive config edits.
	rlStore := middleware.NewRateLimiterStore(baseCtx, 0, 0)

	// The upstream pool registry persists across reloads so named-upstream
	// pools (and their health-check goroutines) have a defined lifetime.
	poolReg := upstream.NewRegistry(upstream.RegistryOptions{
		Logger:              log,
		OnHealth:            metrics.ObserveBackendHealth,
		OnProbe:             metrics.ObserveProbe,
		OnBackends:          metrics.ObserveUpstreamBackends,
		OnBackendsHealthy:   metrics.ObserveBackendsHealthy,
		OnCircuitTransition: func(pool string, to upstream.BackendState) { metrics.ObserveCircuitTransition(pool, string(to)) },
		OnPoolRetired:       metrics.RetirePool,
		OnDiscoveryError:    metrics.ObserveDiscoveryError,
		DialContext:         discoveryDial,
	})
	defer poolReg.CloseAll()
	// The live gauges are read at scrape time rather than pushed from the
	// admission path, which is the hottest code in the proxy.
	metrics.SetUpstreamStatsSource(func() []observability.UpstreamPoolStats {
		return upstreamStats(poolReg)
	})

	// The WASM plugin manager persists across reloads so the compilation
	// cache and KV store survive config edits. Plugin fetches are guarded by
	// both their local allowed_hosts/SSRF rules and, when egress is enabled, the
	// server-wide allow-list via EgressWrap.
	var pluginEgressWrap func(plugins.DialFunc) plugins.DialFunc
	if egressPolicy.Enabled() {
		pluginEgressWrap = egressPolicy.For(egress.SubsystemPlugin).DialContextWith
	}
	pluginMgr, err := plugins.NewManager(plugins.Options{
		Logger:       log,
		OnInvocation: metrics.ObservePluginInvocation,
		OnPanic:      metrics.ObservePluginPanic,
		EgressWrap:   pluginEgressWrap,
	})
	if err != nil {
		log.Error("failed to initialize plugin manager", "error", err)
		return 1
	}
	defer func() { _ = pluginMgr.Close() }()

	// genRes owns the generational teardown lifecycle of the handler tree.
	genRes := NewGenerationResources(poolReg)
	defer genRes.CloseLive()

	// Capture the startup-bound effective fingerprint for restart-required
	// checks. Compute it after all process-lifetime consumers above have
	// initialized (cache, metrics, tracing/ACME/stream runtime, access-log
	// sinks, egress) so path/content canonicalization sees the same state that
	// the running process will use (R6-13).
	startupFP := lifecycle.ComputeFingerprint(cfg)

	// ── Section 2: HandlerFactory ──────────────────────────────────────────
	//
	// The factory holds the process-lifetime dependencies and rebuilds the
	// per-listen-address handler tree on every reload. See factory.go.
	f := &HandlerFactory{
		Log:           log,
		Metrics:       metrics,
		Cache:         responseCache,
		AccessLogTail: logTail,
		RLStore:       rlStore,
		EgressDial:    authDial,
		PoolReg:       poolReg,
		PluginMgr:     pluginMgr,
		GenRes:        genRes,
		RT:            rt,
	}

	// factory adapts HandlerFactory.Prepare to the server reload hook: the
	// three-phase prepare/commit/abort pattern keeps the generation uncommitted
	// until listener staging succeeds (R4-01). The factory no longer returns a
	// redact.State; the server receives the startup candidate's redaction
	// directly so the initial generation masks exactly the secrets resolved at
	// startup (R7-05, R9-01).
	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		return f.Prepare(ctx, c)
	}

	ctx, cancel := context.WithCancel(baseCtx)
	defer cancel()

	// adminReload carries typed reload requests from the admin apply path.
	// It is buffered so a candidate-bearing request can queue while a reload is
	// in progress, but it preserves causal ordering: the candidate is part of
	// the request message, not stored in a separate global slot (R9-02).
	// Unbuffered ownership transfer: SubmitReload succeeds only when the fan-in
	// has accepted responsibility for forwarding or rejecting the request.
	adminReload := make(chan server.ReloadRequest)
	// lastAdminDigest holds the SHA-256 digest of the most recent raw config
	// written by an admin apply. The file watcher uses it to suppress its own
	// echo of that write (R10-01).
	var lastAdminDigest atomic.Pointer[[32]byte]
	var triggerReload func(*config.Candidate, [32]byte) error
	triggerReload = func(c *config.Candidate, rawDigest [32]byte) error {
		req := server.ReloadRequest{Source: server.ReloadSourceAdmin, RawDigest: rawDigest}
		if c != nil {
			req.Candidate = c
		}
		// Record the digest before enqueueing so the file watcher can suppress
		// the echo even if the watcher fires before the enqueue completes.
		if rawDigest != [32]byte{} {
			lastAdminDigest.Store(&rawDigest)
		}
		// Synchronous enqueue with a 5-second timeout. This guarantees the
		// admin caller receives a definite ack instead of a silent drop when
		// the runtime is backlogged (R10-01).
		select {
		case adminReload <- req:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("reload dispatch canceled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
			return fmt.Errorf("reload enqueue timed out after 5s")
		}
	}

	// Only file-backed sources can be watched; zero-config (in-memory) sources
	// have no file on disk, so file-watching is skipped for them.
	var fileWatch <-chan [32]byte
	tomlSrc, hasConfigPath := src.(*config.TOMLSource)
	if hasConfigPath {
		fileWatch = watchConfig(ctx, tomlSrc.Path, log)
	}

	// Authority is resolved once, here, before any writer is wired (ADR 0019
	// §9.1/Invariant T1). The fixed file_owned default is never derived from
	// any other field, in particular never from admin.enabled.
	authority, authoritySource := ResolveConfigAuthority(cfg.Global.ConfigAuthority, hasConfigPath)
	if authoritySource == AuthoritySourceDefault {
		log.Info("config_authority not declared; defaulting to file_owned",
			"config_authority", authority.String(),
			"hint", `set [global].config_authority = "managed" or "file_owned" to declare it explicitly`)
	}
	// ADR 0019 §11.3: managed mode requires a writable, non-symlinked config
	// path. Reported rather than fatal — the process still runs, serves
	// traffic, and reports the mode; only managed writes will fail.
	if hasConfigPath {
		for _, d := range CheckManagedFilesystem(tomlSrc.Path, authority) {
			if d.Severity == config.SeverityError {
				log.Error("managed configuration path is unsuitable", "message", d.Message, "hint", d.Hint)
			} else {
				log.Warn("managed configuration path may be unsuitable", "message", d.Message, "hint", d.Hint)
			}
		}
	}

	// In managed mode neither the file watcher nor SIGHUP triggers a reload
	// (ADR 0019 §11 points 4-5): both become drift detectors. The real
	// channels are drained here and never forwarded into MergeReload; each
	// event schedules a drift re-assessment instead. assessDriftRequests is
	// drained once the coordinator exists, later in this function.
	assessDriftRequests := make(chan struct{}, 1)
	mergeSigReload, mergeFileWatch := sigReload, fileWatch
	if authority == AuthorityManaged {
		mergeSigReload, mergeFileWatch = nil, nil
		go driftOnlySignalConsumer(ctx, sigReload, assessDriftRequests)
		go driftOnlyFileConsumer(ctx, fileWatch, assessDriftRequests)
	}
	// Merge SIGHUP (when present), config file-watch, and admin-triggered
	// reloads into a single typed channel.
	reload := MergeReload(ctx, mergeSigReload, mergeFileWatch, adminReload, &lastAdminDigest)

	// ── Section 3: Preflight ───────────────────────────────────────────────
	//
	// preflight is the truthfulness gate for every config write: a config
	// that passes it is guaranteed to build, so the asynchronous reload cannot
	// fail for configuration reasons. See preflight.go.
	pf := Preflight{
		BuildHandlers: f.Build,
		Stream:        rt.Stream,
		StartupFP:     startupFP,
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
	// Defer the candidate-injecting reload until after the server is created.
	// For now wire the no-candidate fallback used by the admin /reload button.
	deps.Reload = func() error { return triggerReload(nil, [32]byte{}) }
	var readyFlag admin.Readiness
	deps.Ready = readyFlag.Ready
	deps.LoadConfig = src.Load
	deps.TrafficSources = metrics.TrafficSnapshot

	var configPath string
	if ts, ok := src.(*config.TOMLSource); ok {
		configPath = ts.Path
		deps.ReadConfigRaw = func() ([]byte, error) { return os.ReadFile(configPath) }
	}

	// Construct the server and wire LastReload into deps BEFORE creating the
	// admin server. admin.New copies deps by value, so any callback assigned
	// after that call is invisible to the admin server's apply handlers.
	srv := server.New(cfg, startupCand.Raw, startupFP, log, factory, src, func(ctx context.Context, c *config.Config) error {
		return ValidateRuntimeConfig(ctx, c)
	})
	// Overrides the src.Load()-backed default from BuildAdminDeps: certificate
	// status must reflect the server's actually-installed certificate, not
	// whatever bytes currently sit on disk — a rejected candidate, or an
	// out-of-band file rewrite between reloads, must never make an
	// unpublished or invalid certificate appear live (#100).
	deps.Certs = func() []admin.CertStatus {
		return AdaptCerts(srv.LiveCertSummaries())
	}
	srv.ConnStateHook = metrics.ConnState
	srv.OnReloadStart = metrics.ReloadStarted
	srv.OnReloadComplete = metrics.ObserveReload
	// C1 (N-04): maintain a durable admin-degraded flag that persists until a
	// post-Publish reload succeeds for the admin subsystem. It is not cleared
	// by pre-Publish failures, which fixes the defect where a failed pre-Publish
	// reload overwrites the LastReload pointer and silently clears a previously
	// degraded state reported by a published reload.
	var activeAdminDegraded atomic.Bool
	var lastAdminDegradedErr atomic.Pointer[string]
	// lastManagedApplyFinalization is the ADVISORY, non-readiness finalization-
	// health state of the most recent managed apply (WS02 §3.9). It is surfaced
	// in the runtime overview as managed_apply_finalization and is deliberately
	// INDEPENDENT of readiness: a finalization degradation (a completion-callback
	// panic, a terminal-ledger completion failure, or a configuration-history
	// snapshot/metadata failure) NEVER fails /readyz and NEVER turns an
	// already-committed apply into a failed apply (the raw configuration stays
	// roll-back-able). The terminal finalizer publishes a healthy advisory after
	// a clean finalization — which CLEARS any prior degradation (§3.9) — and an
	// unhealthy advisory carrying the apply ID and a bounded detail on
	// degradation.
	var lastManagedApplyFinalization atomic.Pointer[admin.ManagedApplyAdvisory]
	srv.OnReloadResult = func(r server.ReloadResult) {
		metrics.ObserveReloadResult(string(r.Outcome), r.PhaseDurations, r.TimedOut, r.TimedOutPhase)
		// Only published reloads affect the durable admin-health flag.
		// Pre-Publish failures leave it unchanged.
		if r.Published {
			if r.Admin.Status == server.ReloadSubsystemFailed || r.Admin.Status == server.ReloadSubsystemTimedOut {
				msg := "admin subsystem reload failed: " + r.Admin.Error
				lastAdminDegradedErr.Store(&msg)
				activeAdminDegraded.Store(true)
			} else {
				// Successful post-Publish admin clears the degraded state.
				lastAdminDegradedErr.Store(nil)
				activeAdminDegraded.Store(false)
			}
		}
	}
	srv.ACME = rt.ACME

	// Wire the runtime snapshot into preflight after the server exists so
	// listener gates are evaluated against actually-bound listeners (R9-04).
	pf.LiveSnapshot = srv.LiveSnapshot

	// Make the live bound-listener snapshot available to the admin overview
	// for the pending-restart check (R9-11).
	deps.LiveSnapshot = srv.LiveSnapshot

	// Re-wire the reload trigger so admin apply sends a typed ReloadRequest
	// carrying the exact preflight candidate. The candidate is part of the
	// channel message, not a global slot, so it cannot be consumed by an
	// unrelated SIGHUP or file-watch event (R9-02).
	triggerReload = func(c *config.Candidate, rawDigest [32]byte) error {
		req := server.ReloadRequest{Source: server.ReloadSourceAdmin, RawDigest: rawDigest}
		if c != nil {
			req.Candidate = c
		}
		if rawDigest != [32]byte{} {
			lastAdminDigest.Store(&rawDigest)
		}
		select {
		case adminReload <- req:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("reload dispatch canceled: %w", ctx.Err())
		case <-time.After(5 * time.Second):
			return fmt.Errorf("reload enqueue timed out after 5s")
		}
	}
	deps.Reload = func() error {
		return triggerReload(nil, [32]byte{})
	}
	deps.LastReload = func() *server.ReloadResult {
		return srv.LastReload()
	}
	// Admin subsystem health: propagate admin-side reload failures and other
	// runtime-level admin concerns to /readyz and the runtime overview (F-05).
	// H-04: only fail readiness for post-Publish admin subsystem failure or
	// timeout. Pre-publish failures, skipped phases, and not_run do not block
	// readiness because the previous runtime state is still serving.
	// C1 (N-04): read from the durable activeAdminDegraded flag rather than
	// deriving health from srv.LastReload() alone. A later pre-Publish failure
	// updates LastReload to a result with Published=false, which would
	// previously erase a degraded state from a prior published failure.
	deps.AdminHealth = func() error {
		if activeAdminDegraded.Load() {
			detail := "admin subsystem reload failed"
			if p := lastAdminDegradedErr.Load(); p != nil && *p != "" {
				detail = *p
			}
			return &admin.AdminHealthStatus{
				Healthy: false,
				Reason:  "admin_reload",
				Detail:  detail,
			}
		}
		// WS02 §3.9: managed-apply finalization health is ADVISORY and MUST NOT
		// gate readiness. It is surfaced separately through
		// deps.ManagedApplyFinalizationHealth (the runtime overview's
		// managed_apply_finalization field), never through AdminHealth/readyz, so
		// a terminal-finalization degradation never fails /readyz and never rolls
		// back an already-committed apply.
		return nil
	}
	// WS02 §3.9: advisory, non-readiness managed-apply finalization health. It is
	// surfaced in the runtime overview as managed_apply_finalization and is wired
	// here — before admin.New copies deps — so the running overview reads the
	// same atomic pointer the terminal finalizer publishes to. It NEVER gates
	// /readyz. Nil until the first managed apply finalizes, so the overview omits
	// the field.
	deps.ManagedApplyFinalizationHealth = func() *admin.ManagedApplyAdvisory {
		return lastManagedApplyFinalization.Load()
	}

	// Wire the managed apply path now that srv exists so the coordinator can
	// read the live serving version and submit correlated reload requests.
	//
	// ONE shared PlannedRestartStore is constructed here and used by both the
	// coordinator and the startup reconciliation hook. This ensures that
	// reconciliation on successful startup updates the same in-memory state
	// that the coordinator and API see, preventing the split-brain defect where
	// a second store reconciles sidecar files but the coordinator's store still
	// reports pending=true (C-02 fix).
	var coordinator *ConfigApplyCoordinator
	if configPath != "" {
		submitReload := func(req server.ReloadRequest) error {
			select {
			case adminReload <- req:
				return nil
			case <-ctx.Done():
				return fmt.Errorf("reload dispatch canceled: %w", ctx.Err())
			case <-time.After(5 * time.Second):
				return fmt.Errorf("reload enqueue timed out after 5s")
			}
		}

		// Single shared store — coordinator and reconciliation use the same instance.
		sharedStore := NewFilePlannedRestartStore(configPath)
		// The managed baseline persists ownership across a restart (ADR 0019
		// §11.2). It is constructed regardless of authority mode so a
		// file_owned startup can find and clean up artifacts inherited from a
		// prior managed epoch (§17.2).
		managedBaseline := NewManagedBaselineStore(configPath)

		// Wire PendingRestartCheck using the startup config snapshot. It reports
		// which startup-bound subsystems differ between what we were built from
		// and what is currently on disk, and synchronizes the authoritative
		// external-divergence flag on the shared store so hot applies can be
		// blocked consistently (F-04).
		if startupCand.Raw != nil && deps.LoadConfig != nil {
			deps.PendingRestartCheck = func(live server.LiveSnapshot) []string {
				subsystems := pendingRestartCheck(startupCand, startupFP, live, deps.LoadConfig, log)
				sharedStore.SetExternalDivergence(len(subsystems) > 0)
				return subsystems
			}
		}

		coordinator = &ConfigApplyCoordinator{
			BaseCtx:         ctx,
			Path:            configPath,
			Preflight:       &pf,
			SubmitReload:    submitReload,
			LiveSnapshot:    srv.LiveSnapshot,
			WatchDigest:     &lastAdminDigest,
			PlannedRestart:  sharedStore,
			Authority:       authority,
			ManagedBaseline: managedBaseline,
		}
		// Drain the drift-assessment requests scheduled by the watcher/SIGHUP
		// consumers now that the coordinator exists to service them.
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-assessDriftRequests:
					coordinator.AssessDriftNow()
					metrics.SetConfigAuthorityDrift(managedBaseline.Status().Drift)
				}
			}
		}()

		// Reconcile planned-restart sidecar files after the data plane is live.
		// Using OnInitialGenerationReady ensures reconciliation fires only after
		// all startup listeners have bound successfully. On any startup failure
		// the hook is never called and recovery files are preserved.
		srv.OnInitialGenerationReady = func() {
			if err := sharedStore.Reconcile(); err != nil {
				log.Warn("planned-restart reconciliation warning (manual recovery may be needed)",
					"error", err,
					"backup", configPath+".pending-restart.bak",
				)
			}
			// Sync the pending-restart gauge from the now-reconciled shared store.
			metrics.SetPendingRestart(sharedStore.IsPending())

			// ADR 0019 §11.2.3: the baseline reconciles second, against
			// whatever the planned-restart reconciliation above left on disk.
			if authority == AuthorityManaged {
				diskRaw, diskErr := os.ReadFile(configPath)
				var diskVersion, diskParseErr string
				if diskErr == nil {
					if diskCfg, perr := config.Parse(diskRaw); perr == nil {
						diskVersion = server.CanonicalVersion(diskCfg)
					} else {
						diskParseErr = perr.Error()
					}
				}
				if err := managedBaseline.Reconcile(diskRaw, diskErr, diskVersion, diskParseErr); err != nil {
					log.Warn("managed-baseline reconciliation warning (manual recovery may be needed)",
						"error", err,
						"snapshot", configPath+".managed-baseline.snapshot",
					)
				}
				metrics.SetConfigAuthorityDrift(managedBaseline.Status().Drift)
			} else {
				// file_owned mode writes no configuration, ever, except this
				// one bounded cleanup performed once at startup (§17.2): close
				// any managed epoch inherited from a prior restart, and remove
				// any orphan planned-restart backup Reconcile would never
				// collect on its own. RemoveOrphanBackup is threaded through
				// as the step-2 callback so CloseEpoch runs the two
				// secret-bearing removals (snapshot, then orphan backup)
				// strictly before the safe tombstone write.
				hadManagedArtifacts := managedBaseline.HasArtifacts()
				if err := managedBaseline.CloseEpoch(sharedStore.RemoveOrphanBackup); err != nil {
					log.Warn("file-owned startup could not remove leftover managed-baseline artifacts (read-only mount?)", "error", err)
				} else if hadManagedArtifacts {
					log.Info("file-owned startup closed a leftover managed-baseline epoch", "config", configPath)
				}
			}
		}

		coordinator.RefreshState = func() error {
			if err := sharedStore.Refresh(); err != nil {
				return err
			}
			// Only check for unmanaged disk divergence when no managed staged
			// restart is pending. A valid managed marker accounts for the
			// disk/runtime difference and must not be misclassified as external
			// divergence (H-02). Clear any stale external flag when the managed
			// staged state is confirmed.
			if sharedStore.IsPending() {
				sharedStore.SetExternalDivergence(false)
			} else if deps.PendingRestartCheck != nil && deps.LiveSnapshot != nil {
				_ = deps.PendingRestartCheck(deps.LiveSnapshot())
			}
			return nil
		}

		deps.ApplyConfigRaw = func(ctx admin.ApplyRequestContext, data []byte, mode string) (admin.ConfigApplyResult, error) {
			res, err := coordinator.ApplyRaw(ctx, data, ApplyMode(mode))
			metrics.SetConfigAuthorityDrift(managedBaseline.Status().Drift)
			result := toAdminConfigApplyResult(res)
			if mode == string(ApplyStageRestart) {
				result.StagedRestartIsUpdate = res.StagedRestartIsUpdate
				if res.OK {
					if res.StagedRestartIsUpdate {
						metrics.ObserveStageRestart("updated")
					} else {
						metrics.ObserveStageRestart("created")
					}
					metrics.SetPendingRestart(true)
				} else {
					metrics.ObserveStageRestart("failed")
				}
			}
			return result, err
		}
		deps.ApplyConfig = func(ctx admin.ApplyRequestContext, c *config.Config, mode string) (admin.ConfigApplyResult, error) {
			res, err := coordinator.ApplyConfig(ctx, c, ApplyMode(mode))
			metrics.SetConfigAuthorityDrift(managedBaseline.Status().Drift)
			result := toAdminConfigApplyResult(res)
			if mode == string(ApplyStageRestart) {
				result.StagedRestartIsUpdate = res.StagedRestartIsUpdate
				if res.OK {
					if res.StagedRestartIsUpdate {
						metrics.ObserveStageRestart("updated")
					} else {
						metrics.ObserveStageRestart("created")
					}
					metrics.SetPendingRestart(true)
				} else {
					metrics.ObserveStageRestart("failed")
				}
			}
			return result, err
		}
		deps.DiscardPendingRestart = func() (admin.ConfigApplyResult, error) {
			res, err := coordinator.DiscardPlannedRestart()
			if err == nil && res.OK {
				metrics.ObserveStageRestart("discarded")
				metrics.SetPendingRestart(false)
			}
			return toAdminConfigApplyResult(res), err
		}
		deps.PendingRestart = func() *admin.PendingRestartStatus {
			return coordinator.PlannedRestartStatus()
		}
		deps.AdoptExternalPreview = func() (admin.AdoptPreviewResult, error) {
			a, err := coordinator.AssessAdoptExternal()
			if err != nil {
				return admin.AdoptPreviewResult{}, err
			}
			return toAdminAdoptPreviewResult(a), nil
		}
		deps.AdoptExternal = func(ctx admin.ApplyRequestContext, req admin.AdoptExternalRequest) (admin.ConfigApplyResult, error) {
			res, err := coordinator.AdoptExternal(ctx, req)
			metrics.SetConfigAuthorityDrift(managedBaseline.Status().Drift)
			return toAdminConfigApplyResult(res), err
		}
		// Keep legacy closures for external callers during the deprecation
		// window. They pass an empty request context because they are not
		// tied to an authenticated HTTP request.
		deps.WriteConfigRaw = func(data []byte) error {
			result, err := deps.ApplyConfigRaw(admin.ApplyRequestContext{}, data, string(ApplyHot))
			if err != nil {
				return err
			}
			if !result.OK {
				// Convert structured rejection to a typed error so legacy callers
				// (rollbackToSnapshot, etc.) see the real failure reason.
				if result.RestartRequired {
					return fmt.Errorf("%w: %s", admin.ErrRestartRequired, result.Message)
				}
				if len(result.ValidationErrors) > 0 {
					return fmt.Errorf("validation failed: %s", strings.Join(result.ValidationErrors, "; "))
				}
				if result.PendingRestart != nil {
					return fmt.Errorf("hot apply blocked: a managed staged restart is pending; discard it first")
				}
				return fmt.Errorf("apply rejected: %s", result.Message)
			}
			return nil
		}
		deps.SaveConfig = func(c *config.Config) error {
			result, err := deps.ApplyConfig(admin.ApplyRequestContext{}, c, string(ApplyHot))
			if err != nil {
				return err
			}
			if !result.OK {
				if result.RestartRequired {
					return fmt.Errorf("%w: %s", admin.ErrRestartRequired, result.Message)
				}
				return fmt.Errorf("apply rejected: %s", result.Message)
			}
			return nil
		}
	}

	// Authority status is exposed regardless of whether a coordinator exists
	// (a process with no config file is file_owned/no_config_file, ADR 0019
	// §9.1.1). It never gates anything itself — the coordinator and the admin
	// denial gate are the enforcement points — so a nil dependency is safe.
	deps.Authority = func() admin.ConfigAuthorityStatus {
		status := admin.ConfigAuthorityStatus{
			Mode:   authority.String(),
			Source: string(authoritySource),
		}
		if coordinator != nil {
			state, reason := coordinator.currentConfigState()
			status.ConfigState = string(state)
			status.InconsistentReason = string(reason)
		}
		// Drift/version/digest fields are managed-mode baseline evidence.
		// Gating them on authority (not just a nil coordinator) keeps a
		// file_owned process from surfacing artifacts a prior managed epoch
		// left behind, matching config_state's own no-leak requirement.
		if coordinator != nil && authority == AuthorityManaged && coordinator.ManagedBaseline != nil {
			bst := coordinator.ManagedBaseline.Status()
			status.Drift = bst.Drift
			if !bst.DriftDetectedAt.IsZero() {
				status.DriftDetectedAt = bst.DriftDetectedAt
			}
			status.BaselineVersion = bst.BaselineCanonicalVersion
			status.DiskVersion = bst.DiskCanonicalVersion
			status.DiskParseError = bst.DiskParseError
			status.DiskRawDigest = truncatedDigest(bst.DiskRawSHA256)
		}
		return status
	}
	// ADR 0019 §12's fourth event-driven drift trigger: an explicit,
	// operator- or Console-initiated refresh, distinct from the other three
	// (watcher, SIGHUP, pre-write CAS) and never invoked by a passive status
	// poll such as deps.Authority above — doing so would turn drift
	// detection into the polling loop §12 says does not exist.
	deps.RefreshAuthorityDrift = func() admin.ConfigAuthorityStatus {
		if coordinator != nil {
			coordinator.AssessDriftNow()
			if coordinator.ManagedBaseline != nil {
				metrics.SetConfigAuthorityDrift(coordinator.ManagedBaseline.Status().Drift)
			}
		}
		return deps.Authority()
	}
	deps.ObserveAuthorityDenied = metrics.ObserveConfigAuthorityDenied

	// H-05: reserve storage and register the LastManagedApply dep before
	// admin.New copies deps, so the overview can read it. The actual callback
	// that populates the pointer is wired after adminSrv exists.
	var lastManagedApply atomic.Pointer[admin.ManagedApplyOutcome]
	// C3: monotonic high-water mark of stored apply outcomes. Callbacks with a
	// lower sequence number (stale results from a timed-out earlier apply that
	// fired after a newer apply completed) are silently dropped.
	var lastManagedApplySeq atomic.Uint64
	var lastManagedApplyMu sync.Mutex
	// AC-02: bounded terminal-result ledger so a browser can retrieve the exact
	// terminal result of any recent accepted apply by exact ID, independent of
	// the singular latest pointer. Wired into deps before admin.New copies them.
	managedApplies := admin.NewManagedApplyRegistry(0, 0)
	if coordinator != nil {
		deps.ManagedApplies = managedApplies
		deps.LastManagedApply = func() *admin.ManagedApplyOutcome {
			return lastManagedApply.Load()
		}
		// AC-05: the managed coordinator records configuration-history snapshots
		// at terminalization, so the HTTP handlers must not snapshot eagerly.
		// The trusted history write is performed by the unified completion
		// callback wired after adminSrv exists (below); this flag must be set
		// before admin.New copies deps.
		deps.ManagedHistoryActive = true

		// AC-02: register the exact-ID pending ledger record the moment a
		// managed apply persists its candidate and enqueues the reload, BEFORE
		// the synchronous HTTP path can return a 202 saved_not_live. This closes
		// the window where a real 202 could be immediately followed by a spurious
		// 404 that stalls the ConfigPanel poll. Wired here — before admin.New
		// copies deps and starts the listener — so the ledger already accepts
		// pending writes by the time the admin server can receive apply requests.
		coordinator.OnManagedApplyStarted = func(start admin.ManagedApplyStart) error {
			applyID := start.Result.ApplyID
			if applyID == "" && start.Result.Reload != nil {
				applyID = start.Result.Reload.ID
			}

			if applyID == "" {
				return errors.New("managed apply start has no apply id")
			}

			return managedApplies.BeginPending(admin.ManagedApplyRecord{
				ID:           applyID,
				State:        admin.ManagedApplyPending,
				Operation:    start.Context.Operation,
				StartedAt:    start.Context.StartedAt,
				Deadline:     start.Context.Deadline,
				Result:       start.Result,
				OwnerTokenID: start.Context.TokenID,
			})
		}
	}

	// Build admin server before srv.Run so it is running when the first
	// generation is ready. adminSrv is hoisted out of the if-block so the
	// reload hook can call UpdatePolicy after each successful hot reload.
	adminSrv := admin.New(cfg.Admin, log, deps)
	if adminSrv != nil {
		// Install the initial RBAC policy from the startup candidate.
		// H-01: Fail-closed when RBAC is enabled but policy build fails.
		// Never silently fall back to legacy or anonymous auth.
		if cfg.Admin.RBAC.Enabled {
			if p, err := buildRBACPolicy(startupCand.Effective.Admin); err != nil {
				log.Error("RBAC policy build failed at startup; admin listener will not start", "error", err)
				return 1
			} else {
				adminSrv.UpdatePolicy(p)
			}
		}
		prepareAdmin := func(adminCfg config.AdminConfig) (*server.PreparedCommit, error) {
			var policy *rbac.Policy
			if adminCfg.RBAC.Enabled {
				built, err := buildRBACPolicy(adminCfg)
				if err != nil {
					return nil, fmt.Errorf("rbac policy: %w", err)
				}
				policy = built
			}
			prepared := admin.PrepareAuth(adminCfg, policy)
			preparedTLS, err := adminSrv.PrepareTLS(adminCfg)
			if err != nil {
				return nil, err
			}
			return server.NewPreparedCommit(func() {
				adminSrv.CommitPreparedAuth(prepared)
				adminSrv.CommitPreparedTLS(preparedTLS)
			}, nil), nil
		}
		pf.PrepareAdmin = prepareAdmin
		srv.PrepareAdmin = prepareAdmin
		go func() {
			if err := adminSrv.Run(ctx); err != nil {
				log.Error("admin listener failed", "error", err)
			}
		}()
	}

	// H-05: after adminSrv exists, wire the coordinator's async terminal
	// outcome callback so it records audit/metrics and exposes the result in
	// the runtime overview.
	if coordinator != nil && adminSrv != nil {
		coordinator.AuthGeneration = adminSrv.AuthGeneration
		// WS02 §3.6/§3.9: make a managed-apply finalization callback panic
		// EXPLICIT. The coordinator recovers the panic, threads a
		// FinalizationError onto the terminal result, and invokes this hook so
		// the composition root (1) writes a structured error log, (2) increments
		// the finalization-error metric, (3) publishes an advisory, NON-READINESS
		// finalization-health state surfaced through the runtime overview's
		// managed_apply_finalization field (never /readyz), and (4) best-effort
		// writes a terminal ledger record carrying the FinalizationError. A
		// finalization panic never fails an already-committed apply: the raw
		// config stays roll-back-able.
		coordinator.OnManagedApplyFinalizationError = func(completion admin.ManagedApplyCompletion, finalizationErr error) {
			applyID := completion.Result.ApplyID
			if applyID == "" && completion.Result.Reload != nil {
				applyID = completion.Result.Reload.ID
			}

			detail := "managed apply finalization callback failed"
			if finalizationErr != nil {
				detail = finalizationErr.Error()
			}
			log.Error("managed apply finalization callback panicked",
				"apply_id", applyID,
				"error", detail,
			)
			metrics.ObserveManagedApplyFinalizationError("callback_panic")
			// WS02 §3.9: publish the advisory, non-readiness finalization health.
			// A panic aborts the normal completion path, so this is the only
			// advisory write for the ID; it never gates /readyz.
			lastManagedApplyFinalization.Store(&admin.ManagedApplyAdvisory{
				Healthy: false,
				At:      time.Now().UTC(),
				ApplyID: applyID,
				Detail:  detail,
			})
			if applyID == "" {
				return
			}
			// Best-effort terminal ledger record so a browser retrieving the
			// exact apply ID sees the finalization failure. FailFinalization
			// preserves the operation and complete apply result in place instead
			// of overwriting them with this incomplete emergency fallback; the
			// completion's PreviousRaw is sensitive and is never forwarded here.
			fallback := admin.ManagedApplyRecord{
				ID:                applyID,
				Operation:         completion.Context.Operation,
				StartedAt:         completion.Context.StartedAt,
				Deadline:          completion.Context.Deadline,
				CompletedAt:       time.Now().UTC(),
				Result:            completion.Result,
				OwnerTokenID:      completion.Context.TokenID,
				FinalizationError: detail,
			}
			if err := managedApplies.FailFinalization(fallback); err != nil {
				log.Error("managed apply panic fallback could not complete ledger",
					"apply_id", applyID,
					"error", err.Error(),
				)
				metrics.ObserveManagedApplyFinalizationError("registry")
				return
			}
			// WS06 §7.5: keep the retained-ledger gauge in sync after this
			// out-of-band terminal completion.
			metrics.SetManagedApplyRegistryEntries(managedApplies.TerminalCount())
		}
		// WS06 §7.6: make managed-apply machinery failures that happen OUTSIDE
		// the unified completion callback explicit — a saved_not_live terminal
		// restoration write failure and a post-persistence pending-registration
		// write failure. The coordinator supplies a bounded phase; the hook emits
		// a structured error log and the bounded finalization-error metric so the
		// failure is observable instead of silently swallowed. It never fails an
		// already-committed apply: the raw configuration stays roll-back-able.
		coordinator.ReportManagedApplyError = func(applyID, phase string, err error) {
			detail := "managed apply " + phase + " failed"
			if err != nil {
				detail = err.Error()
			}
			log.Error("managed apply "+phase+" failed",
				"apply_id", applyID,
				"phase", phase,
				"error", detail,
			)
			metrics.ObserveManagedApplyFinalizationError(phase)
		}
		// WS02 §3.7: the unified completion callback owns terminal finalization
		// through the single managedApplyFinalizer orchestrator. It claims the
		// transaction BEFORE any history write (fixing WS02 §3.2 defect 2),
		// performs the trusted in-process configuration-history write (previousRaw
		// is sensitive and forwarded only there — never logged or retained by the
		// coordinator), records the audit/metrics/ledger side effects exactly once,
		// no longer ignores the terminal-ledger Complete error (defect 4), fails
		// closed with an explicit FinalizationError on a claim error (defect 5),
		// and returns the finalization provenance threaded back onto the terminal
		// result. The advisory, NON-READINESS finalization-health state surfaced
		// through the runtime overview's managed_apply_finalization field (WS02
		// §3.9) is published on every terminalization — healthy after a clean
		// finalize (which clears any prior degradation) and unhealthy on any
		// claim/complete/history failure — so a terminal finalization degradation
		// is explicit rather than silently swallowed, without ever gating /readyz.
		finalizer := &managedApplyFinalizer{
			registry:  managedApplies,
			admin:     adminSrv,
			metrics:   metrics,
			log:       log,
			latest:    &lastManagedApply,
			latestSeq: &lastManagedApplySeq,
			latestMu:  &lastManagedApplyMu,
			setAdvisory: func(a admin.ManagedApplyAdvisory) {
				lastManagedApplyFinalization.Store(&a)
			},
		}
		coordinator.OnManagedApplyComplete = finalizer.Finalize
		deps.LastManagedApply = func() *admin.ManagedApplyOutcome {
			return lastManagedApply.Load()
		}
	}

	readyFlag.Set(true)
	// NOTE: planned-restart reconciliation now happens in srv.OnInitialGenerationReady
	// (wired above) which fires after all startup listeners are bound successfully.
	// This ensures recovery files are never removed if startup subsequently fails.

	srv.HTTP3ConnHook = metrics.HTTP3ConnDelta
	srv.MTLSResultHook = metrics.ObserveMTLSHandshake
	// Drive L4 stream-proxy reloads from the same validated config as the HTTP
	// listeners. Stream binding errors are reported as a degraded reload result
	// but do not roll back the HTTP swap (the listener sets are independent).
	srv.OnReloaded = func(c *config.Config) (adminErr, streamErr error) {
		// Hot-reload log level without rebuilding the handler. Log format changes
		// are restart-required and blocked by lifecycle checks before here.
		setLogLevel(c.Global.LogLevel)
		// Apply worker_threads on reload. When set to a positive integer, cap
		// GOMAXPROCS; when "auto" or empty, restore the initial container-aware
		// default captured at process startup (R5-08).
		if n := parseWorkerThreads(c.Global.WorkerThreads); n > 0 {
			runtime.GOMAXPROCS(n)
		} else {
			runtime.GOMAXPROCS(lifecycle.InitialGOMAXPROCS())
		}
		if err := rt.Stream.Reload(c.Streams, IndexUpstreams(c.Upstreams)); err != nil {
			log.Error("stream proxy reload failed", "error", err)
			msg := "failed: " + err.Error()
			rt.LastStreamReload.Store(&msg)
			streamErr = err
			return
		}
		ok := ""
		if len(c.Streams) > 0 {
			ok = "ok"
		}
		rt.LastStreamReload.Store(&ok)
		return
	}
	if err := srv.Run(ctx, reload, startupCand.Redaction); err != nil {
		log.Error("server exited with error", "error", err)
		return 1
	}
	return 0
}

// toAdminConfigApplyResult converts an app-layer ApplyResult into the admin
// API response shape. It is a pure projection: no new policy is added.
func toAdminConfigApplyResult(r ApplyResult) admin.ConfigApplyResult {
	var degraded []admin.DegradedEntry
	for _, d := range r.Degraded {
		degraded = append(degraded, admin.DegradedEntry{Kind: string(d.Kind), Message: d.Message})
	}
	return admin.ConfigApplyResult{
		ApplyID:               r.ApplyID,
		OK:                    r.OK,
		Mode:                  string(r.Mode),
		Version:               r.Version,
		PersistedVersion:      r.PersistedVersion,
		DesiredVersion:        r.DesiredVersion,
		ServingVersion:        r.ServingVersion,
		Conflict:              r.Conflict,
		CurrentVersion:        r.CurrentVersion,
		Reload:                r.Reload,
		PendingRestart:        r.PendingRestart,
		Message:               r.Message,
		ValidationErrors:      r.ValidationErrors,
		RestartRequired:       r.RestartRequired,
		CanStage:              r.CanStage,
		Persisted:             r.Persisted,
		Restored:              r.Restored,
		RestoreError:          r.RestoreError,
		FinalDiskVersion:      r.FinalDiskVersion,
		FinalServingVersion:   r.FinalServingVersion,
		StagedRestartIsUpdate: r.StagedRestartIsUpdate,
		TimedOutPhase:         r.TimedOutPhase,
		FinalizationError:     r.FinalizationError,
		AuthorityDenied:       r.AuthorityDenied,
		Degraded:              degraded,
		ConfigState:           string(r.ConfigState),
		Origin:                r.Origin,
		AppOutcome:            r.AppOutcome,
	}
}

// toAdminAdoptPreviewResult converts an app-layer adoption assessment into
// the admin API preview response shape, including parsing the diff itself
// (the assessment carries only raw buffers). It is a pure projection: no
// new policy is added.
func toAdminAdoptPreviewResult(a AdoptExternalAssessment) admin.AdoptPreviewResult {
	out := admin.AdoptPreviewResult{
		OK:                 a.OK,
		Origin:             a.Origin,
		InconsistentReason: string(a.InconsistentReason),
		ObservedDigest:     a.ObservedDigest,
		BaseVersion:        a.BaselineVersion,
		CandidateVersion:   a.CandidateVersion,
		RestartRequired:    a.RestartRequired,
		ValidationErrors:   a.ValidationErrors,
	}
	if !a.OK {
		return out
	}
	if a.PreviousRaw == nil {
		// ADR 0019 §14.1: origin inconsistent means the diff is unavailable
		// because the prior snapshot is damaged, not because no prior
		// baseline ever existed — reporting no_prior_managed_state here
		// would contradict the inconsistent_reason already carried on the
		// same response.
		if a.Origin == "inconsistent" {
			out.DiffUnavailableReason = out.InconsistentReason
		} else {
			out.DiffUnavailableReason = "no_prior_managed_state"
		}
		return out
	}
	prevCfg, perr := config.Parse(a.PreviousRaw)
	candCfg, cerr := config.Parse(a.CandidateRaw)
	if perr == nil && cerr == nil {
		d := admin.DiffConfigs(prevCfg, candCfg)
		out.Diff = &d
	}
	return out
}

// buildRBACPolicy constructs an rbac.Policy from the already-secrets-expanded
// AdminConfig. It is called once at startup and once after each successful
// hot reload. The caller must pass an effective (resolved) config so token
// secret references are already expanded — the policy hashes them once and
// discards the plaintext.
func buildRBACPolicy(a config.AdminConfig) (*rbac.Policy, error) {
	customRoles := make(map[string][]string, len(a.RBAC.Roles))
	for _, r := range a.RBAC.Roles {
		customRoles[r.Name] = r.Permissions
	}
	principals := make([]rbac.PrincipalDef, 0, len(a.RBAC.Principals))
	for _, p := range a.RBAC.Principals {
		principals = append(principals, rbac.PrincipalDef{
			Name:      p.Name,
			Role:      p.Role,
			Token:     p.Token,
			Disabled:  p.Disabled,
			ExpiresAt: p.ExpiresAt,
		})
	}
	return rbac.Build(
		a.RBAC.Enabled,
		a.RBAC.DefaultRole,
		customRoles,
		principals,
		a.Token, // legacy shared token, may be empty
		time.Now(),
	)
}

// watchConfig starts a debounced file watcher for path, returning a reload
// channel. On failure it logs and returns nil (file-watch disabled).
func watchConfig(ctx context.Context, path string, log *slog.Logger) <-chan [32]byte {
	ch, err := config.WatchFile(ctx, path, 300*time.Millisecond, log)
	if err != nil {
		log.Warn("config file watch disabled", "error", err)
		return nil
	}

	out := make(chan [32]byte, 1)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-ch:
				if !ok {
					return
				}
				data, err := os.ReadFile(path)
				if err != nil {
					if log != nil {
						log.Warn("config watcher: failed to read file for digest", "path", path, "error", err)
					}
					continue
				}
				out <- sha256.Sum256(data)
			}
		}
	}()
	return out
}

// parseWorkerThreads converts a [global].worker_threads value to a GOMAXPROCS
// argument. Supported configuration paths validate the public grammar first;
// the zero result for malformed direct-call input is defense in depth only.
func parseWorkerThreads(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "auto") {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// parseReloadSeq extracts the monotonic sequence number from a managed apply /
// reload ID. It accepts both the boot-scoped "rl_<instance>_<sequence>" format
// and the legacy "rl_<sequence>" format, returning 0 for any ID that matches
// neither. The sequence is always the final underscore-delimited field; because
// every ID generated by one process shares the same boot-scoped instance,
// comparing the trailing sequence yields a correct monotonic ordering within
// the process (the boot id is deliberately not compared). Used by
// OnManagedApplyComplete to implement the C3 sequence guard.
func parseReloadSeq(id string) uint64 {
	if !strings.HasPrefix(id, "rl_") {
		return 0
	}
	rest := id[3:]
	if i := strings.LastIndex(rest, "_"); i >= 0 {
		rest = rest[i+1:]
	}
	n, err := strconv.ParseUint(rest, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// managedApplySeqGuard implements the C3/M-05 monotonic high-water sequence
// guard for managed-apply terminal callbacks. It returns true when res should
// be recorded (its sequence is strictly greater than the current high-water
// mark) and atomically advances the mark; it returns false for stale or
// out-of-order results. It prefers res.ApplyID and falls back to res.Reload.ID
// so a terminal result that somehow lacks ApplyID is still sequence-correlated
// rather than silently dropped as sequence 0.
func managedApplySeqGuard(hw *atomic.Uint64, res admin.ConfigApplyResult) bool {
	applyID := res.ApplyID
	if applyID == "" && res.Reload != nil {
		applyID = res.Reload.ID
	}
	seq := parseReloadSeq(applyID)
	for {
		prev := hw.Load()
		if seq <= prev {
			return false
		}
		if hw.CompareAndSwap(prev, seq) {
			return true
		}
	}
}

// pendingRestartCheck reports which startup-bound subsystems have changed on
// disk relative to the values the running process was built from. It compares
// effective values (secret references resolved, file-backed values digested)
// and evaluates listener rebind against the live bound-listener snapshot
// instead of the on-disk baseline (R9-11).
func pendingRestartCheck(startupCand *config.Candidate, startupFP lifecycle.Fingerprint, live server.LiveSnapshot, loadFn func() (*config.Config, error), log *slog.Logger) []string {
	current, err := loadFn()
	if err != nil || current == nil {
		return nil
	}
	candidate, err := config.NewCandidate(current)
	if err != nil {
		log.Warn("pending restart check failed: config resolution error", "error", err)
		return []string{"resolve_error"}
	}

	pendingSet := make(map[string]struct{})
	for _, path := range lifecycle.DiffAddressAware(startupFP, lifecycle.ComputeFingerprint(candidate.Effective)) {
		if e, ok := lifecycle.Lookup(path); ok {
			pendingSet[string(e.Subsystem)] = struct{}{}
		}
	}
	if _, need := server.PreflightRebindRequired(live, candidate.Effective); need {
		pendingSet["listener"] = struct{}{}
	}
	// Compare effective configs on both sides: startupCand.Raw may still
	// contain unresolved secret references (e.g. ${file:...} in ACME email),
	// while candidate.Effective is fully resolved. Using the effective startup
	// config avoids a false "acme" pending-restart warning right after boot.
	if _, need := server.ACMERestartRequired(startupCand.Effective.Servers, candidate.Effective.Servers); need {
		pendingSet["acme"] = struct{}{}
	}

	pending := make([]string, 0, len(pendingSet))
	for sub := range pendingSet {
		pending = append(pending, sub)
	}
	sort.Strings(pending)
	return pending
}

// warnInsecureBackends logs one warning per backend_tls block that disables
// certificate verification. `jul lint` already fails on the same configuration;
// this is the runtime restatement, so an operator who started the process
// anyway sees it in the log they actually read.
func warnInsecureBackends(log *slog.Logger, cfg *config.Config) {
	if log == nil || cfg == nil {
		return
	}
	for i := range cfg.Upstreams {
		if b := cfg.Upstreams[i].BackendTLS; b != nil && b.InsecureSkipVerify {
			log.Warn("backend certificate verification is disabled",
				"upstream", cfg.Upstreams[i].Name,
				"scope", "upstream")
		}
		// Boundary F: the authority a pool's addresses come from is warned about
		// on the same terms as the pool itself.
		d := cfg.Upstreams[i].Discovery
		if d == nil {
			continue
		}
		if k := d.Kubernetes; k != nil && k.InsecureSkipTLSVerify {
			log.Warn("discovery certificate verification is disabled",
				"upstream", cfg.Upstreams[i].Name,
				"scope", "discovery",
				"provider", "kubernetes")
		}
		if cs := d.Consul; cs != nil && cs.TLS != nil && cs.TLS.InsecureSkipVerify {
			log.Warn("discovery certificate verification is disabled",
				"upstream", cfg.Upstreams[i].Name,
				"scope", "discovery",
				"provider", "consul")
		}
	}
	for i := range cfg.Servers {
		for j := range cfg.Servers[i].Locations {
			loc := &cfg.Servers[i].Locations[j]
			if b := loc.BackendTLS; b != nil && b.InsecureSkipVerify {
				log.Warn("backend certificate verification is disabled",
					"listen", cfg.Servers[i].Listen,
					"path", loc.Match.Path,
					"scope", "location")
			}
		}
	}
}

// upstreamStats adapts the registry's live view to the shape the metrics
// collector consumes, so neither package has to import the other.
func upstreamStats(reg *upstream.Registry) []observability.UpstreamPoolStats {
	live := reg.Stats()
	out := make([]observability.UpstreamPoolStats, 0, len(live))
	for _, s := range live {
		byState := make(map[string]int, len(s.ByState))
		for st, n := range s.ByState {
			byState[string(st)] = n
		}
		out = append(out, observability.UpstreamPoolStats{
			Name:        s.Name,
			Active:      s.Active,
			Pending:     s.Pending,
			Connections: s.Connections,
			Eligible:    s.Eligible,
			ByState:     byState,
		})
	}
	return out
}
