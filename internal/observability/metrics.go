// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package observability provides logging and Prometheus metrics for the edge
// server.
package observability

import (
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	dto "github.com/prometheus/client_model/go"

	"jul/internal/middleware"
)

// Metrics holds the Prometheus collectors and a dedicated registry so the
// admin /metrics endpoint exposes only this server's metrics.
type Metrics struct {
	registry *prometheus.Registry

	// hostLabelEnabled controls whether the request Host is recorded as the
	// "host" label on jul_http_requests_total / jul_http_request_duration_seconds.
	// It is opt-in (default off) because Host is client-controlled and an
	// unbounded label would let a flood of distinct Host headers explode metric
	// cardinality. When disabled the label is emitted with an empty value so the
	// metric shape is stable for dashboards.
	hostLabelEnabled bool

	requests    *prometheus.CounterVec
	duration    *prometheus.HistogramVec
	inflight    prometheus.Gauge
	cacheEvents *prometheus.CounterVec
	// cacheRevalidations counts background cache revalidation outcomes. The
	// label values come from a closed set of cache-package constants.
	cacheRevalidations *prometheus.CounterVec
	compressed         *prometheus.CounterVec
	ratelimited        *prometheus.CounterVec
	clientAddrDerived  *prometheus.CounterVec
	authDecisions      *prometheus.CounterVec
	upstreamUp         *prometheus.GaugeVec
	upstreamBackends   *prometheus.GaugeVec
	admissionRejected  *prometheus.CounterVec
	retryAttempts      *prometheus.CounterVec
	retryBudgetDenied  *prometheus.CounterVec
	circuitTransitions *prometheus.CounterVec
	transportRetired   *prometheus.CounterVec
	resilience         *resilienceCollector
	discoveryErrors    *prometheus.CounterVec
	probes             *prometheus.CounterVec
	probeDuration      *prometheus.HistogramVec
	grpcTranscode      *prometheus.CounterVec
	grpcStreamMsgs     *prometheus.CounterVec
	grpcProxyCalls     prometheus.Counter
	pluginInvokes      *prometheus.CounterVec
	pluginDuration     *prometheus.HistogramVec
	pluginPanics       *prometheus.CounterVec
	listenerConns      prometheus.Gauge
	http3Conns         prometheus.Gauge
	streamConns        *prometheus.GaugeVec
	streamBytes        *prometheus.CounterVec
	streamUDPEvicted   *prometheus.CounterVec
	streamUDPReject    prometheus.Counter
	streamDialFailures *prometheus.CounterVec
	httpDialFailures   *prometheus.CounterVec
	certExpiry         *prometheus.GaugeVec
	certRenewals       prometheus.Counter
	mtlsHandshakes     *prometheus.CounterVec
	wafEvents          *prometheus.CounterVec
	egressDecisions    *prometheus.CounterVec
	egressDNSAnswers   *prometheus.CounterVec

	// Reload and staged-restart metrics (P2-05).
	reloadTotal      *prometheus.CounterVec
	reloadDuration   *prometheus.HistogramVec
	reloadInProgress prometheus.Gauge
	// reloadPhaseDuration records per-phase latency (M-04: jul_reload_phase_duration_seconds).
	reloadPhaseDuration *prometheus.HistogramVec
	// reloadTimeouts counts reloads that exceeded their deadline (M-04: jul_reload_timeout_total).
	reloadTimeouts *prometheus.CounterVec
	stageRestarts  *prometheus.CounterVec
	pendingRestart prometheus.Gauge
	// configAuthorityDrift is 1 when managed authority has detected an
	// unresolved external edit to the configuration file, 0 otherwise (ADR
	// 0019 §12/§13). No label carries a path, digest, version, or resource
	// identifier.
	configAuthorityDrift prometheus.Gauge
	// configAuthorityDenied counts a mutating request refused because the
	// process is file_owned (ADR 0019 §15), labeled by a fixed, bounded reason
	// drawn from the operation name — never a path, digest, or actor.
	configAuthorityDenied *prometheus.CounterVec
	// managedApplyFinalized counts terminal async managed-apply outcomes
	// (H-05: jul_managed_apply_finalized_total). outcome is the terminal
	// reload classification; restored is "true", "false", or "n/a".
	managedApplyFinalized *prometheus.CounterVec
	// managedApplyFinalizationErrors counts managed-apply finalization/restoration
	// failures (WS02 §3.6, WS06 §7.5: jul_managed_apply_finalization_errors_total),
	// labeled by the bounded component that failed: "restoration" (terminal
	// restoration write), "pending" (pending-registration write), "registry"
	// (terminal ledger claim/complete), or "callback_panic" (finalization callback
	// panic). component is a fixed, low-cardinality enum — never an apply ID,
	// actor, or configuration version. An increment means the failure was made
	// explicit and surfaced through logs/health/ledger rather than swallowed.
	managedApplyFinalizationErrors *prometheus.CounterVec
	// managedApplyHistory counts configuration-history snapshot attempts made by
	// the terminal finalizer (WS02 §3.7: jul_managed_apply_history_total). result
	// is the bounded snapshot disposition ("recorded", "skipped", or "failed").
	// operation is the initiating managed operation; both labels are bounded,
	// low-cardinality values — never an apply ID, actor, path or version.
	managedApplyHistory *prometheus.CounterVec
	// managedApplyRegistryEntries reports the number of terminal managed-apply
	// records currently retained in the bounded ledger (WS06 §7.5:
	// jul_managed_apply_terminal_registry_entries). It is an unlabeled gauge — the
	// retained count is a single bounded number, never keyed by apply ID, actor,
	// or version.
	managedApplyRegistryEntries prometheus.Gauge
	// managedApplyLookup counts exact-ID managed-apply lookups (WS06 §7.5:
	// jul_managed_apply_terminal_lookup_total), labeled by the bounded result
	// ("pending", "finalizing", "terminal", "missing", or "invalid"). result is
	// the only label and is a fixed low-cardinality enum — never an apply ID,
	// actor, source IP, path, or version.
	managedApplyLookup *prometheus.CounterVec

	// certMu guards certSeen, the last observed NotAfter (unix seconds) per
	// domain. It lets ObserveCertExpiry distinguish a genuine renewal (the
	// expiry moved forward) from the steady stream of cache hits that autocert
	// produces on every TLS handshake.
	certMu   sync.Mutex
	certSeen map[string]int64

	// startTime is when this Metrics (and effectively the process) was created;
	// it backs the console uptime figure.
	startTime time.Time

	// traffic holds the bounded top-N rollups of request hosts/origins/referers
	// surfaced on the Console Overview (Console v2 Milestone 1.4). It is a
	// privacy-preserving, in-memory projection and is never exported as
	// Prometheus labels (which would be unbounded cardinality).
	traffic *trafficTracker

	// samples is the bounded ring buffer of recent requests for the Console v2
	// Request Samples panel (Milestone 5.1). routeFailures is the bounded
	// per-path failure rollup for the Top Failing Routes panel (Milestone 5.2).
	// health and certs track upstream-health and certificate-renewal histories
	// (Milestones 5.5 and 5.6). All are in-memory and bounded.
	samples       *requestSampleBuffer
	routeFailures *routeFailureTracker
	health        *healthHistoryTracker
	certs         *certHistoryTracker
	egressBlocks  *egressBlockTracker

	// statsMu guards the rolling state used by Snapshot to derive
	// rate-over-time figures (requests/sec and the windowed error rate) from
	// the monotonic counters between successive polls.
	statsMu          sync.Mutex
	statsLast        time.Time
	statsLastTotal   float64
	statsLastClasses map[string]float64
}

