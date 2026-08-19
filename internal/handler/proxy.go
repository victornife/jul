// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"jul/internal/backendtls"
	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/tracing"
	"jul/internal/upstream"
)

// NewProxy builds a reverse-proxy handler for a location. The target is a pool
// (a single concrete proxy_pass URL becomes a pool of one; an upstream
// reference becomes its configured pool). Requests are balanced across healthy
// backends with passive health checking and idempotent-method failover.
//
// The extra upstreams/log parameters are bound via a closure in main so the
// resulting function still satisfies router.Builder.
//
// srv is part of the router.Builder signature but is not needed here. ctx
// bounds the upstream registry lookup, including initial discovery resolution.
// dialFailure, when non-nil, is called once per backend dial/connect failure
// with a bounded reason from upstream.ClassifyDialError (excluding client
// cancellation and backend-TLS-identity failures, which are not dial failures).
func NewProxy(ctx context.Context, _ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger, dialFailure func(reason string)) (http.Handler, error) {
	pool, basePath, scheme, err := resolvePool(ctx, loc, upstreams, reg)
	if err != nil {
		return nil, err
	}

	// The backend trust policy is resolved here, while the handler generation
	// is being prepared, so unreadable or malformed material aborts the reload
	// instead of failing the first request.
	policy, err := resolveBackendTLS(loc, upstreams)
	if err != nil {
		return nil, err
	}
	transport := newProxyTransport(loc, policy, maxConnsPerBackend(loc, pool), pool)

	// The target supplies the scheme and base path for path joining; the
	// balancing transport overrides the scheme and host per selected backend on
	// every request. The scheme comes from proxy_pass (not a backend) because a
	// discovery-backed pool can legitimately have zero backends at build time,
	// so indexing a backend here would panic.
	target := &url.URL{Scheme: scheme, Path: basePath}

	rp := &httputil.ReverseProxy{
		Transport: &balancingTransport{
			pool:          pool,
			base:          transport,
			log:           log,
			retryOverride: newLocationRetry(loc),
			tlsBackend:    scheme == "https",
			dialFailure:   dialFailure,
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Secure defaults: clears client-supplied X-Forwarded-* and sets
			// them from Jul's own trusted view of the request.
			setCanonicalXForwarded(pr)
			applyProxyHeaders(pr, loc)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			code := proxyErrorStatus(err, r.Context())
			if log != nil {
				// A dial/connect-shaped failure was already counted (and, if new,
				// logged once) per attempt inside RoundTrip. This request-level line
				// adds request context (path, status, request_id) an operator greps
				// for, but must not re-flood the log on its own, so it shares the
				// same pool heartbeat rather than a second counter. Client
				// cancellation and backend-TLS-identity failures are unrelated to
				// backend health and keep today's unconditional line.
				logLine := true
				if !errors.Is(err, context.Canceled) && tlsFailureCategory(err) == "" {
					logLine = pool.AllowDialFailureLog()
				}
				if logLine {
					attrs := []any{
						"upstream", pool.Name(),
						"path", r.URL.Path,
						"status", code,
						"error", err,
						"request_id", middleware.RequestIDFrom(r.Context()),
					}
					// A bounded category, so a backend-trust failure is greppable
					// and countable without the raw error becoming a label.
					if category := tlsFailureCategory(err); category != "" {
						attrs = append(attrs, "tls_failure", category)
					}
					log.Error("proxy upstream error", attrs...)
				}
			}
			http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
		},
	}
	return &proxyHandler{
		ReverseProxy: rp,
		transport:    transport,
		admission:    pool.Admission(),
		retire:       make(chan struct{}),
	}, nil
}

