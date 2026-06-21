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

	"jul/internal/middleware"
)

// Metrics holds the Prometheus collectors and a dedicated registry so the
// admin /metrics endpoint exposes only this server's metrics.
type Metrics struct {
	registry *prometheus.Registry

	requests         *prometheus.CounterVec
	duration         *prometheus.HistogramVec
	inflight         prometheus.Gauge
	cacheEvents      *prometheus.CounterVec
	compressed       *prometheus.CounterVec
	ratelimited      *prometheus.CounterVec
	authDecisions    *prometheus.CounterVec
	upstreamUp       *prometheus.GaugeVec
	upstreamBackends *prometheus.GaugeVec
	discoveryErrors  *prometheus.CounterVec
	probes           *prometheus.CounterVec
	probeDuration    *prometheus.HistogramVec
	grpcTranscode    *prometheus.CounterVec
	grpcStreamMsgs   *prometheus.CounterVec
	grpcProxyCalls   prometheus.Counter
	pluginInvokes    *prometheus.CounterVec
	pluginDuration   *prometheus.HistogramVec
	pluginPanics     *prometheus.CounterVec
	listenerConns    prometheus.Gauge
	http3Conns       prometheus.Gauge
	streamConns      *prometheus.GaugeVec
	streamBytes      *prometheus.CounterVec
	certExpiry       *prometheus.GaugeVec
	certRenewals     prometheus.Counter
	mtlsHandshakes   *prometheus.CounterVec

	// certMu guards certSeen, the last observed NotAfter (unix seconds) per
	// domain. It lets ObserveCertExpiry distinguish a genuine renewal (the
	// expiry moved forward) from the steady stream of cache hits that autocert
	// produces on every TLS handshake.
	certMu   sync.Mutex
	certSeen map[string]int64

	// startTime is when this Metrics (and effectively the process) was created;
	// it backs the console uptime figure.
	startTime time.Time

	// statsMu guards the rolling state used by Snapshot to derive
	// rate-over-time figures (requests/sec and the windowed error rate) from
	// the monotonic counters between successive polls.
	statsMu          sync.Mutex
	statsLast        time.Time
	statsLastTotal   float64
	statsLastClasses map[string]float64
}

// NewMetrics creates and registers the collectors on a private registry.
func NewMetrics() *Metrics {
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
		compressed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_response_compressed_total",
			Help: "Responses compressed by the edge, labeled by content coding.",
		}, []string{"encoding"}),
		ratelimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_http_ratelimited_total",
			Help: "Requests rejected by rate limiting, labeled by key kind (ip/header/jwt).",
		}, []string{"key"}),
		authDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jul_auth_decisions_total",
			Help: "Access-control decisions, labeled by method (cidr/basic/jwt/forward) and result (allow/deny).",
		}, []string{"method", "result"}),
		upstreamUp: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "jul_upstream_healthy",
			Help: "Active health-check verdict per backend (1 healthy, 0 unhealthy), labeled by pool and backend.",
		}, []string{"pool", "backend"}),
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
			Help: "Active health-check probes, labeled by pool and result (success/failure).",
		}, []string{"pool", "result"}),
		probeDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "jul_upstream_probe_duration_seconds",
			Help:    "Active health-check probe latency in seconds, labeled by pool.",
			Buckets: prometheus.DefBuckets,
		}, []string{"pool"}),
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
	}
	m.startTime = time.Now()
	reg.MustRegister(
		m.requests,
		m.duration,
		m.inflight,
		m.cacheEvents,
		m.compressed,
		m.ratelimited,
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
		m.certExpiry,
		m.certRenewals,
		m.mtlsHandshakes,
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return m
}

// Handler returns the Prometheus exposition handler for this registry.
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
		rw := middleware.WrapResponseWriter(w)
		next.ServeHTTP(rw, r)

		host := hostLabel(r.Host)
		m.requests.WithLabelValues(r.Method, host, strconv.Itoa(rw.Status())).Inc()
		m.duration.WithLabelValues(r.Method, host).Observe(time.Since(start).Seconds())
		if state := rw.Header().Get("X-Cache"); state != "" {
			m.cacheEvents.WithLabelValues(state).Inc()
		}
	})
}

// ObserveCompression records that a response was compressed with the given
// content coding. It is wired into the Compression middleware as its OnCompress
// hook (the middleware package cannot import observability directly).
func (m *Metrics) ObserveCompression(encoding string) {
	m.compressed.WithLabelValues(encoding).Inc()
}

// ObserveRateLimited records that a request was rejected by rate limiting. kind
// is the key KIND (ip/header/jwt), not the raw client value, so metric
// cardinality stays bounded. It is wired into the RateLimit middleware as its
// onLimited hook (the middleware package cannot import observability directly).
func (m *Metrics) ObserveRateLimited(kind string) {
	m.ratelimited.WithLabelValues(kind).Inc()
}

// ObserveAuthDecision records an access-control decision. method is the gate
// that produced it (cidr/basic/jwt/forward) and result is allow or deny, both
// fixed-cardinality labels. It is wired into the auth middleware as its
// OnDecision hook (the auth package cannot import observability directly).
func (m *Metrics) ObserveAuthDecision(method, result string) {
	m.authDecisions.WithLabelValues(method, result).Inc()
}

// ObserveBackendHealth records an active health-check verdict for a backend as
// a per-backend gauge (1 healthy, 0 unhealthy). It is wired into the upstream
// pool registry as its OnHealth hook (the upstream package follows the
// codebase's callback convention rather than importing observability).
func (m *Metrics) ObserveBackendHealth(pool, backend string, healthy bool) {
	v := 0.0
	if healthy {
		v = 1.0
	}
	m.upstreamUp.WithLabelValues(pool, backend).Set(v)
}

// ObserveUpstreamBackends records the current backend count of a pool as a
// gauge. It is wired into the upstream pool registry as its OnBackends hook and
// tracks dynamic service discovery (the count changes as a discoverer resolves
// new endpoint sets).
func (m *Metrics) ObserveUpstreamBackends(pool string, n int) {
	m.upstreamBackends.WithLabelValues(pool).Set(float64(n))
}

// ObserveDiscoveryError records a failed or empty service-discovery resolve. It
// is wired into the upstream pool registry as its OnDiscoveryError hook; the
// pool keeps its last-good backends when this fires.
func (m *Metrics) ObserveDiscoveryError(pool string) {
	m.discoveryErrors.WithLabelValues(pool).Inc()
}

// ObserveProbe records the outcome and latency of a single active health-check
// probe. It is wired into the upstream pool registry as its OnProbe hook.
func (m *Metrics) ObserveProbe(pool string, success bool, latency time.Duration) {
	result := "failure"
	if success {
		result = "success"
	}
	m.probes.WithLabelValues(pool, result).Inc()
	m.probeDuration.WithLabelValues(pool).Observe(latency.Seconds())
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
	}
	m.certSeen[domain] = ts
	m.certMu.Unlock()
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

// hostLabel strips the port so metric cardinality stays bounded by hostname.
func hostLabel(host string) string {
	for i := 0; i < len(host); i++ {
		if host[i] == ':' {
			return host[:i]
		}
	}
	return host
}
