// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"
)

// countingListener accepts and immediately parks every connection, counting how
// many were opened. A backend that is never spoken to is enough to observe
// whether anything dialled it.
func countingListener(t *testing.T) (addr string, opened *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var count atomic.Int64
	var mu sync.Mutex
	var conns []net.Conn
	done := make(chan struct{})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		close(done)
		_ = ln.Close()
		mu.Lock()
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
	})
	_ = done
	return ln.Addr().String(), &count
}

// TestFastCGIHandlerGenerationsDoNotLeak is the regression this slice exists to
// prevent.
//
// gofast.NewClientPool spawned an endless producer goroutine over an unbuffered
// channel, and SimpleClientFactory dials eagerly, so that goroutine sat blocked
// on the handoff holding a live backend connection. ClientPool has no Close, the
// handler retained no reference to it and had no Close of its own, so it was
// never generation-staged: **every** FastCGI handler generation leaked one
// goroutine and one open connection, on every reload, for the process lifetime.
//
// Building many generations must therefore open no connections at all and leave
// the goroutine count flat.
func TestFastCGIHandlerGenerationsDoNotLeak(t *testing.T) {
	addr, opened := countingListener(t)

	// Let anything started by an earlier test settle before sampling.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	before := runtime.NumGoroutine()

	const generations = 50
	for i := 0; i < generations; i++ {
		h, err := NewFastCGI(context.Background(), config.ServerConfig{},
			config.LocationConfig{FastCGIPass: "tcp://" + addr, Root: "/srv"}, nil, nil, nil)
		if err != nil {
			t.Fatalf("generation %d: %v", i, err)
		}
		// Retire it exactly as the handler generation would.
		if c, ok := h.(interface{ Close() error }); ok {
			if err := c.Close(); err != nil {
				t.Fatalf("generation %d close: %v", i, err)
			}
		}
	}

	runtime.GC()
	time.Sleep(100 * time.Millisecond)

	if got := opened.Load(); got != 0 {
		t.Fatalf("%d backend connections were opened by building %d handler generations that served no traffic; each one is leaked for the process lifetime", got, generations)
	}
	if grew := runtime.NumGoroutine() - before; grew > 4 {
		t.Fatalf("goroutines grew by %d across %d handler generations; the per-generation producer goroutine is back", grew, generations)
	}
}

// fakeFPM accepts one FastCGI connection and replies with a minimal valid
// response, so a test can prove a request reached a specific backend.
func fakeFPM(t *testing.T, network, address string) (addr string, hits *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen(network, address)
	if err != nil {
		t.Fatalf("listen %s %s: %v", network, address, err)
	}
	var count atomic.Int64
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			count.Add(1)
			go serveFakeFPM(conn)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), &count
}

// serveFakeFPM speaks just enough FastCGI to complete one request: it drains the
// request records and writes a STDOUT record carrying a minimal CGI response,
// then an end-of-request record.
func serveFakeFPM(conn net.Conn) {
	defer conn.Close()
	body := "Content-Type: text/plain\r\n\r\nfpm-ok"

	// Read records until the request's empty STDIN record arrives.
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(conn, hdr); err != nil {
			return
		}
		recType := hdr[1]
		reqID := uint16(hdr[2])<<8 | uint16(hdr[3])
		length := int(hdr[4])<<8 | int(hdr[5])
		padding := int(hdr[6])
		if length+padding > 0 {
			if _, err := io.CopyN(io.Discard, conn, int64(length+padding)); err != nil {
				return
			}
		}
		// Record type 5 is STDIN; a zero-length STDIN ends the request body.
		if recType == 5 && length == 0 {
			writeFCGIRecord(conn, 6, reqID, []byte(body)) // STDOUT
			writeFCGIRecord(conn, 6, reqID, nil)          // STDOUT EOF
			writeFCGIRecord(conn, 3, reqID, make([]byte, 8))
			return
		}
	}
}

func writeFCGIRecord(w io.Writer, recType byte, reqID uint16, payload []byte) {
	hdr := []byte{1, recType, byte(reqID >> 8), byte(reqID), byte(len(payload) >> 8), byte(len(payload)), 0, 0}
	_, _ = w.Write(hdr)
	if len(payload) > 0 {
		_, _ = w.Write(payload)
	}
}