// proxyHandler is the reverse proxy plus ownership of its transport. The
// handler generation stages it, so the transport's idle connections — and with
// them any connection established under a superseded trust policy — are closed
// when that generation retires.
type proxyHandler struct {
	*httputil.ReverseProxy
	transport *http.Transport

	// admission bounds the pool's in-flight work. It is the pool's own admission
	// owner, so it is shared with every other route pointing at the same upstream
	// and survives reload; only the retire channel below is generation-scoped.
	admission *upstream.Admission

	// retire is closed when this generation is torn down. A request parked in the
	// pending queue holds a generation reference, but retirement is bounded by a
	// forced grace after which the transport is closed anyway — so without an
	// explicit wakeup a parked request could be admitted onto a closed transport.
	// The channel is per generation rather than per pool: a reused pool's waiters
	// belong to whichever generation parked them.
	retire     chan struct{}
	retireOnce sync.Once
}

// ServeHTTP admits the request before any upstream work happens, then delegates
// to the reverse proxy.
//
// This method exists because the type otherwise promotes ServeHTTP from the
// embedded *httputil.ReverseProxy. Admission belongs here rather than in
// RoundTrip because RoundTrip contains the retry loop, and admitting there would
// count one admission per attempt instead of per request.
//
// Being innermost has consequences, all of them intended: a cache hit never
// consumes a slot, a WAF-blocked or unauthenticated request never consumes one,
// background cache revalidation does, and under sustained overload Jul pays full
// WAF and authentication cost for requests it then rejects. Accounting
// correctness outranks CPU savings on the rejection path.
func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	release, err := h.admission.Admit(r.Context(), h.retire)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	// ReverseProxy.ServeHTTP returns only once the response body has been copied,
	// and for a 101 upgrade only once the spliced connection closes, so the slot
	// spans the request's real lifetime including WebSocket and SSE streams.
	defer release()
	h.ReverseProxy.ServeHTTP(w, r)
}

// writeAdmissionError maps an admission rejection to a client-facing status.
// Overload is 503 with Retry-After, never 429: 429 says the client sent too many
// requests, but overload is not the client's fault.
func writeAdmissionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		// The client went away while queued. Nothing can be delivered; the status
		// is recorded for the access log rather than written to a live peer.
		w.WriteHeader(statusClientClosedRequest)
	case errors.Is(err, context.DeadlineExceeded):
		http.Error(w, "504 Gateway Timeout", http.StatusGatewayTimeout)
	case errors.Is(err, upstream.ErrOverloaded):
		w.Header().Set("Retry-After", "1")
		http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
	default: // upstream.ErrRetired
		http.Error(w, "503 Service Unavailable", http.StatusServiceUnavailable)
	}
}

// statusClientClosedRequest is nginx's 499: the client closed the connection
// before a response could be produced. It is not an IANA status and is only ever
// recorded, never negotiated.
const statusClientClosedRequest = 499

// Close retires this generation's proxy handler: it wakes any request parked in
// the pending queue on behalf of this generation, then closes the transport's
// idle connections. The order matters — a waiter granted a slot after the
// transport closed would dial nothing.
func (h *proxyHandler) Close() error {
	h.retireOnce.Do(func() { close(h.retire) })
	return transportCloser{t: h.transport}.Close()
}

// resolvePool turns proxy_pass into a backend pool plus the base path to join.
// A reference to a named upstream is resolved through the registry (reg), which
// owns the pool's lifecycle across reloads and runs its active health checker; a
// concrete URL builds an anonymous pool-of-one outside the registry. When reg is
// nil (for example in unit tests) named upstreams are built directly without
// lifecycle management.
func resolvePool(ctx context.Context, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, string, string, error) {
	u, err := url.Parse(loc.ProxyPass)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, "", "", fmt.Errorf("invalid proxy_pass %q (want http(s)://host:port or http://upstream-name)", loc.ProxyPass)
	}
	if up, ok := upstreams[u.Host]; ok {
		if reg != nil {
			pool, err := reg.For(ctx, up, u.Scheme)
			return pool, u.Path, u.Scheme, err
		}
		pool, err := upstream.NewPool(up, u.Scheme)
		return pool, u.Path, u.Scheme, err
	}
	// Concrete URL: a pool of one backend.
	single := config.UpstreamConfig{
		Name:     u.Host,
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: u.Host, Weight: 1}},
		MaxFails: 3,
	}
	pool, err := upstream.NewPool(single, u.Scheme)
	return pool, u.Path, u.Scheme, err
}

