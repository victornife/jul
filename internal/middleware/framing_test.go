// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package middleware

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// framingServer records the paths it serves behind FramingGuard.
func framingServer(t *testing.T) (addr string, served func() []string) {
	t.Helper()
	var mu sync.Mutex
	var paths []string
	srv := httptest.NewServer(FramingGuard()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		_, _ = io.WriteString(w, "ok")
	})))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String(), func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), paths...)
	}
}

// rawExchange writes raw bytes and reads every response until the server
// closes the connection or goes quiet.
func rawExchange(t *testing.T, addr, raw string) (statuses []int, closed bool) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		resp, err := http.ReadResponse(br, nil)
		if err != nil {
			return statuses, err == io.EOF || strings.Contains(err.Error(), "EOF")
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		statuses = append(statuses, resp.StatusCode)
	}
}

func TestFramingGuardClosesAfterAmbiguousRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raw    string
		served []string
	}{
		{
			// RFC 9112 §6.3: TE overrides CL and the server MUST close; the
			// tail a Content-Length-framing front proxy considered body must
			// never be served.
			name:   "chunked with content-length and a smuggled tail",
			raw:    "POST /p HTTP/1.1\r\nHost: t\r\nContent-Length: 40\r\nTransfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n0\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: t\r\n\r\n",
			served: []string{"/p"},
		},
		{
			name:   "HTTP/1.0 keep-alive with transfer-encoding and a smuggled body",
			raw:    "POST /p HTTP/1.0\r\nHost: t\r\nConnection: keep-alive\r\nTransfer-Encoding: chunked\r\n\r\nGET /smuggled HTTP/1.1\r\nHost: t\r\n\r\n",
			served: []string{"/p"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr, served := framingServer(t)
			_, closed := rawExchange(t, addr, tc.raw)
			if !closed {
				t.Error("connection stayed open after an ambiguously framed request")
			}
			if got := served(); strings.Join(got, ",") != strings.Join(tc.served, ",") {
				t.Fatalf("served %v, want %v", got, tc.served)
			}
		})
	}
}

// Unambiguous requests keep HTTP keep-alive and pipelining.
func TestFramingGuardKeepsUnambiguousConnectionsOpen(t *testing.T) {
	addr, served := framingServer(t)
	statuses, _ := rawExchange(t, addr,
		"POST /a HTTP/1.1\r\nHost: t\r\nContent-Length: 5\r\n\r\nhello"+
			"GET /b HTTP/1.1\r\nHost: t\r\n\r\n"+
			"GET /c HTTP/1.0\r\nHost: t\r\nConnection: keep-alive\r\n\r\n")
	if len(statuses) != 3 || strings.Join(served(), ",") != "/a,/b,/c" {
		t.Fatalf("statuses %v served %v; a Content-Length or bodiless request lost keep-alive", statuses, served())
	}
}