func fastcgiFor(t *testing.T, loc config.LocationConfig, ups map[string]config.UpstreamConfig) (http.Handler, *upstream.Admission) {
	t.Helper()
	h, err := NewFastCGI(context.Background(), config.ServerConfig{}, loc, ups, nil, nil)
	if err != nil {
		t.Fatalf("NewFastCGI: %v", err)
	}
	ah, ok := h.(*admittedHandler)
	if !ok {
		t.Fatalf("NewFastCGI returned %T, want *admittedHandler: FastCGI must acquire admission", h)
	}
	t.Cleanup(func() { _ = ah.Close() })
	return ah, ah.admission
}

// TestFastCGIUnixSocketBackend pins the unix path end to end: the backend is
// selected, dialled on its own network and served, and its slot is released.
func TestFastCGIUnixSocketBackend(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "fpm.sock")
	_, hits := fakeFPM(t, "unix", sock)

	h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "unix:" + sock, Root: "/srv"}, nil)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fpm-ok") {
		t.Fatalf("body = %q, want the backend's response", rec.Body.String())
	}
	if hits.Load() != 1 {
		t.Fatalf("backend hits = %d, want 1", hits.Load())
	}
	if adm.Active() != 0 {
		t.Fatalf("active after the request = %d, want 0", adm.Active())
	}
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket vanished: %v", err)
	}
}

// TestFastCGINamedUpstreamLoadBalances proves an FPM pool is a real pool: a
// named upstream with several backends spreads requests across them. Before
// this slice `fastcgi_pass = "php"` silently dialled the TCP host "php".
func TestFastCGINamedUpstreamLoadBalances(t *testing.T) {
	addrA, hitsA := fakeFPM(t, "tcp", "127.0.0.1:0")
	addrB, hitsB := fakeFPM(t, "tcp", "127.0.0.1:0")

	ups := map[string]config.UpstreamConfig{
		"php": {
			Name:     "php",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: addrA, Weight: 1},
				{Address: addrB, Weight: 1},
			},
			MaxFails: 3,
		},
	}
	h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, ups)

	for i := 0; i < 6; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d: status = %d (%q)", i, rec.Code, rec.Body.String())
		}
	}
	if hitsA.Load() == 0 || hitsB.Load() == 0 {
		t.Fatalf("requests were not balanced: a=%d b=%d", hitsA.Load(), hitsB.Load())
	}
	if adm.Active() != 0 {
		t.Fatalf("active at quiesce = %d, want 0", adm.Active())
	}
}

// TestFastCGIAdmissionEnforcesLimit proves an FPM pool can be bounded, which is
// the point: PHP-FPM's pm.max_children is a hard ceiling, so admitting past it
// only builds a queue inside the application server where Jul cannot see it.
func TestFastCGIAdmissionEnforcesLimit(t *testing.T) {
	addr, _ := fakeFPM(t, "tcp", "127.0.0.1:0")
	ups := map[string]config.UpstreamConfig{
		"php": {
			Name:       "php",
			Strategy:   "round_robin",
			Servers:    []config.UpstreamServer{{Address: addr, Weight: 1}},
			MaxFails:   3,
			Resilience: &config.ResilienceConfig{MaxActiveRequests: 1},
		},
	}
	h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, ups)

	release, err := adm.Admit(context.Background(), nil)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status over the limit = %d, want 503", rec.Code)
	}
	release()
}

// TestFastCGIReleasesAdmissionOnBackendFailure pins the path where a leak would
// hurt most: an unreachable FPM.
func TestFastCGIReleasesAdmissionOnBackendFailure(t *testing.T) {
	h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "tcp://127.0.0.1:1", Root: "/srv"}, nil)
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
		if rec.Code == http.StatusOK {
			t.Fatalf("request %d unexpectedly succeeded", i)
		}
	}
	if adm.Active() != 0 {
		t.Fatalf("active after 10 failures = %d, want 0", adm.Active())
	}
}

// TestUWSGIHonoursConnectTimeout proves the connect timeout comes from the
// location rather than a hardcoded ten seconds, which is the difference between
// a route that fails fast and one that ties up a client for ten seconds.
func TestUWSGIHonoursConnectTimeout(t *testing.T) {
	// 203.0.113.0/24 is TEST-NET-3: routable-looking and guaranteed to blackhole,
	// so the dial hangs until the timeout rather than being refused.
	loc := config.LocationConfig{
		UWSGIPass:           "tcp://203.0.113.1:9999",
		ProxyConnectTimeout: config.Duration(150 * time.Millisecond),
	}
	h, err := NewFastCGI(context.Background(), config.ServerConfig{}, loc, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewFastCGI: %v", err)
	}

	start := time.Now()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.py", nil))
	elapsed := time.Since(start)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("dial took %s; proxy_connect_timeout of 150ms was ignored", elapsed)
	}
}

