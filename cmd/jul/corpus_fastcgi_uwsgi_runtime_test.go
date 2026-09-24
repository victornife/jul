// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/fcgi"
	"strconv"
	"testing"
)

// TestNGINXCorpusFastCGIPassRealE2E proves the fastcgi_pass + literal
// fastcgi_param translation (#367) works end to end through a real Jul
// instance and a real FastCGI responder: Go's own standard-library
// net/http/fcgi.Serve, not a hand-simulated substitute. The backend reads
// X-Custom-Test - the CGI-standard header projection of the translated
// HTTP_X_CUSTOM_TEST fastcgi_param - proving the literal param actually
// reached the backend over the real FastCGI wire protocol.
func TestNGINXCorpusFastCGIPassRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "fastcgi-gateway-runtime")
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 {
		t.Fatalf("fastcgi-gateway-runtime: want 1 server with 1 location, got %+v", cfg.Servers)
	}
	loc := cfg.Servers[0].Locations[0]
	if loc.FastCGIPass == "" || loc.FastCGIParams["HTTP_X_CUSTOM_TEST"] != "hello-world" {
		t.Fatalf("fastcgi-gateway-runtime: want a fastcgi_pass location with HTTP_X_CUSTOM_TEST, got %+v", loc)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve FastCGI backend: %v", err)
	}
	go func() {
		_ = fcgi.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Echo-Custom", r.Header.Get("X-Custom-Test"))
			_, _ = io.WriteString(w, "fastcgi-response-ok")
		}))
	}()
	t.Cleanup(func() { _ = ln.Close() })
	cfg.Servers[0].Locations[0].FastCGIPass = ln.Addr().String()

	baseURL, cleanup := startRealJulForCorpus(t, "fastcgi-gateway-runtime", cfg)
	defer cleanup()

	resp, err := http.Get(baseURL + "/")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "fastcgi-response-ok" {
		t.Fatalf("body = %q, want %q", body, "fastcgi-response-ok")
	}
	if got := resp.Header.Get("X-Echo-Custom"); got != "hello-world" {
		t.Fatalf("X-Echo-Custom = %q, want %q (the fastcgi_param must have reached the real FastCGI backend)", got, "hello-world")
	}
}

// uwsgiParseVars decodes a uWSGI var block: each of key and value is a
// little-endian uint16 length followed by the raw bytes, matching the wire
// format internal/handler/fastcgi_test.go's TestUWSGIHandlerRoundTrip already
// exercises against Jul's own uWSGI client.
func uwsgiParseVars(b []byte) map[string]string {
	vars := map[string]string{}
	for len(b) >= 2 {
		kl := int(binary.LittleEndian.Uint16(b))
		b = b[2:]
		if len(b) < kl {
			break
		}
		key := string(b[:kl])
		b = b[kl:]
		if len(b) < 2 {
			break
		}
		vl := int(binary.LittleEndian.Uint16(b))
		b = b[2:]
		if len(b) < vl {
			break
		}
		val := string(b[:vl])
		b = b[vl:]
		vars[key] = val
	}
	return vars
}

// startUWSGIEchoBackend starts a real, minimal uWSGI-protocol responder: it
// decodes the request's var block, replies with a fixed response, and
// reports the decoded vars of the first accepted connection over gotVars.
func startUWSGIEchoBackend(t *testing.T, gotVars chan<- map[string]string) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve uWSGI backend: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveOneUWSGIRequest(conn, gotVars)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}

// serveOneUWSGIRequest handles exactly one uWSGI request/response exchange on
// conn - startRealJulForCorpus's own readiness probe consumes one connection
// before a test's real request ever gets sent, so the backend must accept
// (and answer) more than once.
func serveOneUWSGIRequest(conn net.Conn, gotVars chan<- map[string]string) {
	defer conn.Close()
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	size := int(binary.LittleEndian.Uint16(header[1:3]))
	varsBuf := make([]byte, size)
	if _, err := io.ReadFull(conn, varsBuf); err != nil {
		return
	}
	vars := uwsgiParseVars(varsBuf)
	// Read exactly CONTENT_LENGTH bytes of body rather than io.ReadAll:
	// a bodyless request (e.g. GET) has nothing to half-close, so
	// blocking for EOF here would hang forever waiting for a close the
	// client never sends.
	if n, err := strconv.Atoi(vars["CONTENT_LENGTH"]); err == nil && n > 0 {
		if _, err := io.CopyN(io.Discard, conn, int64(n)); err != nil {
			return
		}
	}
	gotVars <- vars
	_, _ = io.WriteString(conn, "Status: 200 OK\r\nContent-Type: text/plain\r\nX-Echo: uwsgi-ok\r\n\r\nuwsgi-response-ok")
}

// TestNGINXCorpusUWSGIPassRealE2E proves the uwsgi_pass translation (#367)
// works end to end through a real Jul instance and a real uWSGI-protocol
// backend. uwsgi_param has no Jul translation (no per-parameter uWSGI
// configuration equivalent exists, so it always stays blocking), so param
// propagation is instead proven through the client-header path every uWSGI
// request already carries: an inbound X-Test-Header maps onto the uwsgi var
// HTTP_X_TEST_HEADER, the same CGI-standard convention FastCGI uses.
func TestNGINXCorpusUWSGIPassRealE2E(t *testing.T) {
	cfg := loadCorpusRuntimeCandidate(t, "uwsgi-gateway-runtime")
	if len(cfg.Servers) != 1 || len(cfg.Servers[0].Locations) != 1 || cfg.Servers[0].Locations[0].UWSGIPass == "" {
		t.Fatalf("uwsgi-gateway-runtime: want 1 server with 1 uwsgi_pass location, got %+v", cfg.Servers)
	}

	gotVars := make(chan map[string]string, 4)
	backend := startUWSGIEchoBackend(t, gotVars)
	cfg.Servers[0].Locations[0].UWSGIPass = "tcp://" + backend.Addr().String()

	baseURL, cleanup := startRealJulForCorpus(t, "uwsgi-gateway-runtime", cfg)
	defer cleanup()

	// Drain the vars from startRealJulForCorpus's own plain-HTTP readiness
	// probe: it is a real request the backend already answered, so it holds
	// no X-Test-Header and must not be mistaken for this test's own request.
	select {
	case <-gotVars:
	default:
	}

	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("X-Test-Header", "uwsgi-e2e")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if string(body) != "uwsgi-response-ok" {
		t.Fatalf("body = %q, want %q", body, "uwsgi-response-ok")
	}
	if got := resp.Header.Get("X-Echo"); got != "uwsgi-ok" {
		t.Fatalf("X-Echo = %q, want %q", got, "uwsgi-ok")
	}

	select {
	case vars := <-gotVars:
		if got := vars["HTTP_X_TEST_HEADER"]; got != "uwsgi-e2e" {
			t.Fatalf("HTTP_X_TEST_HEADER = %q, want %q (the request header must reach the real uWSGI backend)", got, "uwsgi-e2e")
		}
	default:
		t.Fatal("uWSGI backend never received a connection")
	}
}
