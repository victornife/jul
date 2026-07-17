// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

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
// srv is part of the router.Builder signature but is not needed here.
func NewProxy(_ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger) (http.Handler, error) {
	pool, basePath, scheme, err := resolvePool(loc, upstreams, reg)
	if err != nil {
		return nil, err
	}

	// The target supplies the scheme and base path for path joining; the
	// balancing transport overrides the scheme and host per selected backend on
	// every request. The scheme comes from proxy_pass (not a backend) because a
	// discovery-backed pool can legitimately have zero backends at build time,
	// so indexing a backend here would panic.
	target := &url.URL{Scheme: scheme, Path: basePath}

	rp := &httputil.ReverseProxy{
		Transport: &balancingTransport{pool: pool, base: newProxyTransport(loc), log: log, maxRetries: loc.ProxyRetries},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			// Secure defaults: clears client-supplied X-Forwarded-* and sets
			// them from the real connection.
			pr.SetXForwarded()
			applyProxyHeaders(pr, loc)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			code := proxyErrorStatus(err)
			if log != nil {
				log.Error("proxy upstream error",
					"upstream", pool.Name(),
					"path", r.URL.Path,
					"status", code,
					"error", err,
					"request_id", middleware.RequestIDFrom(r.Context()),
				)
			}
			http.Error(w, fmt.Sprintf("%d %s", code, http.StatusText(code)), code)
		},
	}
	return rp, nil
}

// resolvePool turns proxy_pass into a backend pool plus the base path to join.
// A reference to a named upstream is resolved through the registry (reg), which
// owns the pool's lifecycle across reloads and runs its active health checker; a
// concrete URL builds an anonymous pool-of-one outside the registry. When reg is
// nil (for example in unit tests) named upstreams are built directly without
// lifecycle management.
func resolvePool(loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, string, string, error) {
	u, err := url.Parse(loc.ProxyPass)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, "", "", fmt.Errorf("invalid proxy_pass %q (want http(s)://host:port or http://upstream-name)", loc.ProxyPass)
	}
	if up, ok := upstreams[u.Host]; ok {
		if reg != nil {
			pool, err := reg.For(up, u.Scheme)
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
type balancingTransport struct {
	pool       *upstream.Pool
	base       *http.Transport
	log        *slog.Logger
	maxRetries int // 0 means try every distinct backend once
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

	canRetry := isIdempotent(req.Method) && (req.Body == nil || req.GetBody != nil)

	var lastErr error
	tried := make(map[upstream.BackendIdentity]struct{})
	retried := false
	attempts := 0
	for {
		attempts++
		b, err := t.pool.PickExcluding(req.Context(), tried)
		if err != nil {
			if lastErr != nil {
				err = lastErr
			}
			span.RecordError(err)
			return nil, err
		}
		tried[b.Identity()] = struct{}{}

		out := req
		if retried && req.GetBody != nil {
			body, berr := req.GetBody()
			if berr != nil {
				t.pool.Release(b)
				// The body could not be rewound for this retry. The meaningful
				// error is the upstream failure that triggered the retry, not the
				// rewind error, so surface lastErr when present (it always is on a
				// retry, since i > 0 means a prior attempt failed).
				err := berr
				if lastErr != nil {
					err = lastErr
				}
				span.RecordError(err)
				return nil, err
			}
			out = req.Clone(req.Context())
			out.Body = body
		}
		out.URL.Scheme = b.URL.Scheme
		out.URL.Host = b.URL.Host

		// Child span per backend attempt. Inject W3C tracecontext from the
		// attempt's context so the upstream continues this trace under it.
		actx, aspan := tr.Start(ctx, "upstream.request")
		aspan.SetString("upstream.backend", b.URL.Host)
		tr.Inject(actx, out.Header)

		resp, err := t.base.RoundTrip(out)
		if err == nil {
			t.pool.MarkSuccess(b)
			aspan.SetStatus(resp.StatusCode)
			aspan.End()
			span.SetStatus(resp.StatusCode)
			// Hold the in-flight slot until the response body is closed so
			// least-conn balancing reflects the full request lifetime. For a
			// protocol upgrade (101) the body is also writable and ReverseProxy
			// splices it bidirectionally, so the wrapper preserves
			// io.ReadWriteCloser (WebSocket / raw stream passthrough).
			resp.Body = wrapReleaseBody(resp.Body, func() { t.pool.Release(b) })
			return resp, nil
		}
		aspan.RecordError(err)
		aspan.End()

		t.pool.MarkFailure(b)
		t.pool.Release(b)
		lastErr = err
		retried = true
		if !canRetry {
			break
		}
		// Bound retries: 0 means try every distinct backend once; a positive
		// value caps attempts to the configured count.
		if t.maxRetries > 0 && attempts >= t.maxRetries {
			break
		}
	}
	span.RecordError(lastErr)
	return nil, lastErr
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

func isIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace, http.MethodPut, http.MethodDelete:
		return true
	default:
		return false
	}
}

// newProxyTransport returns a connection-reusing transport tuned by the
// location's proxy timeouts.
func newProxyTransport(loc config.LocationConfig) *http.Transport {
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
func expandProxyVar(tmpl string, in *http.Request) string {
	if !strings.Contains(tmpl, "$") {
		return tmpl
	}
	remote := clientIP(in)
	scheme := "http"
	if in.TLS != nil {
		scheme = "https"
	}
	xff := remote
	if prior := in.Header.Get("X-Forwarded-For"); prior != "" {
		xff = prior + ", " + remote
	}
	pairs := []string{
		"$proxy_add_x_forwarded_for", xff,
		"$remote_addr", remote,
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
	id := middleware.ClientIdentityFrom(in.Context())
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
func proxyErrorStatus(err error) int {
	if errors.Is(err, upstream.ErrNoAvailableBackend) {
		return http.StatusServiceUnavailable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return http.StatusGatewayTimeout
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return http.StatusGatewayTimeout
	}
	return http.StatusBadGateway
}

// clientIP extracts the remote IP (without port) from a request.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
