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
func NewGRPCProxy(_ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger, onStream func()) (http.Handler, error) {
	pool, basePath, scheme, err := resolvePool(loc, upstreams, reg)
	if err != nil {
		return nil, err
	}

	// scheme comes from proxy_pass, not a backend: a discovery-backed pool can
	// have zero backends at build time, and the per-backend scheme/host are set
	// on every request by the balancing transport anyway. https:// selects an
	// h2-over-TLS transport; http:// selects cleartext h2c.
	tlsBackend := scheme == "https"
	target := &url.URL{Scheme: scheme, Path: basePath}

	rp := &httputil.ReverseProxy{
		// FlushInterval -1 flushes every write immediately: gRPC streaming
		// frames (and unary trailers) must not be buffered, or a server-stream
		// would stall until the whole call completed.
		FlushInterval: -1,
		Transport: &grpcBalancingTransport{
			pool:     pool,
			base:     newGRPCTransport(loc, tlsBackend),
			log:      log,
			onStream: onStream,
		},
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)
			pr.SetXForwarded()
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
	return rp, nil
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
}

func (t *grpcBalancingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	b, err := t.pool.Pick()
	if err != nil {
		return nil, err
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
func newGRPCTransport(loc config.LocationConfig, tlsBackend bool) *http2.Transport {
	connectTimeout := loc.ProxyConnectTimeout.Std()
	if connectTimeout <= 0 {
		connectTimeout = 10 * time.Second
	}
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}

	if tlsBackend {
		return &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
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
