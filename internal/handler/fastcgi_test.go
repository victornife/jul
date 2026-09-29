// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/yookoala/gofast"

	"jul/internal/config"
)

type cgiFaultReader struct {
	data []byte
	err  error
}

type cgiDiscardWriter struct{ header http.Header }

func (w *cgiDiscardWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func TestFastCGIStderrIsBoundedWithoutShortWrites(t *testing.T) {
	var b boundedCGIStderr
	chunk := bytes.Repeat([]byte("x"), maxCGIStderrBytes+100)
	for i := 0; i < 3; i++ {
		if n, err := b.Write(chunk); err != nil || n != len(chunk) {
			t.Fatalf("stderr write = %d, %v", n, err)
		}
	}
	if b.Len() != maxCGIStderrBytes {
		t.Fatalf("stderr retained %d bytes", b.Len())
	}
}

func TestUWSGIRejectsOversizedVarBeforeFraming(t *testing.T) {
	h := &uwsgiHandler{loc: config.LocationConfig{FastCGIParams: map[string]string{"LARGE": strings.Repeat("x", 65536)}}}
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if err := h.sendRequest(nil, r, http.NoBody); err == nil || !strings.Contains(err.Error(), "var block") {
		t.Fatalf("oversized uWSGI var result = %v", err)
	}
}

func TestCGIRejectsInvalidHeaderTokensAndControls(t *testing.T) {
	for _, raw := range []string{"Bad(Name: value\r\n\r\n", "X-Value: has\x01control\r\n\r\n"} {
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), httptest.NewRecorder()); err == nil {
			t.Errorf("malformed CGI field was accepted: %q", raw)
		}
	}
}

func TestCGIInvalidLateHeaderDoesNotPublishPartialHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	err := writeCGIResponse(bufio.NewReader(strings.NewReader("X-Secret: leaked\r\nBad(Name: nope\r\n\r\n")), rec)
	if err == nil || rec.Header().Get("X-Secret") != "" {
		t.Fatalf("partial CGI headers published: error=%v header=%q", err, rec.Header().Get("X-Secret"))
	}
}

func TestCGIDropsHopHeadersAndConnectionNominations(t *testing.T) {
	rec := httptest.NewRecorder()
	raw := "Connection: X-Internal, keep-alive\r\nX-Internal: secret\r\nTransfer-Encoding: chunked\r\nX-End-To-End: kept\r\n\r\nbody"
	if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), rec); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Connection", "X-Internal", "Transfer-Encoding"} {
		if got := rec.Header().Get(name); got != "" {
			t.Errorf("hop header %s leaked: %q", name, got)
		}
	}
	if rec.Header().Get("X-End-To-End") != "kept" || rec.Body.String() != "body" {
		t.Fatalf("end-to-end response lost: %v %q", rec.Header(), rec.Body.String())
	}
}

func TestCGIRejectsAmbiguousResponseLengths(t *testing.T) {
	for _, headers := range []string{"Content-Length: 2\r\nContent-Length: 3\r\n", "Content-Length: 2, 2\r\n", "Content-Length: -1\r\n", "Content-Length: nonsense\r\n"} {
		rec := httptest.NewRecorder()
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(headers+"\r\nbody")), rec); err == nil {
			t.Errorf("invalid length accepted: %q", headers)
		}
		if rec.Header().Get("Content-Length") != "" {
			t.Errorf("invalid length published: %q", headers)
		}
	}
}
func (*cgiDiscardWriter) WriteHeader(int)             {}
func (*cgiDiscardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (r *cgiFaultReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, r.err
}

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
	t.Run("valid header larger than reader buffer", func(t *testing.T) {
		value := strings.Repeat("x", 8<<10)
		raw := "X-Large: " + value + "\r\n\r\nbody"
		rec := httptest.NewRecorder()
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), rec); err != nil {
			t.Fatal(err)
		}
		if got := rec.Header().Get("X-Large"); got != value {
			t.Fatalf("large header length = %d, want %d", len(got), len(value))
		}
		if got := rec.Body.String(); got != "body" {
			t.Fatalf("body = %q, want body", got)
		}
	})

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

	for _, tt := range []struct {
		name string
		raw  string
	}{
		{name: "empty status", raw: "Status:\r\n\r\n"},
		{name: "invalid status", raw: "Status: nope\r\n\r\n"},
		{name: "malformed header", raw: "not-a-header\r\n\r\n"},
		{name: "unterminated header", raw: "Content-Type: text/plain"},
		{name: "oversized header line", raw: "X-Large: " + strings.Repeat("x", cgiResponseHeaderMax) + "\r\n\r\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if err := writeCGIResponse(bufio.NewReader(strings.NewReader(tt.raw)), rec); err == nil {
				t.Fatal("malformed response was accepted")
			}
		})
	}
}