// balancingTransport selects a backend per request, marks passive health, and
// retries idempotent requests against other backends on connection failures.
//
// It is an adapter, not a retry engine: selection, the overall deadline,
// backoff and the retry budget live in upstream.Pool.Do so that the CGI
// adapters and the transcoder answer to the same rule. What stays here is
// everything that is genuinely HTTP — body rewind, the scheme guard, spans and
// passive-health logging.
type balancingTransport struct {
	pool *upstream.Pool
	base *http.Transport
	log  *slog.Logger
	// retryOverride holds the location's retry settings. A zero field inherits
	// the pool policy, which is read per request so a reload takes effect
	// without rebuilding the transport.
	retryOverride upstream.RetryOverride
	// tlsBackend records that the configured target is https. A backend whose
	// scheme is not https is then refused rather than dialled: no retry,
	// failover or discovery result may move a request from TLS to plaintext.
	tlsBackend bool
	// dialFailure, when non-nil, counts a backend dial/connect failure by a
	// bounded reason. It may be nil (for example in tests).
	dialFailure func(reason string)
}

// newLocationRetry reads the location's retry overrides, accepting the
// deprecated proxy_retries spelling. Validation has already rejected setting
// both, so preferring either here cannot mask a conflict.
func newLocationRetry(loc config.LocationConfig) upstream.RetryOverride {
	lr := upstream.RetryOverride{Attempts: loc.ProxyRetries}
	if loc.Resilience == nil {
		return lr
	}
	if loc.Resilience.RetryAttempts > 0 {
		lr.Attempts = loc.Resilience.RetryAttempts
	}
	lr.Deadline = loc.Resilience.RetryDeadline.Std()
	lr.BackoffInitial = loc.Resilience.RetryBackoffInitial.Std()
	lr.BackoffMax = loc.Resilience.RetryBackoffMax.Std()
	return lr
}

// retryRequest resolves the effective retry settings for one request: the
// location wins where it set a value, the pool policy supplies the rest.
func (t *balancingTransport) retryRequest(replayable bool) upstream.RetryRequest {
	return t.pool.RetryRequestFor(t.retryOverride, replayable)
}

