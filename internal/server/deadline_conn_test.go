// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"jul/internal/reqdeadline"
)

type refusingDeadlineWriter struct{ http.ResponseWriter }

func (refusingDeadlineWriter) SetReadDeadline(time.Time) error { return http.ErrNotSupported }

// TestServerConnContextEndsRefusedH1Read: the server's ConnContext registers
// the HTTP/1 connection, so a refused deadline abort still ends a stalled
// upload and the request fails closed with 500.
func TestServerConnContextEndsRefusedH1Read(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := reqdeadline.NewForRequest(refusingDeadlineWriter{w}, r)
		defer o.End()
		_ = o.BeginBodyRead(25*time.Millisecond, false, nil)
		_, _ = io.ReadAll(r.Body)
		if o.EndBodyRead() == reqdeadline.BodyUnsupported {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	srv.Config.ConnContext = serverConnContext
	srv.Start()
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_, _ = io.WriteString(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\na")
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatalf("no response while the upload stalled: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", resp.StatusCode)
	}
}