// MetricsOption customises a Metrics at construction time.
type MetricsOption func(*Metrics)

// WithHostLabel enables (or disables) the per-request "host" label on the HTTP
// request counter and latency histogram. It is off by default: the Host header
// is client-controlled, so an attacker sending many distinct values could
// otherwise drive unbounded metric cardinality.
func WithHostLabel(on bool) MetricsOption {
	return func(m *Metrics) { m.hostLabelEnabled = on }
}

// NewMetrics creates and registers the collectors on a private registry.
func NewMetrics(opts ...MetricsOption) *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_requests_total",
			Help: "Total HTTP requests handled, labeled by method, host, and status code.",
		}, []string{"method", "host", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_http_request_duration_seconds",
			Help:    "HTTP request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "host"}),
		inflight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_http_requests_in_flight",
			Help: "Number of HTTP requests currently being served.",
		}),
		cacheEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_cache_events_total",
			Help: "Response cache outcomes, labeled by state (HIT/MISS/STALE/BYPASS).",
		}, []string{"state"}),
		cacheRevalidations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_cache_revalidations_total",
			Help: "Cache validation and revalidation decisions, labeled by bounded outcome (stored/not_modified/uncacheable/origin_error/canceled/panic/no_lease/deduplicated).",
		}, []string{"outcome"}),
		compressed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_response_compressed_total",
			Help: "Responses compressed by the edge, labeled by content coding.",
		}, []string{"encoding"}),
		ratelimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_ratelimited_total",
			Help: "Requests rejected by rate limiting, labeled by key kind (ip/header/jwt).",
		}, []string{"key"}),
		clientAddrDerived: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_client_addr_derivations_total",
			Help: "Canonical client-address derivations, labeled by source (peer/forwarded/xff) and result (accepted/untrusted_peer/malformed/too_many_hops).",
		}, []string{"source", "result"}),
		authDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_auth_decisions_total",
			Help: "Access-control decisions, labeled by method (cidr/basic/jwt/forward) and result (allow/deny).",
		}, []string{"method", "result"}),
		wafEvents: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_waf_events_total",
			Help: "Web-application-firewall rule matches, labeled by action (block/detect) and matched rule ID.",
		}, []string{"action", "rule"}),
		egressDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_egress_decisions_total",
			Help: "Outbound egress allow-list decisions, labeled by subsystem, result (allow/block), and reason (empty on allow).",
		}, []string{"subsystem", "result", "reason"}),
		egressDNSAnswers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_egress_dns_answers_total",
			Help: "Egress CIDR-only hostname resolutions evaluated, labeled by subsystem and result (allow/block).",
		}, []string{"subsystem", "result"}),
		upstreamUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_upstream_backends_healthy",
			Help: "Backends a pool's active health checks currently consider healthy, labeled by pool.",
		}, []string{"pool"}),
		admissionRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_upstream_admission_rejected_total",
			Help: "Requests refused before reaching a backend, labeled by pool and bounded reason.",
		}, []string{"pool", "reason"}),
		retryAttempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_upstream_retry_attempts_total",
			Help: "Retry attempts, labeled by pool and the bounded outcome that ended the sequence.",
		}, []string{"pool", "outcome"}),
		retryBudgetDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_upstream_retry_budget_denied_total",
			Help: "Retries suppressed because the pool's retry budget was spent, labeled by pool.",
		}, []string{"pool"}),
		circuitTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_upstream_circuit_transitions_total",
			Help: "Backend circuit transitions, labeled by pool and destination state.",
		}, []string{"pool", "to"}),
		transportRetired: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_transport_retired_total",
			Help: "Handler-generation transports retired, labeled by mode (graceful/forced).",
		}, []string{"mode"}),
		resilience: newResilienceCollector(),
		upstreamBackends: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_upstream_backends",
			Help: "Current number of backends in a pool, labeled by pool (tracks dynamic service discovery).",
		}, []string{"pool"}),
		discoveryErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_discovery_errors_total",
			Help: "Failed or empty service-discovery resolves, labeled by pool (last-good backends are kept).",
		}, []string{"pool"}),
		probes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_upstream_probes_total",
			Help: "Active health-check probes, labeled by pool, result (success/failure) and the proxy surface that owns the checker (http/stream).",
		}, []string{"pool", "result", "source"}),
		probeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_upstream_probe_duration_seconds",
			Help:    "Active health-check probe latency in seconds, labeled by pool and the proxy surface that owns the checker (http/stream).",
			Buckets: prometheus.DefBuckets,
		}, []string{"pool", "source"}),
		grpcTranscode: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_grpc_transcode_requests_total",
			Help: "gRPC-JSON transcoding requests, labeled by gRPC method full name and HTTP status code.",
		}, []string{"method", "code"}),
		grpcStreamMsgs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_grpc_transcode_stream_msgs_total",
			Help: "gRPC-JSON transcoding streamed messages, labeled by gRPC method full name and direction (sent to backend / received from backend).",
		}, []string{"method", "direction"}),
		grpcProxyCalls: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jul_grpc_proxy_streams_total",
			Help: "Native gRPC calls forwarded by the HTTP/2 passthrough proxy (one per call, including each streaming call).",
		}),
		pluginInvokes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_plugin_invocations_total",
			Help: "WASM plugin invocations, labeled by plugin name and result (continue/stop/error).",
		}, []string{"plugin", "result"}),
		pluginDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_plugin_duration_seconds",
			Help:    "WASM plugin invocation latency in seconds, labeled by plugin name.",
			Buckets: prometheus.DefBuckets,
		}, []string{"plugin"}),
		pluginPanics: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_plugin_panics_total",
			Help: "WASM plugin traps/panics contained by the host, labeled by plugin name.",
		}, []string{"plugin"}),
		listenerConns: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_listener_conns",
			Help: "Current concurrent connections across all listeners.",
		}),
		http3Conns: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_http3_connections",
			Help: "Current open HTTP/3 (QUIC) connections across all listeners.",
		}),
		streamConns: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_stream_active_conns",
			Help: "Current active L4 stream connections/sessions, labeled by protocol (tcp/udp).",
		}, []string{"proto"}),
		streamBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_stream_bytes_total",
			Help: "Bytes relayed by the L4 stream proxy, labeled by protocol (tcp/udp) and direction (up to backend / down to client).",
		}, []string{"proto", "direction"}),
		streamUDPEvicted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_stream_udp_sessions_evicted_total",
			Help: "UDP sessions removed by the L4 stream proxy to enforce limits, labeled by reason: 'idle' (reaped after idle_timeout) or 'lru' (reclaimed to admit a new client at the session cap).",
		}, []string{"reason"}),
		streamUDPReject: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jul_stream_udp_sessions_rejected_total",
			Help: "New UDP clients dropped because a listener's max_udp_sessions cap was reached and no session was reclaimable.",
		}),
		streamDialFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_stream_backend_dial_failures_total",
			Help: "L4 stream backend dial/connect failures, labeled by protocol (tcp/udp) and a bounded reason (timeout/refused/no_backend/other). The accompanying log line is throttled once a backend is already known to be down; this counter is not.",
		}, []string{"proto", "reason"}),
		httpDialFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_backend_dial_failures_total",
			Help: "HTTP reverse-proxy backend dial/connect failures, labeled by a bounded reason (timeout/refused/no_backend/other). Excludes client-cancelled requests and backend-TLS-identity failures, which are accounted separately. The accompanying log line is throttled once a backend is already known to be down; this counter is not.",
		}, []string{"reason"}),
		certExpiry: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_tls_cert_expiry_seconds",
			Help: "Leaf certificate expiry as a Unix timestamp, labeled by domain.",
		}, []string{"domain"}),
		certRenewals: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jul_acme_renewals_total",
			Help: "ACME certificate renewals observed (expiry advanced for a domain).",
		}),
		mtlsHandshakes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_mtls_handshakes_total",
			Help: "Mutual-TLS handshakes presenting a CA-verified client certificate, labeled by result (verified/rejected). Certificates failing CA-chain verification are rejected by the TLS stack before this counter; a missing certificate denied per location is counted as a 403 in jul_http_requests_total.",
		}, []string{"result"}),
		certSeen: make(map[string]int64),
		traffic:  newTrafficTracker(),

		reloadTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_reload_total",
			Help: "Configuration reloads, labeled by source (admin/sighup/watch) and outcome (applied_live/applied_degraded/not_applied/saved_not_live).",
		}, []string{"source", "outcome"}),
		reloadDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_reload_duration_seconds",
			Help:    "Configuration reload latency in seconds, labeled by source and outcome.",
			Buckets: []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30},
		}, []string{"source", "outcome"}),
		reloadInProgress: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_reload_in_progress",
			Help: "1 while a configuration reload transaction is in flight; 0 otherwise.",
		}),
		stageRestarts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_config_stage_restart_total",
			Help: "Staged-restart apply operations, labeled by result (created/updated/discarded/failed).",
		}, []string{"result"}),
		pendingRestart: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_config_pending_restart",
			Help: "1 when a managed staged-restart candidate is pending (waiting for process restart); 0 otherwise.",
		}),
		configAuthorityDrift: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_config_authority_drift",
			Help: "1 when managed configuration authority has detected an unresolved external edit to the configuration file; 0 otherwise.",
		}),
		configAuthorityDenied: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_config_authority_denied_total",
			Help: "Mutating configuration requests refused because the process is file_owned, labeled by the bounded operation name.",
		}, []string{"reason"}),
		managedApplyFinalized: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_managed_apply_finalized_total",
			Help: "Terminal async managed-apply outcomes, labeled by operation, mode, outcome and whether restoration succeeded (true/false/n/a).",
		}, []string{"operation", "mode", "outcome", "restored"}),
		managedApplyFinalizationErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_managed_apply_finalization_errors_total",
			Help: "Managed-apply finalization/restoration failures, labeled by the bounded component that failed (restoration/pending/registry/callback_panic). An increment means the failure was made explicit and surfaced through logs/health/ledger rather than silently discarded.",
		}, []string{"component"}),
		managedApplyHistory: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_managed_apply_history_total",
			Help: "Configuration-history snapshot attempts made by the terminal managed-apply finalizer (WS02 §3.7), labeled by operation and result (recorded/skipped/failed).",
		}, []string{"operation", "result"}),
		managedApplyRegistryEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jul_managed_apply_terminal_registry_entries",
			Help: "Number of terminal managed-apply records currently retained in the bounded ledger.",
		}),
		managedApplyLookup: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_managed_apply_terminal_lookup_total",
			Help: "Exact-ID managed-apply lookups, labeled by bounded result (pending/finalizing/terminal/missing/invalid).",
		}, []string{"result"}),
		reloadPhaseDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_reload_phase_duration_seconds",
			Help:    "Latency of individual reload phases (resolve/validate/lifecycle/prepare/stage_listeners/publish/activate), labeled by phase and outcome.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
		}, []string{"phase", "outcome"}),
		reloadTimeouts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_reload_timeout_total",
			Help: "Configuration reloads that exceeded their deadline, labeled by the phase that timed out.",
		}, []string{"phase"}),

		samples:       newRequestSampleBuffer(requestSampleCap),
		routeFailures: newRouteFailureTracker(routeFailureCap),
		health:        newHealthHistoryTracker(),
		certs:         newCertHistoryTracker(),
		egressBlocks:  newEgressBlockTracker(),
	}
	m.startTime = time.Now()
	reg.MustRegister(
		m.admissionRejected,
		m.retryAttempts,
		m.retryBudgetDenied,
		m.circuitTransitions,
		m.transportRetired,
		m.resilience,
		m.requests,
		m.duration,
		m.inflight,
		m.cacheEvents,
		m.cacheRevalidations,
		m.compressed,
		m.ratelimited,
		m.clientAddrDerived,
		m.authDecisions,
		m.upstreamUp,
		m.upstreamBackends,
		m.discoveryErrors,
		m.probes,
		m.probeDuration,
		m.grpcTranscode,
		m.grpcStreamMsgs,
		m.grpcProxyCalls,
		m.pluginInvokes,
		m.pluginDuration,
		m.pluginPanics,
		m.listenerConns,
		m.http3Conns,
		m.streamConns,
		m.streamBytes,
		m.streamUDPEvicted,
		m.streamUDPReject,
		m.streamDialFailures,
		m.httpDialFailures,
		m.certExpiry,
		m.certRenewals,
		m.mtlsHandshakes,
		m.wafEvents,
		m.egressDecisions,
		m.egressDNSAnswers,
		m.reloadTotal,
		m.reloadDuration,
		m.reloadInProgress,
		m.reloadPhaseDuration,
		m.reloadTimeouts,
		m.stageRestarts,
		m.pendingRestart,
		m.configAuthorityDrift,
		m.configAuthorityDenied,
		m.managedApplyFinalized,
		m.managedApplyFinalizationErrors,
		m.managedApplyHistory,
		m.managedApplyRegistryEntries,
		m.managedApplyLookup,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// Handler returns the Prometheus exposition handler for this registry.
// Gather returns the currently exported metric families. It exists for tests
// and tooling that need to inspect labels without scraping over HTTP.
func (m *Metrics) Gather() ([]*dto.MetricFamily, error) { return m.registry.Gather() }

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Middleware instruments next with request counters, latency, in-flight gauge,
// and cache-state accounting (read from the X-Cache response header).
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.inflight.Inc()
		defer m.inflight.Dec()

		start := time.Now()
		rec := middleware.NewRecorder(w)
		next.ServeHTTP(rec.Writer(), r)

		host := hostLabel(r.Host)
		if !m.hostLabelEnabled {
			// Opt-out (default): collapse the client-controlled Host to a single
			// empty series so per-host cardinality cannot grow unbounded.
			host = ""
		}
		// The request method is client-controlled — HTTP permits arbitrary
		// method tokens — so it is normalized to a fixed set before it becomes a
		// label; anything unrecognized collapses to "other" so a flood of bogus
		// methods cannot explode cardinality (see the metric label policy in
		// docs/core-http.md).
		method := methodLabel(r.Method)
		m.requests.WithLabelValues(method, host, strconv.Itoa(rec.Status())).Inc()
		m.duration.WithLabelValues(method, host).Observe(time.Since(start).Seconds())
		if state := rec.Header().Get("X-Cache"); state != "" {
			m.cacheEvents.WithLabelValues(state).Inc()
		}
		// Fold the request into the bounded traffic-source rollups. Only the Host
		// and the Origin/Referer hostnames are retained — never the path, query
		// string, or any credential header (Console v2 Milestone 1.4).
		m.traffic.record(r.Host, r.Header.Get("Origin"), r.Header.Get("Referer"), r.Method)

		// Capture a privacy-preserving sample of the request and fold its outcome
		// into the per-path failure rollup (Console v2 Milestones 5.1 and 5.2).
		durationMs := time.Since(start).Seconds() * 1000
		status := rec.Status()
		m.samples.record(RequestSample{
			Time:        start.UTC(),
			Method:      r.Method,
			Path:        r.URL.Path,
			Host:        host,
			Status:      status,
			DurationMs:  durationMs,
			CacheState:  rec.Header().Get("X-Cache"),
			Compressed:  rec.Header().Get("Content-Encoding") != "",
			RateLimited: status == http.StatusTooManyRequests,
			Origin:      r.Header.Get("Origin"),
			UserAgent:   r.Header.Get("User-Agent"),
		})
		m.routeFailures.record(r.URL.Path, status, durationMs)
	})
}

