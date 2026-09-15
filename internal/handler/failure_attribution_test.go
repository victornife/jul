// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

type failingResponseWriter struct{ header http.Header }

func (w *failingResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (*failingResponseWriter) WriteHeader(int) {}

func (*failingResponseWriter) Write([]byte) (int, error) {
	return 0, errors.New("downstream connection closed")
}

func inspectableHTTPProxy(t *testing.T, address string, maxFails int) (*proxyHandler, *upstream.Pool) {
	return inspectableHTTPProxyWithLocation(t, address, maxFails, config.LocationConfig{})
}

func inspectableHTTPProxyWithLocation(t *testing.T, address string, maxFails int, loc config.LocationConfig) (*proxyHandler, *upstream.Pool) {
	return inspectableHTTPProxyWithCircuit(t, address, maxFails, 5*time.Millisecond, loc)
}

func inspectableHTTPProxyWithCircuit(t *testing.T, address string, maxFails int, failTimeout time.Duration, loc config.LocationConfig) (*proxyHandler, *upstream.Pool) {
	t.Helper()
	ups := map[string]config.UpstreamConfig{
		"health": {
			Name:        "health",
			Strategy:    "round_robin",
			Servers:     []config.UpstreamServer{{Address: address, Weight: 1}},
			MaxFails:    maxFails,
			FailTimeout: config.Duration(failTimeout),
		},
	}
	loc.ProxyPass = "http://health"
	h, err := NewProxy(context.Background(), config.ServerConfig{}, loc, ups, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ph := h.(*proxyHandler)
	t.Cleanup(func() { _ = ph.Close() })
	pool := ph.ReverseProxy.Transport.(*balancingTransport).pool
	return ph, pool
}

func TestHTTPClientCancellationBeforeHeadersIsHealthNeutral(t *testing.T) {
	for _, maxFails := range []int{0, 1, 3} {
		t.Run("max_fails_"+strconv.Itoa(maxFails), func(t *testing.T) {
			accepted := make(chan struct{}, 4)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ok" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				accepted <- struct{}{}
				<-r.Context().Done()
			}))
			defer backend.Close()

			h, pool := inspectableHTTPProxy(t, strings.TrimPrefix(backend.URL, "http://"), maxFails)
			for i := 0; i < 3; i++ {
				ctx, cancel := context.WithCancel(context.Background())
				req := httptest.NewRequest(http.MethodGet, "http://edge/cancel", nil).WithContext(ctx)
				done := make(chan struct{})
				go func() {
					h.ServeHTTP(httptest.NewRecorder(), req)
					close(done)
				}()
				<-accepted
				cancel()
				<-done
			}

			b := pool.Backends()[0]
			if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
				t.Fatalf("cancellations changed backend health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/ok", nil))
			if rec.Code != http.StatusNoContent {
				t.Fatalf("healthy response after cancellation = %d, want 204", rec.Code)
			}
		})
	}
}

func TestHTTPHalfOpenClientCancellationReturnsProbe(t *testing.T) {
	accepted := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		close(accepted)
		<-r.Context().Done()
	}))
	defer backend.Close()

	// The half-open window uses fail_timeout too. Keep it comfortably wider
	// than a canceled HTTP round trip on loaded Windows runners; the old 5 ms
	// fixture could expire the replacement-probe window before ServeHTTP
	// returned and turn this attribution test into a scheduler benchmark.
	h, pool := inspectableHTTPProxyWithCircuit(t, strings.TrimPrefix(backend.URL, "http://"), 1, 250*time.Millisecond, config.LocationConfig{})
	failed, err := pool.Pick()
	if err != nil {
		t.Fatal(err)
	}
	pool.RecordAttempt(failed, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	pool.Release(failed.Backend)
	b := pool.Backends()[0]
	deadline := time.Now().Add(2 * time.Second)
	for b.CircuitStatus().State != upstream.StateCircuitHalfOpen {
		if time.Now().After(deadline) {
			t.Fatal("circuit did not reach the half-open precondition")
		}
		time.Sleep(time.Millisecond)
	}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://edge/cancel", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-accepted
	cancel()
	<-done
	if status := b.CircuitStatus(); status.State != upstream.StateCircuitHalfOpen || status.ProbesRemaining != 1 {
		t.Fatalf("neutral cancellation did not return half-open probe: state=%q remaining=%d", status.State, status.ProbesRemaining)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/ok", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("replacement half-open probe = %d, want 204", rec.Code)
	}
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("half-open cancellation leaked state: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestHTTPClientCancellationDuringResponseBodyIsHealthNeutral(t *testing.T) {
	headers := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		close(headers)
		<-r.Context().Done()
	}))
	defer backend.Close()

	h, pool := inspectableHTTPProxy(t, strings.TrimPrefix(backend.URL, "http://"), 3)
	prior, err := pool.Pick()
	if err != nil {
		t.Fatal(err)
	}
	pool.RecordAttempt(prior, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	pool.Release(prior.Backend)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://edge/stream", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-headers
	cancel()
	<-done

	b := pool.Backends()[0]
	if !b.Available() || b.FailCount() != 1 || b.Inflight() != 0 {
		t.Fatalf("mid-body cancellation changed prior health evidence: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestHTTPClientDeadlineBeforeHeadersIsHealthNeutral(t *testing.T) {
	accepted := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(accepted)
		<-r.Context().Done()
	}))
	defer backend.Close()

	h, pool := inspectableHTTPProxy(t, strings.TrimPrefix(backend.URL, "http://"), 1)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "http://edge/deadline", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-accepted
	<-done

	b := pool.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("client deadline changed backend health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestHTTPBackendConnectFailureStillTripsCircuit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	h, pool := inspectableHTTPProxy(t, addr, 1)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	b := pool.Backends()[0]
	if b.Available() {
		t.Fatal("genuine backend connection failure did not trip max_fails=1 circuit")
	}
}

