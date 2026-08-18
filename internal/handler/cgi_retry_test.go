// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/upstream"

	"github.com/yookoala/gofast"
)

// TestFastCGIRetriesDialFailureOntoAnotherBackend pins the whole point of
// making FastCGI a pool member: an FPM instance that is down costs one failed
// dial, not the request.
func TestFastCGIRetriesDialFailureOntoAnotherBackend(t *testing.T) {
	liveAddr, hits := fakeFPM(t, "tcp", "127.0.0.1:0")

	ups := map[string]config.UpstreamConfig{
		"fpm": {
			Name:     "fpm",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: liveAddr, Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	h, _ := fastcgiFor(t, config.LocationConfig{FastCGIPass: "fpm"}, ups)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/index.php", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failing over to the live FPM: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fpm-ok") {
		t.Fatalf("body = %q, want the live backend's response", rec.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("live backend served %d requests, want 1", got)
	}
}

// TestFastCGIRetryEligibilityMatchesTheSharedRule pins that the CGI adapters
// answer to the same decision function as the HTTP proxy rather than a
// protocol-local rule: `POST` and `PATCH` are attempted once even against a
// pool of dead backends, while retry-safe methods walk the pool.
//
// A connection error does not prove the application did not accept, commit and
// then die, so replayability is not safety.
//
// With max_fails = 1 a single failure trips a backend, so the number of
// unavailable backends is the number of attempts.
func TestFastCGIRetryEligibilityMatchesTheSharedRule(t *testing.T) {
	for _, tc := range []struct {
		method       string
		wantAttempts int
	}{
		{http.MethodPost, 1},
		{http.MethodPatch, 1},
		{http.MethodGet, 3},
		{http.MethodPut, 3},
	} {
		t.Run(tc.method, func(t *testing.T) {
			ups := map[string]config.UpstreamConfig{
				"fpm": {
					Name:     "fpm",
					Strategy: "round_robin",
					Servers: []config.UpstreamServer{
						{Address: "127.0.0.1:1", Weight: 1},
						{Address: "127.0.0.1:2", Weight: 1},
						{Address: "127.0.0.1:3", Weight: 1},
					},
					MaxFails:    1,
					FailTimeout: config.Duration(time.Hour),
				},
			}
			pool, err := newCGIPool(ups["fpm"])
			if err != nil {
				t.Fatalf("pool: %v", err)
			}
			t.Cleanup(pool.Close)

			// No body, so replayability turns on the method alone — which is
			// the axis under test here.
			h := &fastcgiHandler{pool: pool, dialer: cgiDialer(config.LocationConfig{}), session: fcgiTestSession()}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(tc.method, "http://edge/index.php", nil))

			down := 0
			for _, b := range pool.Backends() {
				if !b.Available() {
					down++
				}
			}
			if down != tc.wantAttempts {
				t.Fatalf("%s tripped %d backends, want %d attempts", tc.method, down, tc.wantAttempts)
			}
		})
	}
}

// TestFastCGIRetryReplaysTheBody pins that a retried request carries its body
// again. Without the rewind the second backend would receive an empty body and
// answer a request nobody made — a silent wrong answer, which is worse than the
// failure the retry was meant to avoid.
//
// GetBody is set explicitly because net/http never sets one on a server
// request, so this exercises the rewind path itself rather than asserting
// something production would do on its own. The companion test below pins what
// production actually does with a bodied request.
func TestFastCGIRetryReplaysTheBody(t *testing.T) {
	liveAddr, stdin := recordingFPM(t)

	ups := map[string]config.UpstreamConfig{
		"fpm": {
			Name:     "fpm",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: liveAddr, Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	h, _ := fastcgiFor(t, config.LocationConfig{FastCGIPass: "fpm"}, ups)

	const payload = "replay-me"
	req := httptest.NewRequest(http.MethodPut, "http://edge/index.php", strings.NewReader(payload))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(payload)), nil
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if body, _ := stdin.Load().(string); body != payload {
		t.Fatalf("backend received body %q after the retry, want %q", body, payload)
	}
}

// TestFastCGIBodiedRequestIsNotRetried pins the deliberate limit: a server
// request carries no GetBody, so a request that really has a body is attempted
// once even with a retry-safe method. Buffering it to make it replayable is the
// unbounded-memory failure this programme exists to prevent, so this is a
// decision rather than a gap.
func TestFastCGIBodiedRequestIsNotRetried(t *testing.T) {
	ups := map[string]config.UpstreamConfig{
		"fpm": {
			Name:     "fpm",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: "127.0.0.1:2", Weight: 1},
				{Address: "127.0.0.1:3", Weight: 1},
			},
			MaxFails:    1,
			FailTimeout: config.Duration(time.Hour),
		},
	}
	pool, err := newCGIPool(ups["fpm"])
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)

	h := &fastcgiHandler{pool: pool, dialer: cgiDialer(config.LocationConfig{}), session: fcgiTestSession()}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "http://edge/index.php", strings.NewReader("payload")))

	down := 0
	for _, b := range pool.Backends() {
		if !b.Available() {
			down++
		}
	}
	if down != 1 {
		t.Fatalf("a bodied PUT tripped %d backends, want 1 attempt: replaying it would need the body buffered", down)
	}
}