// RequestSamples returns the bounded ring buffer of recent requests, newest
// first, for the Console v2 Request Samples panel (Milestone 5.1).
func (m *Metrics) RequestSamples() []RequestSample {
	return m.samples.snapshot()
}

// FailingRoutes returns the top n paths ranked by recent failures for the
// Console v2 Top Failing Routes panel (Milestone 5.2). A non-positive n returns
// all tracked failing paths.
func (m *Metrics) FailingRoutes(n int) []RouteFailure {
	return m.routeFailures.snapshot(n)
}

// UpstreamHealthHistory returns the per-backend up/down history for the Console
// v2 Upstream Health History panel (Milestone 5.5).
func (m *Metrics) UpstreamHealthHistory() []BackendHealthHistory {
	return m.health.snapshot()
}

// CertRenewalHistory returns the per-domain certificate renewal history for the
// Console v2 Certificate Renewal History panel (Milestone 5.6).
func (m *Metrics) CertRenewalHistory() []CertRenewalHistory {
	return m.certs.snapshot()
}

// TrafficSnapshot returns the current bounded traffic-source rollups for the
// Console Overview. It is safe to call concurrently.
func (m *Metrics) TrafficSnapshot() TrafficSources {
	return m.traffic.snapshot()
}

// ObserveCompression records that a response was compressed with the given
// content coding. It is wired into the Compression middleware as its OnCompress
// hook (the middleware package cannot import observability directly).
func (m *Metrics) ObserveCompression(encoding string) {
	m.compressed.WithLabelValues(encoding).Inc()
}

