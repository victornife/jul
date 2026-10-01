// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// failingBody stands in for a client upload whose framing breaks mid-stream.
type failingBody struct{ sent bool }

func (b *failingBody) Read(p []byte) (int, error) {
	if !b.sent {
		b.sent = true
		return copy(p, "hel"), nil
	}
	return 0, errors.New("invalid byte in chunk length")
}
func (*failingBody) Close() error { return nil }

func clientBodyProxy(t *testing.T) (http.Handler, *atomic.Int64) {
	t.Helper()
	var gets atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if r.Method == http.MethodGet {
			gets.Add(1)
		}
		_, _ = io.WriteString(w, "ok")
	}))
	t.Cleanup(backend.Close)
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://app"}, map[string]config.UpstreamConfig{
		"app": {Name: "app", Servers: []config.UpstreamServer{{Address: backend.Listener.Addr().String(), Weight: 1}}, MaxFails: 1},
	})
	return h, &gets
}

// A client body that cannot be read is answered 400 and never counts against
// the backend: with max_fails = 1, one such request used to take the only
// backend out of rotation.
func TestProxyMalformedClientBodyDoesNotFailBackend(t *testing.T) {
	h, gets := clientBodyProxy(t)
	for i := range 3 {
		req := httptest.NewRequest(http.MethodPost, "http://edge.example/upload", nil)
		req.Body = &failingBody{}
		req.ContentLength = -1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("malformed body %d: status = %d, want 400", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/page", nil))
	if rec.Code != http.StatusOK || gets.Load() != 1 {
		t.Fatalf("GET after malformed uploads: status %d, backend GETs %d; the backend was marked down", rec.Code, gets.Load())
	}
}

// A body over client_max_body_size is the client's 413, not the backend's.
func TestProxyOversizedClientBodyIs413(t *testing.T) {
	h, gets := clientBodyProxy(t)
	for i := range 3 {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "http://edge.example/upload", nil)
		req.Body = http.MaxBytesReader(rec, io.NopCloser(bytes.NewReader(make([]byte, 64<<10))), 1024)
		req.ContentLength = -1
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversized body %d: status = %d, want 413", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://edge.example/page", nil))
	if rec.Code != http.StatusOK || gets.Load() != 1 {
		t.Fatalf("GET after oversized uploads: status %d; the backend was marked down", rec.Code)
	}
}

// End to end over a real listener: a malformed chunked upload from the wire
// gets 400 and the backend stays in rotation.
func TestProxyMalformedChunkedUploadOverTheWire(t *testing.T) {
	h, gets := clientBodyProxy(t)
	front := httptest.NewServer(h)
	defer front.Close()
	for range 3 {
		conn, err := net.Dial("tcp", front.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, "POST /x HTTP/1.1\r\nHost: t\r\nTransfer-Encoding: chunked\r\n\r\nzz\r\nhello\r\n0\r\n\r\n")
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode >= 500 {
			t.Fatalf("malformed chunked upload: status %d, want a 4xx client error", resp.StatusCode)
		}
	}
	resp, err := http.Get(front.URL + "/page")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "ok") || gets.Load() != 1 {
		t.Fatalf("GET after malformed chunked uploads: status %d body %q", resp.StatusCode, body)
	}
}
