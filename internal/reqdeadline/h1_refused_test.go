// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package reqdeadline

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// refusingWriter exposes SetReadDeadline but refuses every deadline.
type refusingWriter struct{ http.ResponseWriter }

func (refusingWriter) SetReadDeadline(time.Time) error { return http.ErrNotSupported }

// stalledH1Upload serves handler over a real HTTP/1 server, sends a request
// declaring 100 body bytes, sends one and stalls. It returns the response
// status, or 0 if none arrived within wait.
func stalledH1Upload(t *testing.T, register bool, handler http.HandlerFunc, wait time.Duration) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	if register {
		srv.Config.ConnContext = ConnContext
	}
	srv.Start()
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\na"); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(wait))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestRefusedH1ExpiryEndsRealRead: with a writer refusing the abort, expiry
// ends a real, stalled HTTP/1 body read through the registered connection,
// and the request fails closed with 500 while the client is still waiting.
func TestRefusedH1ExpiryEndsRealRead(t *testing.T) {
	got := stalledH1Upload(t, true, func(w http.ResponseWriter, r *http.Request) {
		o := NewForRequest(refusingWriter{w}, r)
		defer o.End()
		o.InterruptWith(r.Body)
		if err := o.BeginBodyRead(25*time.Millisecond, false, nil); err != nil {
			t.Errorf("BeginBodyRead: %v", err)
		}
		_, _ = io.ReadAll(r.Body)
		if res := o.EndBodyRead(); res != BodyUnsupported {
			t.Errorf("result %v, want BodyUnsupported", res)
		}
		w.WriteHeader(http.StatusInternalServerError)
	}, 5*time.Second)
	if got != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 before the client gave up", got)
	}
}

// TestRefusedH1RetirementEndsRealRead: a refused retirement abort ends a
// real, stalled HTTP/1 body read the same way.
func TestRefusedH1RetirementEndsRealRead(t *testing.T) {
	got := stalledH1Upload(t, true, func(w http.ResponseWriter, r *http.Request) {
		o := NewForRequest(refusingWriter{w}, r)
		defer o.End()
		o.InterruptWith(r.Body)
		if err := o.BeginBodyRead(time.Hour, false, nil); err != nil {
			t.Errorf("BeginBodyRead: %v", err)
		}
		time.AfterFunc(25*time.Millisecond, func() { _ = o.Abort() })
		_, err := io.ReadAll(r.Body)
		o.EndBodyRead()
		if err == nil {
			t.Error("read completed despite the abort")
		}
		w.WriteHeader(http.StatusInternalServerError)
	}, 5*time.Second)
	if got != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500 before the client gave up", got)
	}
}

// TestRefusedH1WithoutConnCannotEndRead documents why the connection is
// registered: closing a net/http HTTP/1 body waits for the blocked read.
func TestRefusedH1WithoutConnCannotEndRead(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the client deadline")
	}
	got := stalledH1Upload(t, false, func(w http.ResponseWriter, r *http.Request) {
		o := NewForRequest(refusingWriter{w}, r)
		defer o.End()
		o.InterruptWith(r.Body)
		_ = o.BeginBodyRead(25*time.Millisecond, false, nil)
		_, _ = io.ReadAll(r.Body)
		o.EndBodyRead()
		w.WriteHeader(http.StatusInternalServerError)
	}, 500*time.Millisecond)
	if got != 0 {
		t.Fatalf("status %d: closing the HTTP/1 body ended the read", got)
	}
}