// ObserveCacheRevalidation records the outcome of one background cache
// revalidation decision. outcome is one of the cache package's own bounded
// constants — never a cache key, URL, host, or error string — so the label set
// stays fixed. It is wired into the cache as its revalidation observer (the
// cache package cannot import observability directly).
func (m *Metrics) ObserveCacheRevalidation(outcome string) {
	m.cacheRevalidations.WithLabelValues(outcome).Inc()
}

// ObserveRateLimited records that a request was rejected by rate limiting. kind
// is the key KIND (ip/header/jwt), not the raw client value, so metric
// cardinality stays bounded. It is wired into the RateLimit middleware as its
// onLimited hook (the middleware package cannot import observability directly).
func (m *Metrics) ObserveRateLimited(kind string) {
	m.ratelimited.WithLabelValues(kind).Inc()
}

// ObserveClientAddrDerivation records how one request's canonical client address
// was derived. Both labels are the bounded enums from internal/clientaddr, never
// an address, so cardinality is at most twelve series.
//
// It is what makes a degraded derivation alertable: a run of malformed or
// too_many_hops from a trusted peer is an attempt to pad a forwarding header
// past its bounds, which per-request logs record but cannot be alerted on.
func (m *Metrics) ObserveClientAddrDerivation(source, result string) {
	m.clientAddrDerived.WithLabelValues(source, result).Inc()
}