// TestFastCGIPoolAcrossStrategies proves an FPM pool is balanced by whichever
// strategy the upstream declares.
//
// least_conn is checked differently on purpose. It is not round robin: with
// sequential traffic every backend is idle at selection time, ties resolve to
// the first candidate, and sending everything to one backend is the correct
// answer to "who has the fewest in-flight requests". It is exercised under real
// concurrency below, where the question has a meaningful answer.
func TestFastCGIPoolAcrossStrategies(t *testing.T) {
	for _, strategy := range []string{"round_robin", "weighted_round_robin"} {
		t.Run(strategy, func(t *testing.T) {
			addrA, hitsA := fakeFPM(t, "tcp", "127.0.0.1:0")
			addrB, hitsB := fakeFPM(t, "tcp", "127.0.0.1:0")
			ups := map[string]config.UpstreamConfig{
				"php": {
					Name:     "php",
					Strategy: strategy,
					Servers: []config.UpstreamServer{
						{Address: addrA, Weight: 1},
						{Address: addrB, Weight: 1},
					},
					MaxFails: 3,
				},
			}
			h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, ups)

			for i := 0; i < 8; i++ {
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("request %d: status = %d (%q)", i, rec.Code, rec.Body.String())
				}
			}
			if hitsA.Load()+hitsB.Load() != 8 {
				t.Fatalf("backend hits = %d + %d, want 8 in total", hitsA.Load(), hitsB.Load())
			}
			if hitsA.Load() == 0 || hitsB.Load() == 0 {
				t.Fatalf("%s never used one of the backends: a=%d b=%d", strategy, hitsA.Load(), hitsB.Load())
			}
			if adm.Active() != 0 {
				t.Fatalf("active at quiesce = %d, want 0", adm.Active())
			}
		})
	}

	t.Run("least_conn under concurrency", func(t *testing.T) {
		addrA, hitsA := fakeFPM(t, "tcp", "127.0.0.1:0")
		addrB, hitsB := fakeFPM(t, "tcp", "127.0.0.1:0")
		ups := map[string]config.UpstreamConfig{
			"php": {
				Name:     "php",
				Strategy: "least_conn",
				Servers: []config.UpstreamServer{
					{Address: addrA, Weight: 1},
					{Address: addrB, Weight: 1},
				},
				MaxFails: 3,
			},
		}
		h, adm := fastcgiFor(t, config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, ups)

		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/index.php", nil))
			}()
		}
		wg.Wait()

		if hitsA.Load()+hitsB.Load() != 8 {
			t.Fatalf("backend hits = %d + %d, want 8 in total", hitsA.Load(), hitsB.Load())
		}
		if adm.Active() != 0 {
			t.Fatalf("active at quiesce = %d, want 0", adm.Active())
		}
	})
}

// TestCGIHandlerRejectsUnusableTargets pins the construction-time errors. A
// handler that cannot name its backend must fail the reload rather than build.
func TestCGIHandlerRejectsUnusableTargets(t *testing.T) {
	t.Run("empty fastcgi_pass", func(t *testing.T) {
		if _, err := NewFastCGI(context.Background(), config.ServerConfig{}, config.LocationConfig{}, nil, nil, nil); err == nil {
			t.Fatal("NewFastCGI accepted an empty fastcgi_pass")
		}
	})
	t.Run("empty uwsgi_pass", func(t *testing.T) {
		loc := config.LocationConfig{UWSGIPass: "   "}
		if _, err := NewFastCGI(context.Background(), config.ServerConfig{}, loc, nil, nil, nil); err == nil {
			t.Fatal("NewFastCGI accepted a blank uwsgi_pass")
		}
	})
}