func (t *balancingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// proxy.roundtrip spans the whole balanced call (including failover); each
	// backend attempt is an upstream.request child span so failover is visible.
	// The seam is a no-op unless the otel build wired a real tracer, so this is
	// free when tracing is off.
	tr := tracing.Active()
	ctx, span := tr.Start(req.Context(), "proxy.roundtrip")
	defer span.End()
	span.SetString("upstream.name", t.pool.Name())

	replayable := isIdempotent(req.Method) && (req.Body == nil || req.GetBody != nil)

	var resp *http.Response
	attempts := 0
	_, err := t.pool.Do(req.Context(), t.retryRequest(replayable), func(actx context.Context, b upstream.Attempt, n int) upstream.AttemptResult {
		attempts++
		out := req
		if n > 1 && req.GetBody != nil {
			body, berr := req.GetBody()
			if berr != nil {
				// A body that cannot be rewound is not a backend failure and
				// retrying cannot fix it, so the sequence ends here — but the
				// error the client deserves is the upstream failure that
				// triggered the retry, which Do already carries as lastErr.
				return upstream.AttemptResult{Err: berr, Terminal: true}
			}
			out = req.Clone(actx)
			out.Body = body
		} else if actx != req.Context() {
			out = req.Clone(actx)
		}
		if t.tlsBackend && (b.URL == nil || b.URL.Scheme != "https") {
			// Fail closed rather than downgrade. Reaching here would mean a
			// backend entered the pool with a different scheme than the route
			// was configured with. It is terminal: every retry would face the
			// same misconfiguration, and none of them may downgrade either.
			return upstream.AttemptResult{
				Err:      fmt.Errorf("backend %s is not https but the route is: refusing to downgrade", b.Address),
				Terminal: true,
			}
		}
		out.URL.Scheme = b.URL.Scheme
		out.URL.Host = b.URL.Host

		// Child span per backend attempt. Inject W3C tracecontext from the
		// attempt's context so the upstream continues this trace under it.
		sctx, aspan := tr.Start(ctx, "upstream.request")
		aspan.SetString("upstream.backend", b.URL.Host)
		tr.Inject(sctx, out.Header)

		r, err := t.base.RoundTrip(out)
		if err == nil {
			if t.pool.MarkSuccess(b) && t.log != nil {
				t.log.Info("proxy backend recovered", "upstream", t.pool.Name(), "backend", b.URL.Host)
			}
			aspan.SetStatus(r.StatusCode)
			aspan.End()
			span.SetStatus(r.StatusCode)
			// Hold the in-flight slot until the response body is closed so
			// least-conn balancing reflects the full request lifetime. For a
			// protocol upgrade (101) the body is also writable and ReverseProxy
			// splices it bidirectionally, so the wrapper preserves
			// io.ReadWriteCloser (WebSocket / raw stream passthrough).
			r.Body = wrapReleaseBody(r.Body, func() { t.pool.Release(b.Backend) })
			resp = r
			return upstream.AttemptResult{Retain: true}
		}
		aspan.RecordError(err)
		aspan.End()
		t.noteFailure(b, err)
		// A deterministic backend-identity failure is the same failure against
		// every backend, so retrying it is amplification with no chance of a
		// different answer.
		return upstream.AttemptResult{Err: err, Terminal: tlsFailureCategory(err) != ""}
	})
	if err != nil {
		if attempts == 0 && t.dialFailure != nil {
			// Every backend was already in cooldown before this call made any
			// attempt of its own: count it (bounded reason "no_backend"), same as
			// a real dial failure, so the counter is not undercounted relative to
			// what an operator would see logged.
			t.dialFailure(upstream.ClassifyDialError(err))
		}
		span.RecordError(err)
		return nil, err
	}
	return resp, nil
}

// noteFailure records a failed attempt against passive health and the bounded
// dial-failure counter, logging a transition unconditionally and an ordinary
// failure only on the pool's throttle.
func (t *balancingTransport) noteFailure(b upstream.Attempt, err error) {
	tripped := t.pool.MarkFailure(b)
	// Client cancellation and backend-TLS-identity failures are not backend
	// dial failures: the former is client behavior, and the latter already has
	// its own unthrottled, categorized line via tlsFailureCategory (ADR 0016
	// territory). MarkFailure still runs for both, unchanged from before this
	// counter existed: this is observability only, not a circuit-breaker change.
	if errors.Is(err, context.Canceled) || tlsFailureCategory(err) != "" {
		return
	}
	reason := upstream.ClassifyDialError(err)
	if t.dialFailure != nil {
		t.dialFailure(reason)
	}
	if t.log == nil {
		return
	}
	switch {
	case tripped:
		t.log.Warn("proxy backend marked down", "upstream", t.pool.Name(), "backend", b.Address, "reason", reason, "error", err)
	case t.pool.AllowDialFailureLog():
		t.log.Warn("proxy dial failed", "upstream", t.pool.Name(), "backend", b.Address, "reason", reason, "error", err)
	}
}

// releaseBody releases a backend's in-flight slot exactly once, when the
// response body is closed.
type releaseBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (r *releaseBody) Close() error {
	r.once.Do(r.release)
	return r.ReadCloser.Close()
}

// wrapReleaseBody attaches the in-flight slot release to a response body. A
// protocol-upgrade (HTTP 101) response carries a writable body that
// httputil.ReverseProxy type-asserts to io.ReadWriteCloser to splice the
// upgraded connection (WebSocket, raw TCP-over-HTTP). The plain releaseBody is
// read-only, which would break that assertion, so upgrade bodies are wrapped in
// releaseRWBody to keep the Write method exposed.
func wrapReleaseBody(body io.ReadCloser, release func()) io.ReadCloser {
	rb := &releaseBody{ReadCloser: body, release: release}
	if rw, ok := body.(io.ReadWriteCloser); ok {
		return &releaseRWBody{releaseBody: rb, w: rw}
	}
	return rb
}