// ObserveAuthDecision records an access-control decision. method is the gate
// that produced it (cidr/basic/jwt/forward) and result is allow or deny, both
// fixed-cardinality labels. It is wired into the auth middleware as its
// OnDecision hook (the auth package cannot import observability directly).
func (m *Metrics) ObserveAuthDecision(method, result string) {
	m.authDecisions.WithLabelValues(method, result).Inc()
}

// ObserveWAFEvent records a web-application-firewall rule match. action is the
// enforcement mode (block/detect) and rule is the matched rule's ID; both are
// fixed/low-cardinality. It is wired into the WAF as its OnEvent hook (the waf
// package cannot import observability directly).
func (m *Metrics) ObserveWAFEvent(action, rule string) {
	m.wafEvents.WithLabelValues(action, rule).Inc()
}

// ObserveBackendHealth records an active health-check transition for a backend.
// It is wired into the upstream pool registry as its OnHealth hook (the upstream
// package follows the codebase's callback convention rather than importing
// observability).
//
// The backend address deliberately reaches the Console history tracker and not
// a metric label: a pod address is unbounded under churn, and per-backend detail
// belongs to an API queried on demand rather than a series retained forever.
func (m *Metrics) ObserveBackendHealth(pool, backend string, healthy bool) {
	// The tracker only appends on an actual state change.
	m.health.record(pool, backend, healthy)
}

// ObserveBackendsHealthy records how many of a pool's backends the active health
// checks currently consider healthy. It is level-triggered — the caller reports
// a count derived from current state rather than an increment — so a missed
// event cannot leave the gauge permanently wrong.
func (m *Metrics) ObserveBackendsHealthy(pool string, n int) {
	m.upstreamUp.WithLabelValues(pool).Set(float64(n))
}

// RetirePool deletes every series belonging to a pool that no longer exists.
//
// Pool names are bounded at one configuration snapshot but not over the life of
// the process: upstream_add and upstream_remove are supported admin patch
// operations, so an operator can churn pool names indefinitely and every one of
// them used to keep its series until restart. Prometheus never expires a series
// on its own, so this has to be explicit.
func (m *Metrics) RetirePool(pool string) {
	labels := prometheus.Labels{"pool": pool}
	m.upstreamUp.Delete(labels)
	m.upstreamBackends.Delete(labels)
	m.discoveryErrors.Delete(labels)
	m.probeDuration.DeletePartialMatch(labels)
	// probes carries a second label, so the whole family for this pool goes at
	// once rather than one result value at a time.
	m.probes.DeletePartialMatch(labels)
	m.admissionRejected.DeletePartialMatch(labels)
	m.retryAttempts.DeletePartialMatch(labels)
	m.retryBudgetDenied.Delete(labels)
	m.circuitTransitions.DeletePartialMatch(labels)
}

