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
	"testing"
	"time"

	"jul/internal/config"
)

// TestTimeoutConnReadDeadline proves the read side arms an inactivity deadline:
// a peer that never writes makes Read fail with a timeout (not block forever).
func TestTimeoutConnReadDeadline(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	tc := &timeoutConn{Conn: c1, readTimeout: 100 * time.Millisecond}
	_, err := tc.Read(make([]byte, 4)) // c2 never writes
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("read err = %v, want a timeout", err)
	}
}

// TestTimeoutConnWriteDeadline proves the write side arms an inactivity deadline
// (proxy_send_timeout): a peer that never reads makes Write fail with a timeout.
func TestTimeoutConnWriteDeadline(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	tc := &timeoutConn{Conn: c1, writeTimeout: 100 * time.Millisecond}
	_, err := tc.Write([]byte("blocked")) // c2 never reads from the synchronous pipe
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("write err = %v, want a timeout", err)
	}
}

// TestProxyTransportWrapsConnWhenTimeoutSet verifies the transport installs the
// timeoutConn wrapper exactly when a read/send bound is configured, and leaves
// the connection bare otherwise (so default deployments keep stdlib behaviour).
func TestProxyTransportWrapsConnWhenTimeoutSet(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { c.Close() })
		}
	}()

	withTimeout := newProxyTransport(config.LocationConfig{ProxyReadTimeout: config.Duration(time.Second)}, nil)
	c, err := withTimeout.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial (timeout set): %v", err)
	}
	defer c.Close()
	if _, ok := c.(*timeoutConn); !ok {
		t.Fatalf("conn = %T, want *timeoutConn when proxy_read_timeout is set", c)
	}

	bare := newProxyTransport(config.LocationConfig{}, nil)
	c2, err := bare.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial (no timeout): %v", err)
	}
	defer c2.Close()
	if _, ok := c2.(*timeoutConn); ok {
		t.Fatalf("conn wrapped in *timeoutConn with no proxy timeouts configured")
	}
}

// TestProxyReadTimeoutBoundsSlowBody proves a slow-trickle upstream that stalls
// mid-body past proxy_read_timeout cannot hang the client indefinitely: the
// body read fails promptly instead of blocking forever.
func TestProxyReadTimeoutBoundsSlowBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, _ := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "first-chunk")
		if fl != nil {
			fl.Flush()
		}
		// Hold the body open without sending more until the proxy gives up and
		// closes the upstream connection, which cancels this request's context.
		<-r.Context().Done()
	}))
	defer backend.Close()

	front := httptest.NewServer(newProxy(t, config.LocationConfig{
		ProxyPass:        backend.URL,
		ProxyReadTimeout: config.Duration(150 * time.Millisecond),
	}, nil))
	defer front.Close()

	start := time.Now()
	resp, err := http.Get(front.URL + "/slow")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if _, err := io.ReadAll(resp.Body); err == nil {
		t.Fatal("body read succeeded; want an error when upstream stalls past proxy_read_timeout")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("read did not abort promptly: took %s", elapsed)
	}
}

// TestProxyReadTimeoutAllowsSteadyStream proves the bound is per-read inactivity,
// not a total cap: a response streamed in steady chunks completes in full even
// though the whole transfer outlasts proxy_read_timeout, because no single gap
// between chunks exceeds it.
func TestProxyReadTimeoutAllowsSteadyStream(t *testing.T) {
	const chunks = 8
	const gap = 30 * time.Millisecond

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fl, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream") // ensure incremental flushing
		w.WriteHeader(http.StatusOK)
		for i := 0; i < chunks; i++ {
			_, _ = io.WriteString(w, "x")
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(gap)
		}
	}))
	defer backend.Close()

	front := httptest.NewServer(newProxy(t, config.LocationConfig{
		ProxyPass:        backend.URL,
		ProxyReadTimeout: config.Duration(200 * time.Millisecond), // > gap, < total (8*30ms)
	}, nil))
	defer front.Close()

	resp, err := http.Get(front.URL + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("steady stream read failed: %v", err)
	}
	if len(body) != chunks {
		t.Fatalf("received %d bytes, want %d (the full steady stream)", len(body), chunks)
	}
}