// TestUpstreamErrorStatus pins the two backend-selection failures apart. Both
// are 503 to a client, but they are distinct errors so an operator is never told
// "no healthy backend" when the answer is "every backend is at capacity".
func TestUpstreamErrorStatus(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"no healthy backend", upstream.ErrNoAvailableBackend, http.StatusServiceUnavailable},
		{"all backends at capacity", upstream.ErrBackendAtCapacity, http.StatusServiceUnavailable},
		{"anything else", io.ErrUnexpectedEOF, http.StatusBadGateway},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := upstreamErrorStatus(tc.err); got != tc.want {
				t.Fatalf("status = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestUWSGIReleasesAdmissionOnBackendFailure mirrors the FastCGI case for the
// other CGI-family handler, and pins a behaviour uWSGI did not have before this
// slice: passive health. The first failures are 502s from the dial, and once
// max_fails consecutive failures trip the backend's cooldown the pool has no
// eligible backend left and answers 503. Either way the slot is returned.
func TestUWSGIReleasesAdmissionOnBackendFailure(t *testing.T) {
	loc := config.LocationConfig{UWSGIPass: "tcp://127.0.0.1:1", Root: "/srv"}
	h, err := NewFastCGI(context.Background(), config.ServerConfig{}, loc, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewFastCGI: %v", err)
	}
	ah := h.(*admittedHandler)
	t.Cleanup(func() { _ = ah.Close() })

	var sawDialFailure, sawCooldown bool
	for i := 0; i < 10; i++ {
		rec := httptest.NewRecorder()
		ah.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.py", nil))
		switch rec.Code {
		case http.StatusBadGateway:
			sawDialFailure = true
		case http.StatusServiceUnavailable:
			sawCooldown = true
		default:
			t.Fatalf("request %d: status = %d, want 502 or 503", i, rec.Code)
		}
	}
	if !sawDialFailure {
		t.Fatal("no request reported a dial failure")
	}
	if !sawCooldown {
		t.Fatal("the backend never tripped into passive cooldown; uWSGI is not accounting failures against its pool")
	}
	if ah.admission.Active() != 0 {
		t.Fatalf("active after 10 failures = %d, want 0", ah.admission.Active())
	}
}

// TestCGIHandlersResolveThroughRegistry pins that a named upstream goes through
// the registry, which owns the pool's lifecycle across reloads and runs its
// health checker — the whole point of pool membership. It also exercises the
// logging paths, which are silent in the other tests.
func TestCGIHandlersResolveThroughRegistry(t *testing.T) {
	reg := upstream.NewRegistry(upstream.RegistryOptions{})
	t.Cleanup(reg.CloseAll)
	reg.Begin()

	addr, _ := fakeFPM(t, "tcp", "127.0.0.1:0")
	ups := map[string]config.UpstreamConfig{
		"php": {
			Name:     "php",
			Strategy: "round_robin",
			Servers:  []config.UpstreamServer{{Address: addr, Weight: 1}},
			MaxFails: 3,
		},
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	fcgi, err := NewFastCGI(context.Background(), config.ServerConfig{},
		config.LocationConfig{FastCGIPass: "php", Root: "/srv"}, ups, reg, log)
	if err != nil {
		t.Fatalf("NewFastCGI: %v", err)
	}
	t.Cleanup(func() { _ = fcgi.(*admittedHandler).Close() })

	uwsgi, err := NewFastCGI(context.Background(), config.ServerConfig{},
		config.LocationConfig{UWSGIPass: "php", Root: "/srv"}, ups, reg, log)
	if err != nil {
		t.Fatalf("NewFastCGI (uwsgi): %v", err)
	}
	t.Cleanup(func() { _ = uwsgi.(*admittedHandler).Close() })

	// Both routes named the same upstream, so the registry handed them one pool.
	if fcgi.(*admittedHandler).admission != uwsgi.(*admittedHandler).admission {
		t.Fatal("two routes naming the same upstream did not share its pool")
	}

	rec := httptest.NewRecorder()
	fcgi.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (%q)", rec.Code, rec.Body.String())
	}

	// Exercise the error path with a logger attached: no eligible backend once
	// the pool is emptied.
	pool, err := reg.For(context.Background(), ups["php"], "")
	if err != nil {
		t.Fatalf("reg.For: %v", err)
	}
	pool.UpdateBackends(nil)

	rec = httptest.NewRecorder()
	fcgi.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/index.php", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status with no backends = %d, want 503", rec.Code)
	}
	rec = httptest.NewRecorder()
	uwsgi.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/app.py", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uwsgi status with no backends = %d, want 503", rec.Code)
	}
}