// ObserveUpstreamBackends records the current backend count of a pool as a
// gauge. It is wired into the upstream pool registry as its OnBackends hook and
// tracks dynamic service discovery (the count changes as a discoverer resolves
// new endpoint sets).
func (m *Metrics) ObserveUpstreamBackends(pool string, n int) {
	m.upstreamBackends.WithLabelValues(pool).Set(float64(n))
}

// ObserveAdmissionRejected counts a request refused before it reached a
// backend. reason is a value from the upstream failure taxonomy; the caller
// passes the string form so this package need not import internal/upstream.
func (m *Metrics) ObserveAdmissionRejected(pool, reason string) {
	m.admissionRejected.WithLabelValues(pool, reason).Inc()
}

// ObserveRetryAttempt counts one retry attempt and the bounded outcome that
// ended its sequence.
func (m *Metrics) ObserveRetryAttempt(pool, outcome string) {
	m.retryAttempts.WithLabelValues(pool, outcome).Inc()
}

// ObserveRetryBudgetDenied counts a retry suppressed by a spent budget. It is
// separate from the rejection counter because a denied retry is not a rejected
// request: the client still gets the last attempt's answer.
func (m *Metrics) ObserveRetryBudgetDenied(pool string) {
	m.retryBudgetDenied.WithLabelValues(pool).Inc()
}

// ObserveCircuitTransition counts a backend circuit entering a new state. The
// backend is not a label: per-backend detail is a runtime-API concern and an
// address is unbounded.
func (m *Metrics) ObserveCircuitTransition(pool, to string) {
	m.circuitTransitions.WithLabelValues(pool, to).Inc()
}

// ObserveTransportRetired counts a handler-generation transport retirement.
// mode is "graceful" when the generation drained and "forced" when it was cut
// short, which is the difference between a clean reload and one that dropped
// in-flight work.
func (m *Metrics) ObserveTransportRetired(mode string) {
	m.transportRetired.WithLabelValues(mode).Inc()
}

// ObserveDiscoveryError records a failed or empty service-discovery resolve. It
// is wired into the upstream pool registry as its OnDiscoveryError hook; the
// pool keeps its last-good backends when this fires.
func (m *Metrics) ObserveDiscoveryError(pool string) {
	m.discoveryErrors.WithLabelValues(pool).Inc()
}

// ObserveEgressDecision records an outbound egress allow-list decision. The
// labels are the bounded subsystem/result/reason set only; the destination host
// and IP are deliberately never labels. reason is empty on an allow. It is wired
// as the egress policy observer via a small adapter so this package need not
// import internal/egress.
func (m *Metrics) ObserveEgressDecision(subsystem, result, reason string, dnsAnswers int) {
	m.egressDecisions.WithLabelValues(subsystem, result, reason).Inc()
	if dnsAnswers > 0 {
		m.egressDNSAnswers.WithLabelValues(subsystem, result).Inc()
	}
	if result == "block" {
		m.egressBlocks.add(subsystem, reason)
	}
}

// EgressBlocked returns the bounded per-subsystem/reason tally of egress
// allow-list blocks for the Console Security panel. It carries no destination
// host or IP.
func (m *Metrics) EgressBlocked() []EgressBlockedCount {
	return m.egressBlocks.snapshot()
}

// ObserveProbe records the outcome and latency of a single active health-check
// probe. It is wired into the upstream pool registry as its OnProbe hook.
func (m *Metrics) ObserveProbe(pool, source string, success bool, latency time.Duration) {
	result := "failure"
	if success {
		result = "success"
	}
	m.probes.WithLabelValues(pool, result, source).Inc()
	m.probeDuration.WithLabelValues(pool, source).Observe(latency.Seconds())
}

// ObserveGRPCTranscode counts a gRPC-JSON transcoding request by the gRPC method
// full name and the HTTP status code returned to the client. It is wired into
// each transcoding handler as its OnResult hook. A request that fails to match
// any route is recorded with an empty method.
func (m *Metrics) ObserveGRPCTranscode(method, code string) {
	m.grpcTranscode.WithLabelValues(method, code).Inc()
}

// ObserveGRPCTranscodeStreamMsg counts one streamed transcoding message by the
// gRPC method full name and direction ("sent" to the backend, "recv" from the
// backend). It is wired into each transcoding handler as its OnStreamMsg hook.
func (m *Metrics) ObserveGRPCTranscodeStreamMsg(method, direction string) {
	m.grpcStreamMsgs.WithLabelValues(method, direction).Inc()
}

// ObserveGRPCProxyStream counts one native gRPC call forwarded by the HTTP/2
// passthrough proxy. It is wired into each gRPC proxy handler as its onStream
// hook and fires once per call (including each streaming call).
func (m *Metrics) ObserveGRPCProxyStream() {
	m.grpcProxyCalls.Inc()
}

// ObservePluginInvocation counts a WASM plugin invocation by plugin name and
// result ("continue", "stop", or "error") and records its latency. It is wired
// into the plugin set as its metrics hook.
func (m *Metrics) ObservePluginInvocation(plugin, result string, latency time.Duration) {
	m.pluginInvokes.WithLabelValues(plugin, result).Inc()
	m.pluginDuration.WithLabelValues(plugin).Observe(latency.Seconds())
}