// releaseRWBody is wrapReleaseBody's variant for upgrade responses whose body is
// also writable. Read and Close are promoted from the embedded releaseBody (so
// the slot is still released exactly once); Write is forwarded to the upgraded
// connection.
type releaseRWBody struct {
	*releaseBody
	w io.Writer
}

func (r *releaseRWBody) Write(p []byte) (int, error) { return r.w.Write(p) }

func isIdempotent(method string) bool { return upstream.RetrySafeMethod(method) }

// replayableBody reports whether a request's body can be sent a second time.
//
// The obvious test, `Body == nil || GetBody != nil`, is right for the outbound
// request `httputil.ReverseProxy` builds — it nils the body when there is none
// — and silently wrong for a *server* request, where `Body` is always non-nil
// and merely returns EOF immediately. Applied there it reports "not replayable"
// for every request, which would disable retry entirely for the CGI adapters
// while looking correct. A body-less request is trivially replayable, so that
// case is recognised explicitly.
//
// A server request never carries GetBody, because net/http does not set one, so
// a CGI request that really has a body is not retried. Buffering it to make it
// replayable is exactly the unbounded-memory failure this programme exists to
// prevent, so the limit is deliberate rather than pending.
func replayableBody(r *http.Request) bool {
	if r.GetBody != nil {
		return true
	}
	return r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0
}

// newProxyTransport returns a connection-reusing transport tuned by the
// location's proxy timeouts.
//
// maxConns is max_connections_per_backend. It maps to MaxConnsPerHost, which is
// the only lever Go offers that bounds sockets without defeating connection
// pooling and that honours the request context while a request queues for a
// dial. Idle connections count toward it until IdleConnTimeout, so under
// HTTP/1.1 keep-alive it can bind while the pool's active count is low.
func newProxyTransport(loc config.LocationConfig, policy *backendtls.Policy, maxConns int, pool *upstream.Pool) *http.Transport {
	connectTimeout := loc.ProxyConnectTimeout.Std()
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}

	readTimeout := loc.ProxyReadTimeout.Std()
	sendTimeout := loc.ProxySendTimeout.Std()

	// When either inactivity bound is configured, wrap each upstream connection
	// so a slow-trickle peer cannot stall the exchange indefinitely. The
	// deadlines are re-armed on every Read/Write, so they cap the gap *between*
	// successive I/O operations (NGINX proxy_read_timeout / proxy_send_timeout
	// semantics) rather than the total transfer — a steadily streaming response
	// (SSE, chunked downloads) is never interrupted while data keeps flowing.
	dial := dialer.DialContext
	if pool != nil {
		// net/http enforces MaxConnsPerHost internally and exposes no live count,
		// so the only honest source for jul_upstream_connections is Jul's own
		// dialer.
		base := dial
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := base(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &countedConn{Conn: c, release: pool.TrackConn()}, nil
		}
	}
	if readTimeout > 0 || sendTimeout > 0 {
		base := dial
		dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := base(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &timeoutConn{Conn: c, readTimeout: readTimeout, writeTimeout: sendTimeout}, nil
		}
	}

	t := &http.Transport{
		DialContext:           dial,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       maxConns,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	if readTimeout > 0 {
		// Time allowed to receive the response headers (time-to-first-byte).
		// The per-read deadline above additionally bounds the body.
		t.ResponseHeaderTimeout = readTimeout
	}
	if policy != nil {
		// A fresh config per transport: the policy is shared, the tls.Config is
		// not, so nothing here can affect another consumer. ForceAttemptHTTP2
		// stays set, so HTTP/2 is still negotiated with a custom TLS config.
		t.TLSClientConfig = policy.ClientConfig()
	}
	return t
}

