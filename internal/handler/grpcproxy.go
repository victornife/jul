// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"golang.org/x/net/http2"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/upstream"
)

// NewGRPCProxy builds a native gRPC / HTTP-2 passthrough handler for a location
// whose proxy_pass names a gRPC backend and which sets grpc = true. Unlike
// gRPC-JSON transcoding, this forwards the request end-to-end over HTTP/2
// without touching the payload: trailers (grpc-status / grpc-message) are
// preserved and response buffering is disabled so streaming frames flush as
// they arrive. A proxy_pass scheme of http:// dials the backend over cleartext
// HTTP/2 (h2c); https:// dials over HTTP/2 with TLS.
//
// onStream, when non-nil, is invoked once per forwarded gRPC call.
func NewGRPCProxy(ctx context.Context, _ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger, onStream func()) (http.Handler, error) {
	pool, basePath, scheme, err := resolvePool(ctx, loc, upstreams, reg)
	if err != nil {
		return nil, err
	}

	// scheme comes from proxy_pass, not a backend: a discovery-backed pool can
	// have zero backends at build time, and the per-backend scheme/host are set
	// on every request by the balancing transport anyway. https:// selects an
	// h2-over-TLS transport; http:// selects cleartext h2c.
	tlsBackend := scheme == "https"
	target := &url.URL{Scheme: scheme, Path: basePath}

	// Resolved while the handler generation is prepared, so unreadable or
	// malformed trust material aborts the reload rather than failing a call.
	policy, err := resolveBackendTLS(loc, upstreams)
	if err != nil {
		return nil, err
	}

	// max_connections_per_backend is deliberately not applied here. One HTTP/2
	// connection carries every stream to a backend, so a socket bound would not
	// bound concurrency; the request limit is what binds on this path, and
	// setting the field on a gRPC route is a lint warning.
	transport := newGRPCTransport(loc, tlsBackend, policy)

	rp := &httputil.ReverseProxy{
		// FlushInterval -1 flushes every write immediately: gRPC streaming
		// frames (and unary trailers) must not be buffered, or a server-stream
		// would stall until the whole call completed.
		FlushInterval: -1,
		Transport: &grpcBalancingTransport{
			pool:       pool,
			base:       transport,
			log:        log,
			onStream:   onStream,
			tlsBackend: tlsBackend,
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			setCanonicalXForwarded(pr)
			applyProxyHeaders(pr, loc)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			code := proxyErrorStatus(err)
			if log != nil {
				log.Error("grpc proxy upstream error",
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
	// A gRPC call holds its slot for the whole call, which for a server, client
	// or bidirectional stream is the stream's real lifetime. Closing the handler
	// also drops the transport's idle HTTP/2 connections, so a retired
	// generation stops holding sockets open.
	return newAdmittedHandler(rp, pool.Admission(), func() error {
		transport.CloseIdleConnections()
		return nil
	}), nil
}

// grpcBalancingTransport selects a backend per call and forwards it over an
// HTTP/2 transport. gRPC streams are not replayable, so unlike the HTTP proxy
// it does not retry against other backends mid-call; a dial failure surfaces as
// a gateway error.
type grpcBalancingTransport struct {
	pool     *upstream.Pool
	base     *http2.Transport
	log      *slog.Logger
	onStream func()
	// tlsBackend records that the route is configured for TLS gRPC. A backend
	// whose scheme is not https is refused rather than dialled: nothing may
	// move a call from TLS gRPC to cleartext h2c.
	tlsBackend bool
}

func (t *grpcBalancingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := t.pool.PickCtx(req.Context())
	if err != nil {
		return nil, err
	}
	if t.tlsBackend && b.URL.Scheme != "https" {
		t.pool.Release(b)
		return nil, fmt.Errorf("grpc backend %s is not https but the route is: refusing to downgrade to h2c", b.URL.Host)
	}
	req.URL.Scheme = b.URL.Scheme
	req.URL.Host = b.URL.Host

	resp, err := t.base.RoundTrip(req)
	if err != nil {
		t.pool.MarkFailure(b)
		t.pool.Release(b)
		return nil, err
	}
	t.pool.MarkSuccess(b)
	if t.onStream != nil {
		t.onStream()
	}
	// Hold the in-flight slot until the response body is closed so least-conn
	// balancing reflects the full call (including a long-lived stream).
	resp.Body = &releaseBody{ReadCloser: resp.Body, release: func() { t.pool.Release(b) }}
	return resp, nil
}

// newGRPCTransport builds an HTTP/2 transport for the backend. For a cleartext
// (h2c) backend it dials plain TCP and serves prior-knowledge HTTP/2; for a TLS
// backend it dials TLS negotiating the h2 ALPN protocol. The connect timeout
// comes from the location's proxy_connect_timeout.
func newGRPCTransport(loc config.LocationConfig, tlsBackend bool, policy *backendtls.Policy) *http2.Transport {
	connectTimeout := loc.ProxyConnectTimeout.Std()
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}

	if tlsBackend {
		// One config per transport, built here rather than per dial. ALPN must
		// advertise h2 explicitly: this transport speaks HTTP/2 only, and a
		// backend that negotiated http/1.1 would break gRPC framing.
		var policyCfg *tls.Config
		if policy != nil {
			policyCfg = policy.ClientConfig()
			policyCfg.NextProtos = []string{"h2"}
		}
		return &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
				if policyCfg != nil {
					// The policy decides the verified identity; the address is
					// only where to dial.
					cfg = policyCfg
				}
				td := &tls.Dialer{NetDialer: dialer, Config: cfg}
				return td.DialContext(ctx, network, addr)
			},
		}
	}
	// Cleartext HTTP/2: AllowHTTP lets the transport accept "http" URLs and use
	// DialTLSContext for a plain (non-TLS) connection \u2014 the standard h2c client.
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			return dialer.DialContext(ctx, network, addr)
		},
	}
}