// ObservePluginPanic counts a WASM plugin trap or panic that the host contained
// (turning it into a 500 while keeping the server alive).
func (m *Metrics) ObservePluginPanic(plugin string) {
	m.pluginPanics.WithLabelValues(plugin).Inc()
}

// ObserveCertExpiry records a certificate's leaf expiry for domain and counts a
// renewal when the expiry advances past the previously seen value. It is wired
// into the ACME certificate provider (the server package cannot import
// observability directly). The first observation for a domain sets the gauge
// without counting a renewal.
func (m *Metrics) ObserveCertExpiry(domain string, notAfter time.Time) {
	ts := notAfter.Unix()
	m.certExpiry.WithLabelValues(domain).Set(float64(ts))

	m.certMu.Lock()
	prev, ok := m.certSeen[domain]
	if ok && ts > prev {
		m.certRenewals.Inc()
		// Record the renewal for the Console v2 certificate-history panel
		// (Milestone 5.6). Issuer/staging are unknown at this hook and left
		// blank; the advancing expiry is the renewal signal.
		m.certs.recordRenewal(domain, notAfter, "", false)
	}
	m.certSeen[domain] = ts
	m.certMu.Unlock()
}

// ObserveCertRenewalError records a failed certificate renewal attempt for the
// Console v2 certificate-history panel (Milestone 5.6). errMsg should already be
// a short, non-sensitive description (no key material, no tokens).
func (m *Metrics) ObserveCertRenewalError(domain, errMsg string) {
	m.certs.recordError(domain, errMsg)
}

// ConnState maintains the listenerConns gauge of concurrent connections. It is
// installed as http.Server.ConnState (via Server.ConnStateHook) so the server
// package stays decoupled from observability.
func (m *Metrics) ConnState(_ net.Conn, state http.ConnState) {
	switch state {
	case http.StateNew:
		m.listenerConns.Inc()
	case http.StateHijacked, http.StateClosed:
		m.listenerConns.Dec()
	}
}

// HTTP3ConnDelta adjusts the jul_http3_connections gauge by delta (+1 when a
// QUIC connection opens, -1 when it closes). It is installed as the HTTP/3
// listener's connection hook (via Server.HTTP3ConnHook) so the server package
// stays decoupled from observability.
func (m *Metrics) HTTP3ConnDelta(delta int64) {
	m.http3Conns.Add(float64(delta))
}

// ObserveMTLSHandshake records a mutual-TLS handshake that presented a
// CA-verified client certificate, with result "verified" or "rejected". It is
// installed as Server.MTLSResultHook so the server package stays decoupled from
// observability.
func (m *Metrics) ObserveMTLSHandshake(result string) {
	m.mtlsHandshakes.WithLabelValues(result).Inc()
}

// StreamConnDelta adjusts the jul_stream_active_conns gauge for proto by delta
// (+1 when an L4 connection/session opens, -1 when it closes). It is supplied to
// the stream proxy so that package stays decoupled from observability.
func (m *Metrics) StreamConnDelta(proto string, delta int64) {
	m.streamConns.WithLabelValues(proto).Add(float64(delta))
}

// ObserveStreamBytes adds n bytes relayed by the L4 stream proxy for proto in
// the given direction ("up" to backend or "down" to client).
func (m *Metrics) ObserveStreamBytes(proto, direction string, n int64) {
	if n <= 0 {
		return
	}
	m.streamBytes.WithLabelValues(proto, direction).Add(float64(n))
}

// StreamUDPEvicted counts a UDP session removed to enforce limits, by reason
// ("idle" reaped after idle_timeout or "lru" reclaimed at the session cap). It
// is supplied to the stream proxy so that package stays decoupled from metrics.
func (m *Metrics) StreamUDPEvicted(reason string) {
	m.streamUDPEvicted.WithLabelValues(reason).Inc()
}

// StreamUDPRejected counts a new UDP client dropped because a listener's
// max_udp_sessions cap was reached and no session was reclaimable.
func (m *Metrics) StreamUDPRejected() {
	m.streamUDPReject.Inc()
}

// ObserveStreamDialFailure counts an L4 stream backend dial/connect failure,
// labeled by protocol ("tcp"/"udp") and a bounded reason from
// upstream.ClassifyDialError. It is supplied to the stream proxy so that
// package stays decoupled from observability.
func (m *Metrics) ObserveStreamDialFailure(proto, reason string) {
	m.streamDialFailures.WithLabelValues(proto, reason).Inc()
}

// ObserveHTTPDialFailure counts an HTTP reverse-proxy backend dial/connect
// failure, labeled by a bounded reason from upstream.ClassifyDialError. It is
// supplied to the proxy handler so that package stays decoupled from
// observability.
func (m *Metrics) ObserveHTTPDialFailure(reason string) {
	m.httpDialFailures.WithLabelValues(reason).Inc()
}

// ObserveReload records the outcome and duration of a completed hot reload
// (P2-05). source is the reload trigger ("admin", "sighup", or "watch");
// outcome is the terminal classification ("applied_live", "applied_degraded",
// "not_applied", or "saved_not_live"); durationMs is the reload wall time.
// inProgress must be decremented by the caller just before calling this.
func (m *Metrics) ObserveReload(source, outcome string, durationMs int64) {
	m.reloadTotal.WithLabelValues(source, outcome).Inc()
	if durationMs > 0 {
		m.reloadDuration.WithLabelValues(source, outcome).Observe(float64(durationMs) / 1000.0)
	}
	m.reloadInProgress.Dec()
}