func TestWriteCGIResponseHeaderBounds(t *testing.T) {
	for _, size := range []int{(4 << 10) - 1, 4 << 10, (4 << 10) + 1, 16 << 10} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			value := strings.Repeat("x", size)
			raw := "X-Large: " + value + "\r\n\r\n"
			rec := httptest.NewRecorder()
			if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), rec); err != nil {
				t.Fatal(err)
			}
			if got := len(rec.Header().Get("X-Large")); got != size {
				t.Fatalf("header length = %d, want %d", got, size)
			}
		})
	}

	t.Run("multiple large headers below aggregate limit", func(t *testing.T) {
		value := strings.Repeat("x", 12<<10)
		raw := "X-One: " + value + "\r\nX-Two: " + value + "\r\n\r\n"
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), httptest.NewRecorder()); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("aggregate exactly at maximum", func(t *testing.T) {
		value := strings.Repeat("x", cgiResponseHeaderMax-len("X: ")-len("\r\n\r\n"))
		raw := "X: " + value + "\r\n\r\n"
		if got := len(raw); got != cgiResponseHeaderMax {
			t.Fatalf("fixture length = %d", got)
		}
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw)), httptest.NewRecorder()); err != nil {
			t.Fatal(err)
		}
	})

	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "aggregate one byte over", raw: "X: " + strings.Repeat("x", cgiResponseHeaderMax-len("X: ")-len("\r\n\r\n")+1) + "\r\n\r\n"},
		{name: "huge unterminated input", raw: "X: " + strings.Repeat("x", 4*cgiResponseHeaderMax)},
		{name: "newline beyond maximum", raw: "X: " + strings.Repeat("x", 4*cgiResponseHeaderMax) + "\n\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := writeCGIResponse(bufio.NewReader(strings.NewReader(tc.raw)), httptest.NewRecorder())
			if !errors.Is(err, errCGIResponseHeaderTooLarge) {
				t.Fatalf("error = %v, want bounded-header error", err)
			}
		})
	}

	t.Run("more than 256 fields", func(t *testing.T) {
		var raw strings.Builder
		for i := 0; i <= cgiResponseHeaderFields; i++ {
			fmt.Fprintf(&raw, "X-%d: value\r\n", i)
		}
		raw.WriteString("\r\n")
		if err := writeCGIResponse(bufio.NewReader(strings.NewReader(raw.String())), httptest.NewRecorder()); err == nil {
			t.Fatal("response with too many fields was accepted")
		}
	})

	t.Run("partial data plus reader error", func(t *testing.T) {
		want := errors.New("backend read failed")
		reader := &cgiFaultReader{data: []byte("X-Test: partial"), err: want}
		err := writeCGIResponse(bufio.NewReaderSize(reader, 4), httptest.NewRecorder())
		if !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}

func FuzzWriteCGIResponseHeaders(f *testing.F) {
	for _, seed := range [][]byte{
		[]byte("Status: 200 OK\r\nContent-Type: text/plain\r\n\r\nbody"),
		[]byte("X-Large: " + strings.Repeat("x", 8<<10) + "\r\n\r\n"),
		[]byte("X: " + strings.Repeat("x", cgiResponseHeaderMax+1)),
		[]byte("broken"),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = writeCGIResponse(bufio.NewReaderSize(bytes.NewReader(data), 64), &cgiDiscardWriter{})
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

// TestCGIParamsDropClientProxyHeader: a client "Proxy" header must not reach
// the application as HTTP_PROXY (httpoxy), for uWSGI and FastCGI alike, while
// an operator's explicit fastcgi_params value is still honoured.
func TestCGIParamsDropClientProxyHeader(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://edge.example/index.php", nil)
	r.Header.Set("Proxy", "http://attacker.example:8080")

	if v, ok := buildCGIParams(config.LocationConfig{}, r)["HTTP_PROXY"]; ok {
		t.Fatalf("uWSGI HTTP_PROXY = %q, want absent", v)
	}
	if v := buildCGIParams(config.LocationConfig{FastCGIParams: map[string]string{"HTTP_PROXY": "http://egress:3128"}}, r)["HTTP_PROXY"]; v != "http://egress:3128" {
		t.Fatalf("uWSGI operator HTTP_PROXY = %q", v)
	}

	fcgi := func(loc config.LocationConfig) map[string]string {
		var got map[string]string
		inner := func(_ gofast.Client, req *gofast.Request) (*gofast.ResponsePipe, error) {
			got = req.Params
			return nil, nil
		}
		req := &gofast.Request{Raw: r, Params: map[string]string{}}
		_, _ = gofast.Chain(gofast.MapHeader, fcgiScriptParams(loc))(inner)(nil, req)
		return got
	}
	if v, ok := fcgi(config.LocationConfig{})["HTTP_PROXY"]; ok {
		t.Fatalf("FastCGI HTTP_PROXY = %q, want absent", v)
	}
	if v := fcgi(config.LocationConfig{FastCGIParams: map[string]string{"HTTP_PROXY": "http://egress:3128"}})["HTTP_PROXY"]; v != "http://egress:3128" {
		t.Fatalf("FastCGI operator HTTP_PROXY = %q", v)
	}
}