func TestHTTPBackendReadTimeoutStillTripsCircuit(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "x")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer backend.Close()

	h, pool := inspectableHTTPProxyWithLocation(t, strings.TrimPrefix(backend.URL, "http://"), 1, config.LocationConfig{
		ProxyReadTimeout: config.Duration(20 * time.Millisecond),
	})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if pool.Backends()[0].Available() {
		t.Fatal("backend body read timeout after headers did not trip max_fails=1 circuit")
	}
}

func TestHTTPBackendResetStillTripsCircuit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}()

	h, pool := inspectableHTTPProxy(t, ln.Addr().String(), 1)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/", nil))
	if pool.Backends()[0].Available() {
		t.Fatal("backend connection reset did not trip max_fails=1 circuit")
	}
}

func TestUnixHTTPClientCancellationIsHealthNeutral(t *testing.T) {
	accepted := make(chan struct{})
	path := startUnixHTTPBackend(t, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(accepted)
		<-r.Context().Done()
	}))
	h, pool := inspectableHTTPProxy(t, "unix:"+path, 1)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "http://edge/cancel", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), req)
		close(done)
	}()
	<-accepted
	cancel()
	<-done

	b := pool.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("Unix cancellation changed backend health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestCGIClientCancellationIsHealthNeutral(t *testing.T) {
	for _, name := range []string{"fastcgi", "uwsgi"} {
		t.Run(name, func(t *testing.T) {
			_, p := inspectableHTTPProxy(t, "backend.test:80", 1)
			ctx, cancel := context.WithCancel(context.Background())
			at, err := p.Pick()
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			if name == "fastcgi" {
				(&fastcgiHandler{pool: p}).noteFailure(at, context.Canceled, ctx, ctx, false, "cancelled")
			} else {
				(&uwsgiHandler{pool: p}).noteFailure(at, context.Canceled, ctx, ctx, false, "cancelled")
			}
			p.Release(at.Backend)
			b := p.Backends()[0]
			if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
				t.Fatalf("%s cancellation changed backend health: available=%t fails=%d inflight=%d", name, b.Available(), b.FailCount(), b.Inflight())
			}
		})
	}
}

func TestFastCGIDownstreamWriteFailurePreservesPriorHealth(t *testing.T) {
	addr, _ := fakeFPM(t, "tcp", "127.0.0.1:0")
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "fastcgi-write",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Hour),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	prior, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	p.RecordAttempt(prior, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	p.Release(prior.Backend)

	h := &fastcgiHandler{pool: p, dialer: cgiDialer(config.LocationConfig{}), session: fcgiTestSession()}
	h.ServeHTTP(&failingResponseWriter{}, httptest.NewRequest(http.MethodGet, "http://edge/index.php", nil))
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 1 || b.Inflight() != 0 {
		t.Fatalf("FastCGI downstream write changed prior health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestUWSGIDownstreamWriteFailurePreservesPriorHealth(t *testing.T) {
	addr, _ := fakeUWSGI(t)
	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "uwsgi-write",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: addr, Weight: 1}},
		MaxFails:    3,
		FailTimeout: config.Duration(time.Hour),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	prior, err := p.Pick()
	if err != nil {
		t.Fatal(err)
	}
	p.RecordAttempt(prior, upstream.BackendProtocolFailure(upstream.ReasonUpstreamConnectFailed))
	p.Release(prior.Backend)

	(&uwsgiHandler{pool: p, dialer: &net.Dialer{}}).ServeHTTP(
		&failingResponseWriter{}, httptest.NewRequest(http.MethodGet, "http://edge/app", nil))
	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 1 || b.Inflight() != 0 {
		t.Fatalf("uWSGI downstream write changed prior health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestUWSGIClientCancellationDuringResponseIsHealthNeutral(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	requestRead := make(chan struct{})
	release := make(chan struct{})
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		close(requestRead)
		<-release
	}()
	t.Cleanup(func() { close(release) })

	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "uwsgi-cancel",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: ln.Addr().String(), Weight: 1}},
		MaxFails:    1,
		FailTimeout: config.Duration(time.Hour),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	h := &uwsgiHandler{pool: p, dialer: &net.Dialer{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/app", nil).WithContext(ctx))
		close(done)
	}()
	<-requestRead
	cancel()
	<-done

	b := p.Backends()[0]
	if !b.Available() || b.FailCount() != 0 || b.Inflight() != 0 {
		t.Fatalf("uWSGI response cancellation changed backend health: available=%t fails=%d inflight=%d", b.Available(), b.FailCount(), b.Inflight())
	}
}

func TestUWSGIMalformedResponseStillTripsCircuit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		conn, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn)
		_, _ = io.WriteString(conn, "Status:\r\n\r\n")
	}()

	p, err := upstream.NewPool(config.UpstreamConfig{
		Name:        "uwsgi-malformed",
		Strategy:    "round_robin",
		Servers:     []config.UpstreamServer{{Address: ln.Addr().String(), Weight: 1}},
		MaxFails:    1,
		FailTimeout: config.Duration(time.Hour),
	}, "http")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	(&uwsgiHandler{pool: p, dialer: &net.Dialer{}}).ServeHTTP(
		httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "http://edge/app", nil))
	if p.Backends()[0].Available() {
		t.Fatal("malformed uWSGI response did not trip max_fails=1 circuit")
	}
}