// timeoutConn enforces NGINX-style inactivity deadlines on a proxied upstream
// connection. Before each Read it (re)arms a read deadline of readTimeout
// (proxy_read_timeout: the maximum gap between successive reads of the response,
// covering both the headers and a slow-trickle body). Before each Write it arms
// a write deadline of writeTimeout (proxy_send_timeout: the maximum gap between
// successive writes of the request to the upstream). A non-positive timeout
// leaves that direction unbounded, so the wrapper is only installed when at
// least one bound is configured.
//
// Because the deadlines are refreshed per call, an actively streaming exchange
// (chunked download, SSE, a long-lived upgrade) is never torn down while data
// keeps moving inside the window; only true inactivity beyond the bound fails.
type timeoutConn struct {
	net.Conn
	readTimeout  time.Duration
	writeTimeout time.Duration
}

func (c *timeoutConn) Read(b []byte) (int, error) {
	if c.readTimeout > 0 {
		if err := c.SetReadDeadline(time.Now().Add(c.readTimeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Read(b)
}

func (c *timeoutConn) Write(b []byte) (int, error) {
	if c.writeTimeout > 0 {
		if err := c.SetWriteDeadline(time.Now().Add(c.writeTimeout)); err != nil {
			return 0, err
		}
	}
	return c.Conn.Write(b)
}

// applyProxyHeaders applies the Host override and custom headers with variable
// expansion. It runs after SetXForwarded so explicit config wins.
func applyProxyHeaders(pr *httputil.ProxyRequest, loc config.LocationConfig) {
	for name, tmpl := range loc.Headers {
		val := expandProxyVar(tmpl, pr.In)
		if strings.EqualFold(name, "Host") {
			pr.Out.Host = val
			continue
		}
		pr.Out.Header.Set(name, val)
	}
}

// expandProxyVar substitutes NGINX-style variables used in proxy headers.
//
// $remote_addr is the canonical client address, matching NGINX with its realip
// module configured — which is exactly what [servers.client_address] expresses.
// With no trusted proxy it is the transport peer, so a direct deployment is
// unchanged. $realip_remote_addr is NGINX's name for the address the connection
// actually came from, and carries the direct peer here too.
func expandProxyVar(tmpl string, in *http.Request) string {
	if !strings.Contains(tmpl, "$") {
		return tmpl
	}
	client, peer := forwardedAddrs(in)
	scheme := "http"
	if in.TLS != nil {
		scheme = "https"
	}
	pairs := []string{
		"$proxy_add_x_forwarded_for", forwardedChain(client, peer),
		"$realip_remote_addr", peer,
		"$remote_addr", client,
		"$host", in.Host,
		"$scheme", scheme,
	}
	if strings.Contains(tmpl, "$ssl_client") {
		pairs = append(pairs, sslClientPairs(in)...)
	}
	return strings.NewReplacer(pairs...).Replace(tmpl)
}

// sslClientPairs returns the old/new replacement pairs for the $ssl_client_*
// variables, carrying the verified mutual-TLS client identity from the request
// context onto upstream headers. When no client certificate was presented every
// value is empty except $ssl_client_verify, which is "NONE" (and "SUCCESS" when
// an identity is present), mirroring NGINX semantics.
func sslClientPairs(in *http.Request) []string {
	id := middleware.PeerCertIdentityFrom(in.Context())
	if id == nil {
		return []string{
			"$ssl_client_s_dn", "",
			"$ssl_client_i_dn", "",
			"$ssl_client_cn", "",
			"$ssl_client_serial", "",
			"$ssl_client_fingerprint", "",
			"$ssl_client_san", "",
			"$ssl_client_verify", "NONE",
		}
	}
	return []string{
		"$ssl_client_s_dn", id.SubjectDN,
		"$ssl_client_i_dn", id.IssuerDN,
		"$ssl_client_cn", id.CN,
		"$ssl_client_serial", id.Serial,
		"$ssl_client_fingerprint", id.Fingerprint,
		"$ssl_client_san", id.SANs,
		"$ssl_client_verify", "SUCCESS",
	}
}

// proxyErrorStatus maps a transport error to a gateway status code.
// proxyErrorStatus maps a transport error to a gateway status, using the
// inbound request context to tell a client that disconnected from Jul's own
// machinery giving up.
//
// Both arrive as context.Canceled — the retry driver derives its context from
// the inbound one — so without the inbound context the two are indistinguishable
// and every deadline Jul enforces would be recorded as a client going away.
func proxyErrorStatus(err error, inbound context.Context) int {
	if status := upstream.ReasonFor(err, inbound).HTTPStatus(); status != upstream.StatusFromLastAttempt {
		return status
	}
	return http.StatusBadGateway
}

// setCanonicalXForwarded replaces every client-supplied X-Forwarded-* header
// with Jul's own view of the request, then overwrites X-Forwarded-For with the
// canonical chain.
//
// SetXForwarded already clears the inbound values and sets X-Forwarded-Host and
// X-Forwarded-Proto from the real connection; only the address chain needs the
// canonical identity, because the standard library derives it from RemoteAddr
// and therefore cannot know about trusted proxies.
func setCanonicalXForwarded(pr *httputil.ProxyRequest) {
	pr.SetXForwarded()
	client, peer := forwardedAddrs(pr.In)
	if chain := forwardedChain(client, peer); chain != "" {
		pr.Out.Header.Set("X-Forwarded-For", chain)
	}
	setClientCertHeaders(pr)
}

// certAssertionHeaders are the channels that convey a client certificate to a
// backend. RFC 9440 §2.4 requires a terminating proxy to remove or overwrite
// them on every request it forwards, *including* requests where no client
// certificate was negotiated: a backend that trusts the header cannot tell an
// assertion Jul made from one the client made. X-Forwarded-Client-Cert is the
// widely deployed pre-standard spelling and is stripped on the same terms.
var certAssertionHeaders = []string{"Client-Cert", "Client-Cert-Chain", "X-Forwarded-Client-Cert"}

// setClientCertHeaders sanitizes the certificate-assertion channel and, when the
// listener asks for it, emits Jul's own assertion.
//
// Sanitizing is unconditional, mirroring how X-Forwarded-* is cleared and
// rebuilt on every request: the guarantee must be a property of the code, not of
// the operator having remembered to list a header.
func setClientCertHeaders(pr *httputil.ProxyRequest) {
	for _, h := range certAssertionHeaders {
		pr.Out.Header.Del(h)
	}
	id := middleware.PeerCertIdentityFrom(pr.In.Context())
	if id == nil || len(id.Raw) == 0 {
		return
	}
	pr.Out.Header.Set("Client-Cert", certItem(id.Raw))
	if len(id.Chain) == 0 {
		return
	}
	items := make([]string, 0, len(id.Chain))
	for _, der := range id.Chain {
		items = append(items, certItem(der))
	}
	pr.Out.Header.Set("Client-Cert-Chain", strings.Join(items, ", "))
}

// certItem renders DER as an RFC 8941 Byte Sequence: base64 delimited by colons
// (RFC 9440 §2.1).
func certItem(der []byte) string {
	return ":" + base64.StdEncoding.EncodeToString(der) + ":"
}

// forwardedAddrs returns the canonical client and direct peer as text.
func forwardedAddrs(in *http.Request) (client, peer string) {
	return addrText(clientaddr.Client(in)), addrText(clientaddr.Peer(in))
}

// forwardedChain builds the outbound X-Forwarded-For value.
//
// It is deliberately lossy: Jul emits its own trusted knowledge — the canonical
// client and the peer it was received from — and never preserves an inbound
// chain. An inbound "client, P1" received from peer P2 is emitted as
// "client, P2", dropping intermediate trusted proxies. Jul is the last hop
// before the backend, and reconstructing the full chain would re-inject
// third-party data into a channel the backend authenticates.
func forwardedChain(client, peer string) string {
	switch {
	case client == "":
		return peer
	case peer == "" || peer == client:
		return client
	default:
		return client + ", " + peer
	}
}

// addrText renders an address, returning "" when it could not be identified.
func addrText(addr netip.Addr) string {
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}

// countedConn decrements a pool's live connection count when the transport
// closes it.
type countedConn struct {
	net.Conn
	release func()
}

func (c *countedConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