// TestUWSGIRetriesDialFailureOntoAnotherBackend is the uWSGI half of the same
// property, exercising the other retry boundary: everything before
// writeCGIResponse.
func TestUWSGIRetriesDialFailureOntoAnotherBackend(t *testing.T) {
	live, hits := fakeUWSGI(t)

	ups := map[string]config.UpstreamConfig{
		"app": {
			Name:     "app",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: "127.0.0.1:1", Weight: 1},
				{Address: live, Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	h, _ := fastcgiFor(t, config.LocationConfig{UWSGIPass: "app"}, ups)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/app", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failing over: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "uwsgi-ok") {
		t.Fatalf("body = %q, want the live backend's response", rec.Body.String())
	}
	if got := hits.Load(); got != 1 {
		t.Fatalf("live backend served %d requests, want 1", got)
	}
}

// TestUWSGINoRetryOnceTheResponseHasStarted pins the boundary itself: a backend
// that accepts the request and answers must not be retried, whatever happens
// afterwards, because bytes have already reached the client.
func TestUWSGINoRetryOnceTheResponseHasStarted(t *testing.T) {
	var hits atomic.Int64
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			hits.Add(1)
			go func() {
				defer conn.Close()
				_, _ = io.Copy(io.Discard, conn)
				// Headers and a first chunk, then a hard close: the client has
				// already been written to, so there is nothing left to retry.
				_, _ = io.WriteString(conn, "Content-Type: text/plain\r\n\r\npartial")
			}()
		}
	}()

	ups := map[string]config.UpstreamConfig{
		"app": {
			Name:     "app",
			Strategy: "round_robin",
			Servers: []config.UpstreamServer{
				{Address: ln.Addr().String(), Weight: 1},
				{Address: ln.Addr().String(), Weight: 1},
			},
			MaxFails: 1000,
		},
	}
	h, _ := fastcgiFor(t, config.LocationConfig{UWSGIPass: "app"}, ups)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge/app", nil))

	if got := hits.Load(); got != 1 {
		t.Fatalf("backend was contacted %d times; a started response was retried", got)
	}
}

// recordingFPM is fakeFPM with the request body captured, so a replay can be
// proved rather than assumed.
func recordingFPM(t *testing.T) (addr string, stdin *atomic.Value) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var got atomic.Value
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer conn.Close()
				var body strings.Builder
				hdr := make([]byte, 8)
				for {
					if _, rerr := io.ReadFull(conn, hdr); rerr != nil {
						return
					}
					recType := hdr[1]
					reqID := uint16(hdr[2])<<8 | uint16(hdr[3])
					length := int(hdr[4])<<8 | int(hdr[5])
					padding := int(hdr[6])
					payload := make([]byte, length)
					if length > 0 {
						if _, rerr := io.ReadFull(conn, payload); rerr != nil {
							return
						}
					}
					if padding > 0 {
						if _, rerr := io.CopyN(io.Discard, conn, int64(padding)); rerr != nil {
							return
						}
					}
					if recType != 5 { // not STDIN
						continue
					}
					if length > 0 {
						body.Write(payload)
						continue
					}
					got.Store(body.String())
					writeFCGIRecord(conn, 6, reqID, []byte("Content-Type: text/plain\r\n\r\nfpm-ok"))
					writeFCGIRecord(conn, 6, reqID, nil)
					writeFCGIRecord(conn, 3, reqID, make([]byte, 8))
					return
				}
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), &got
}

// fakeUWSGI accepts a uWSGI request and answers with a minimal CGI response.
func fakeUWSGI(t *testing.T) (addr string, hits *atomic.Int64) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var count atomic.Int64
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			count.Add(1)
			go func() {
				defer conn.Close()
				// The framing does not matter for the property under test, only
				// that a response comes back once the request has been sent.
				_, _ = io.Copy(io.Discard, conn)
				_, _ = io.WriteString(conn, "Content-Type: text/plain\r\n\r\nuwsgi-ok")
			}()
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().String(), &count
}

// newCGIPool builds a pool directly, for tests that need to inspect backend
// state rather than go through the handler constructor.
func newCGIPool(cfg config.UpstreamConfig) (*upstream.Pool, error) {
	return upstream.NewPool(cfg, "http")
}

// fcgiTestSession is the same session chain the real handler builds, so an
// eligibility test exercises the production path rather than a stub.
func fcgiTestSession() gofast.SessionHandler {
	return gofast.Chain(
		gofast.BasicParamsMap,
		gofast.MapHeader,
		fcgiScriptParams(config.LocationConfig{}),
	)(gofast.BasicSession)
}