// ObserveReloadResult records per-phase durations and timeout counts from the
// full ReloadResult (M-04: jul_reload_phase_duration_seconds, jul_reload_timeout_total).
// It is called alongside ObserveReload so both sets of metrics are always current.
// outcome is the terminal classification string; phaseDurations is the per-phase
// timing map from ReloadResult.PhaseDurations; timedOut/timedOutPhase come from
// the ReloadResult.TimedOut/TimedOutPhase fields.
func (m *Metrics) ObserveReloadResult(outcome string, phaseDurations map[string]int64, timedOut bool, timedOutPhase string) {
	if timedOut {
		if timedOutPhase == "" {
			timedOutPhase = "unknown"
		}
		m.reloadTimeouts.WithLabelValues(timedOutPhase).Inc()
	}
	for phase, ms := range phaseDurations {
		m.reloadPhaseDuration.WithLabelValues(phase, outcome).Observe(float64(ms) / 1000.0)
	}
}

// ReloadStarted increments the in-progress gauge at the beginning of a reload
// transaction. The caller must pair it with ObserveReload (which decrements).
func (m *Metrics) ReloadStarted() {
	m.reloadInProgress.Inc()
}

// ObserveStageRestart records the outcome of a stage_restart apply operation
// (P2-05). result is one of "created", "updated", "discarded", or "failed".
func (m *Metrics) ObserveStageRestart(result string) {
	m.stageRestarts.WithLabelValues(result).Inc()
}

// ObserveManagedApplyFinalized records the terminal async outcome of a managed
// apply (H-05, AC-04). operation is the initiating managed operation
// (config.apply/config.patch/config.raw/config.settings/config.rollback);
// mode is the apply mode (hot/stage_restart); outcome is the terminal reload
// classification; restored is "true", "false", or "n/a" when restoration was
// not applicable. All labels are bounded, low-cardinality values — never an
// apply ID, actor, path or version.
func (m *Metrics) ObserveManagedApplyFinalized(operation, mode, outcome, restored string) {
	if operation == "" {
		operation = "unknown"
	}
	if mode == "" {
		mode = "unknown"
	}
	m.managedApplyFinalized.WithLabelValues(operation, mode, outcome, restored).Inc()
}

// ObserveManagedApplyFinalizationError counts a managed-apply finalization or
// restoration failure (WS02 §3.6, WS06 §7.5). component is the bounded machinery
// component that failed: "restoration", "pending", "registry", or
// "callback_panic". It is a fixed, low-cardinality enum so the failure is
// visible without leaking apply IDs, actors, or configuration versions as
// unbounded labels; an empty value is normalized to "unknown".
func (m *Metrics) ObserveManagedApplyFinalizationError(component string) {
	if component == "" {
		component = "unknown"
	}
	m.managedApplyFinalizationErrors.WithLabelValues(component).Inc()
}

// SetManagedApplyRegistryEntries publishes the number of terminal managed-apply
// records currently retained in the bounded ledger (WS06 §7.5). The value is a
// single bounded count, never keyed by apply ID, actor, or version.
func (m *Metrics) SetManagedApplyRegistryEntries(n int) {
	m.managedApplyRegistryEntries.Set(float64(n))
}

// ObserveManagedApplyLookup counts one exact-ID managed-apply lookup (WS06
// §7.5). result is the bounded lookup disposition ("pending", "finalizing",
// "terminal", "missing", "forbidden", or "invalid"); an empty value is
// normalized to "unknown". result is the only label and is a fixed
// low-cardinality enum — never an apply ID, actor, source IP, path, or version.
func (m *Metrics) ObserveManagedApplyLookup(result string) {
	if result == "" {
		result = "unknown"
	}
	m.managedApplyLookup.WithLabelValues(result).Inc()
}

// ObserveManagedApplyHistory records the disposition of a configuration-history
// snapshot attempt made by the terminal managed-apply finalizer (WS02 §3.7).
// operation is the initiating managed operation; result is the bounded snapshot
// disposition ("recorded", "skipped", or "failed"). Both labels are bounded,
// low-cardinality values — never an apply ID, actor, path or version.
func (m *Metrics) ObserveManagedApplyHistory(operation, result string) {
	if operation == "" {
		operation = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.managedApplyHistory.WithLabelValues(operation, result).Inc()
}

// SetPendingRestart sets the pending-restart gauge to 1 when a managed staged
// restart is pending, or 0 when none is active (P2-05).
func (m *Metrics) SetPendingRestart(pending bool) {
	if pending {
		m.pendingRestart.Set(1)
	} else {
		m.pendingRestart.Set(0)
	}
}

// SetConfigAuthorityDrift sets the managed-authority drift gauge (ADR 0019
// §12/§13). It carries no path, digest, or version — only a boolean.
func (m *Metrics) SetConfigAuthorityDrift(drift bool) {
	if drift {
		m.configAuthorityDrift.Set(1)
	} else {
		m.configAuthorityDrift.Set(0)
	}
}

// ObserveConfigAuthorityDenied counts one mutating request refused because
// the process is file_owned (ADR 0019 §15). reason is a bounded operation
// label (e.g. "config.raw", "config.patch") — never a path or actor.
func (m *Metrics) ObserveConfigAuthorityDenied(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	m.configAuthorityDenied.WithLabelValues(reason).Inc()
}

// hostLabel strips the port so metric cardinality stays bounded by hostname.
func hostLabel(host string) string {
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}

// knownMethods is the fixed allow-list of HTTP request methods recorded verbatim
// on the request metrics. HTTP permits arbitrary method tokens and the method is
// client-controlled, so any value outside this set collapses to "other" (see
// methodLabel). This bounds the "method" label to at most len(knownMethods)+1
// series per host/code combination by construction.
var knownMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodConnect: {},
	http.MethodOptions: {},
	http.MethodTrace:   {},
}

// methodLabel maps a request method to a bounded metric label value: a
// recognized method is returned unchanged, anything else becomes "other" so a
// client cannot drive unbounded cardinality with novel method tokens.
func methodLabel(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return "other"
}
