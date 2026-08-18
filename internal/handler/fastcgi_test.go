// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestParseSocketAddress(t *testing.T) {
	cases := []struct{ in, net, addr string }{
		{"unix:/run/php/php-fpm.sock", "unix", "/run/php/php-fpm.sock"},
		{"tcp://127.0.0.1:9000", "tcp", "127.0.0.1:9000"},
		{"127.0.0.1:9000", "tcp", "127.0.0.1:9000"},
	}
	for _, c := range cases {
		n, a := parseSocketAddress(c.in)
		if n != c.net || a != c.addr {
			t.Errorf("parseSocketAddress(%q) = (%q,%q), want (%q,%q)", c.in, n, a, c.net, c.addr)
		}
	}
}

func TestScriptNameFor(t *testing.T) {
	if got := scriptNameFor("/info.php", nil); got != "/info.php" {
		t.Errorf("file path = %q", got)
	}
	if got := scriptNameFor("/app/", []string{"index.php"}); got != "/app/index.php" {
		t.Errorf("dir path = %q", got)
	}
	if got := scriptNameFor("/", nil); got != "/index.php" {
		t.Errorf("root = %q", got)
	}
}

func TestBuildCGIParams(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "http://edge.example/app/run?x=1", strings.NewReader("body"))
	r.Header.Set("X-Custom", "v")
	loc := config.LocationConfig{
		Root:          "/srv/app",
		FastCGIParams: map[string]string{"SERVER_SOFTWARE": "override"},
	}
	p := buildCGIParams(loc, r)

	if p["REQUEST_METHOD"] != "POST" {
		t.Errorf("REQUEST_METHOD = %q", p["REQUEST_METHOD"])
	}
	if p["QUERY_STRING"] != "x=1" {
		t.Errorf("QUERY_STRING = %q", p["QUERY_STRING"])
	}
	if p["SCRIPT_FILENAME"] == "" || !strings.HasSuffix(p["SCRIPT_FILENAME"], "run") {
		t.Errorf("SCRIPT_FILENAME = %q", p["SCRIPT_FILENAME"])
	}
	if p["HTTP_X_CUSTOM"] != "v" {
		t.Errorf("HTTP_X_CUSTOM = %q", p["HTTP_X_CUSTOM"])
	}
	if p["SERVER_SOFTWARE"] != "override" {
		t.Errorf("override not applied: SERVER_SOFTWARE = %q", p["SERVER_SOFTWARE"])
	}
}

func TestWriteCGIResponse(t *testing.T) {
	t.Run("status header", func(t *testing.T) {
		raw := "Status: 201 Created\r\nContent-Type: text/plain\r\n\r\nhi"
		rec := httptest.NewRecorder()
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), rec); err != nil && err != io.EOF {
			t.Fatal(err)
		}
		if rec.Code != http.StatusCreated {
			t.Errorf("status = %d", rec.Code)
		}
		if rec.Header().Get("Content-Type") != "text/plain" {
			t.Errorf("content-type = %q", rec.Header().Get("Content-Type"))
		}
		if rec.Body.String() != "hi" {
			t.Errorf("body = %q", rec.Body.String())
		}
	})

	t.Run("http status line", func(t *testing.T) {
		raw := "HTTP/1.1 404 Not Found\r\nContent-Type: text/html\r\n\r\nmissing"
		rec := httptest.NewRecorder()
		_ = writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), rec)
		if rec.Code != http.StatusNotFound {
			t.Errorf("status = %d", rec.Code)
		}
		if rec.Body.String() != "missing" {
			t.Errorf("body = %q", rec.Body.String())
		}
	})
}

// parseUWSGIVars decodes a uWSGI var block for test assertions.
func parseUWSGIVars(b []byte) map[string]string {
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

func TestUWSGIHandlerRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	type result struct {
		vars map[string]string
		body string
	}
	resCh := make(chan result, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		br := bufio.NewReader(conn)

		var header [4]byte
		if _, err := io.ReadFull(br, header[:]); err != nil {
			return
		}
		size := int(binary.LittleEndian.Uint16(header[1:3]))
		varsBuf := make([]byte, size)
		if _, err := io.ReadFull(br, varsBuf); err != nil {
			return
		}
		body, _ := io.ReadAll(br) // until CloseWrite EOF
		resCh <- result{vars: parseUWSGIVars(varsBuf), body: string(body)}

		_, _ = io.WriteString(conn, "Status: 200 OK\r\nContent-Type: text/plain\r\nX-Echo: ok\r\n\r\nuwsgi-response")
	}()

	loc := config.LocationConfig{UWSGIPass: "tcp://" + ln.Addr().String(), Root: "/srv"}
	h, err := NewFastCGI(context.Background(), config.ServerConfig{}, loc, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "http://edge/app.py", strings.NewReader("payload"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Body.String() != "uwsgi-response" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if rec.Header().Get("X-Echo") != "ok" {
		t.Errorf("X-Echo header = %q", rec.Header().Get("X-Echo"))
	}

	got := <-resCh
	if got.body != "payload" {
		t.Errorf("upstream body = %q", got.body)
	}
	if got.vars["REQUEST_METHOD"] != "POST" {
		t.Errorf("REQUEST_METHOD = %q", got.vars["REQUEST_METHOD"])
	}
	if got.vars["SCRIPT_NAME"] != "/app.py" {
		t.Errorf("SCRIPT_NAME = %q", got.vars["SCRIPT_NAME"])
	}
}
