// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// accountingProxy builds a proxy route against one backend with the supplied
// pool and location resilience blocks, returning the handler and the pool's
// admission owner.
func accountingProxy(t *testing.T, backend string, pool *config.ResilienceConfig, loc *config.LocationResilienceConfig) (http.Handler, *upstream.Admission) {
	t.Helper()
	ups := map[string]config.UpstreamConfig{
		"api": {
			Name:       "api",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: backend, Weight: 1}},
			MaxFails:   3,
			Resilience: pool,
		},
	}
	h, err := NewProxy(context.Background(), config.ServerConfig{},
		config.LocationConfig{
			Match:      config.MatchConfig{Type: "prefix", Path: "/"},
			ProxyPass:  "http://api",
			Resilience: loc,
		}, ups, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewProxy: %v", err)
	}
	ph := h.(*proxyHandler)
	t.Cleanup(func() { _ = ph.Close() })
	return ph, ph.admission
}

// TestAccountingHTTP11KeepAlive pins the first two rows of the accounting
// matrix. A request holds a slot for its own duration and releases it, whether
// or not the connection underneath is reused: the request limit counts requests,
// not sockets, so many sequential requests over one kept-alive connection never
// accumulate.
func TestAccountingHTTP11KeepAlive(t *testing.T) {
	var peak atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 8}, nil)
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r)
		if cur := adm.Active(); cur > peak.Load() {
			peak.Store(cur)
		}
	}))
	defer front.Close()

	client := front.Client()
	for i := 0; i < 20; i++ {
		resp, err := client.Get(front.URL + "/x")
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	if got := adm.Active(); got != 0 {
		t.Fatalf("active after 20 keep-alive requests = %d, want 0", got)
	}
}

// TestAccountingProtocolAgnostic pins the HTTP/2 and HTTP/3 rows at the point
// where they actually differ from HTTP/1.1: they do not.
//
// Admission counts one slot per *request*, and an h2 or h3 stream is one
// request in the same handler tree, reached through the same acquireGen path.
// Jul has no HTTP/3 backend transport — h3 is inbound only — so there is no
// protocol-specific code to test, and asserting the counting is identical for
// every value of r.Proto is the property that makes that true rather than
// merely believed. A real QUIC round trip is covered in internal/server.
func TestAccountingProtocolAgnostic(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 4}, nil)

	for _, proto := range []struct {
		name  string
		proto string
		major int
		minor int
	}{
		{"HTTP/1.1", "HTTP/1.1", 1, 1},
		{"HTTP/2.0", "HTTP/2.0", 2, 0},
		{"HTTP/3.0", "HTTP/3.0", 3, 0},
	} {
		t.Run(proto.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Proto, req.ProtoMajor, req.ProtoMinor = proto.proto, proto.major, proto.minor
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d", rec.Code)
			}
			if got := adm.Active(); got != 0 {
				t.Fatalf("active after one %s request = %d, want 0", proto.proto, got)
			}
		})
	}
}

// TestAccountingConcurrentStreams pins "+1 per stream": concurrent requests each
// hold their own slot, so the request limit is what binds on a multiplexed
// protocol where a single socket carries them all.
func TestAccountingConcurrentStreams(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 3}, nil)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	waitActive(t, adm, 3)

	// The fourth concurrent stream exceeds the request limit even though every
	// one of them could share a single backend socket.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("fourth concurrent stream: status = %d, want 503", rec.Code)
	}

	once.Do(func() { close(release) })
	wg.Wait()
	if got := adm.Active(); got != 0 {
		t.Fatalf("active at quiesce = %d, want 0", got)
	}
}

// TestAccountingWebSocketHoldsSlotForConnectionLifetime pins the WebSocket row.
// A 101 response is spliced bidirectionally and the slot must stay held for the
// whole upgraded connection, not just until the handshake completes — otherwise
// a gateway full of idle WebSockets would report itself empty.
//
// It also pins the half-close semantics, which are the standard library's and
// are easy to mistake for a leak: io.Copy returns a nil error at EOF, and
// ReverseProxy waits for the *second* copier when the first ends cleanly. So a
// backend that closes its side does not by itself end the tunnel — the client's
// half is still open — and the slot is correctly still held until the connection
// really ends.
func TestAccountingWebSocketHoldsSlotForConnectionLifetime(t *testing.T) {
	backendDone := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("backend ResponseWriter is not a Hijacker")
			return
		}
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		_ = buf.Flush()
		<-backendDone
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 4}, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	_, _ = fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	if !strings.Contains(line, "101") {
		t.Fatalf("status line = %q, want 101", line)
	}

	// The handshake is complete and the connection is spliced: the slot is still
	// held, because the logical request is the whole upgraded connection.
	waitActive(t, adm, 1)

	// Only the backend half closes. The tunnel is not over, so the slot stays.
	close(backendDone)
	time.Sleep(100 * time.Millisecond)
	if got := adm.Active(); got != 1 {
		t.Fatalf("active after a backend half-close = %d, want 1: the client half is still open", got)
	}

	// The client half closes too, ending the connection.
	conn.Close()
	deadline := time.Now().Add(5 * time.Second)
	for adm.Active() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slot was not released when the upgraded connection closed")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAccountingSSEHoldsSlotForResponseLifetime pins the server-sent-events row:
// the slot is held for the streaming response's whole lifetime, not released
// when the headers are flushed.
func TestAccountingSSEHoldsSlotForResponseLifetime(t *testing.T) {
	sentFirst := make(chan struct{})
	finish := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: one\n\n")
		w.(http.Flusher).Flush()
		close(sentFirst)
		<-finish
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 4}, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	resp, err := front.Client().Get(front.URL + "/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	<-sentFirst
	br := bufio.NewReader(resp.Body)
	if _, err := br.ReadString('\n'); err != nil {
		t.Fatalf("read first event: %v", err)
	}
	// The first event has been delivered and the stream is still open.
	if got := adm.Active(); got != 1 {
		t.Fatalf("active during an open SSE stream = %d, want 1", got)
	}

	close(finish)
	_, _ = io.Copy(io.Discard, resp.Body)
	deadline := time.Now().Add(5 * time.Second)
	for adm.Active() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the slot was not released when the SSE stream ended")
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAccountingClientDisconnectReleasesSlot pins that a client going away mid
// response returns the slot. A leak here would be worst exactly when it hurts
// most: clients abandoning a slow backend.
func TestAccountingClientDisconnectReleasesSlot(t *testing.T) {
	started := make(chan struct{})
	var once sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(started) })
		<-r.Context().Done()
	}))
	defer backend.Close()

	h, adm := accountingProxy(t, backend.Listener.Addr().String(),
		&config.ResilienceConfig{MaxActiveRequests: 4}, nil)
	front := httptest.NewServer(h)
	defer front.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL+"/slow", nil)
	errc := make(chan error, 1)
	go func() {
		resp, err := front.Client().Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		errc <- err
	}()

	<-started
	waitActive(t, adm, 1)
	cancel()
	<-errc

	deadline := time.Now().Add(5 * time.Second)
	for adm.Active() != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("active after client disconnect = %d, want 0", adm.Active())
		}
		time.Sleep(time.Millisecond)
	}
}

// TestAccountingPanicReleasesSlot pins that a panic in the handler chain does
// not leak a slot. The release is a defer, so this holds by construction — the
// test exists so a future refactor to an explicit release cannot break it
// silently.
func TestAccountingPanicReleasesSlot(t *testing.T) {
	adm := upstream.NewAdmission(nil)
	h := newAdmittedHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("backend handler exploded")
	}), adm, nil)

	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the panic to propagate")
			}
		}()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()

	if got := adm.Active(); got != 0 {
		t.Fatalf("active after a panic = %d, want 0", got)
	}
}

// TestMaxConnsPerHostBoundsSockets is the integration test the issue requires:
// it measures the documented semantics rather than trusting the standard
// library's documentation.
//
// It also covers the interaction with MaxIdleConnsPerHost = 32. The idle pool is
// larger than the connection bound here, which is the case that matters: if
// MaxConnsPerHost degraded pooling, keep-alive churn would open a new socket per
// request and the accepted-connection count would climb with request count
// instead of stopping at the bound.
func TestMaxConnsPerHostBoundsSockets(t *testing.T) {
	const limit = 3

	var accepted atomic.Int64
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	backend := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-release
			w.WriteHeader(http.StatusOK)
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateNew {
				accepted.Add(1)
			}
		},
	}
	go func() { _ = backend.Serve(ln) }()
	defer func() { _ = backend.Close() }()

	h, adm := accountingProxy(t, ln.Addr().String(),
		&config.ResilienceConfig{MaxConnectionsPerBackend: limit}, nil)

	// Far more concurrent requests than the socket bound. Admission is unlimited
	// here so the only thing that can hold them back is MaxConnsPerHost.
	const concurrency = 12
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
		}()
	}
	waitActive(t, adm, concurrency)

	// Every request is admitted — the request limit is unset — but only `limit`
	// sockets may exist, so the rest are queued inside the transport waiting for
	// a connection.
	deadline := time.Now().Add(2 * time.Second)
	for accepted.Load() < limit {
		if time.Now().After(deadline) {
			t.Fatalf("only %d connections were accepted, expected the bound of %d to be reached", accepted.Load(), limit)
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond) // let any excess dial land, if the bound leaked
	if got := accepted.Load(); got > limit {
		t.Fatalf("%d connections accepted, above max_connections_per_backend = %d", got, limit)
	}

	once.Do(func() { close(release) })
	wg.Wait()

	// Keep-alive churn: sequential requests must reuse the pooled connections
	// rather than opening a new socket each time.
	before := accepted.Load()
	for i := 0; i < 30; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}
	if got := accepted.Load(); got != before {
		t.Fatalf("keep-alive churn opened %d new connections; MaxConnsPerHost must not defeat pooling", got-before)
	}
}

// TestMaxConnsPerHostLocationOverridesPool pins the scope rule for the one
// stateless control in this slice: a location wins, and a zero at location level
// inherits rather than meaning unlimited.
func TestMaxConnsPerHostLocationOverridesPool(t *testing.T) {
	cases := []struct {
		name string
		pool *config.ResilienceConfig
		loc  *config.LocationResilienceConfig
		want int
	}{
		{"neither set", nil, nil, 0},
		{"pool only", &config.ResilienceConfig{MaxConnectionsPerBackend: 10}, nil, 10},
		{"location only", nil, &config.LocationResilienceConfig{MaxConnectionsPerBackend: 7}, 7},
		{"location wins", &config.ResilienceConfig{MaxConnectionsPerBackend: 10}, &config.LocationResilienceConfig{MaxConnectionsPerBackend: 7}, 7},
		{"zero at location inherits", &config.ResilienceConfig{MaxConnectionsPerBackend: 10}, &config.LocationResilienceConfig{}, 10},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := accountingProxy(t, "127.0.0.1:1", tc.pool, tc.loc)
			got := h.(*proxyHandler).transport.MaxConnsPerHost
			if got != tc.want {
				t.Fatalf("MaxConnsPerHost = %d, want %d", got, tc.want)
			}
		})
	}
}
