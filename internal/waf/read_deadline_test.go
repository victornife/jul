// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// An inner handler (a body-reading WASM plugin, #699) must be able to end a
// blocked request-body read through the WAF response interceptor with
// http.ResponseController, on HTTP/1.1 and HTTP/2, while flushing still goes
// through the interceptor rather than around it.
func TestWAFInterceptorForwardsReadDeadline(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "h1", true: "h2"}[h2], func(t *testing.T) {
			type outcome struct {
				setErr, readErr error
				flushThrough    bool
			}
			got := make(chan outcome, 1)
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ic, wrapped := newWAFResponseInterceptor(w, r, nil, http.StatusForbidden, nil, nil)
				buf := make([]byte, 1)
				if _, err := io.ReadFull(r.Body, buf); err != nil {
					got <- outcome{readErr: err}
					return
				}
				rc := http.NewResponseController(wrapped)
				o := outcome{setErr: rc.SetReadDeadline(time.Now().Add(-time.Second))}
				_, o.readErr = r.Body.Read(make([]byte, 16))
				_, isFlusher := wrapped.(http.Flusher)
				o.flushThrough = isFlusher && !ic.wroteHeader
				got <- o
			}))
			srv.EnableHTTP2 = h2
			if h2 {
				srv.StartTLS()
			} else {
				srv.Start()
			}
			defer srv.Close()
			pr, pw := io.Pipe()
			defer pw.Close()
			req, _ := http.NewRequest(http.MethodPost, srv.URL, pr)
			req.ContentLength = -1
			go func() {
				if resp, err := srv.Client().Do(req); err == nil {
					resp.Body.Close()
				}
			}()
			if _, err := pw.Write([]byte("x")); err != nil {
				t.Fatal(err)
			}
			select {
			case o := <-got:
				if o.setErr != nil {
					t.Fatalf("SetReadDeadline through the interceptor: %v", o.setErr)
				}
				if o.readErr == nil || errors.Is(o.readErr, io.EOF) {
					t.Fatalf("stalled body read after a past deadline = %v, want a deadline error", o.readErr)
				}
				if !o.flushThrough {
					t.Fatal("interceptor no longer owns Flush")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("blocked body read was not ended by the read deadline")
			}
		})
	}
}

// The interceptor's SetReadDeadline is a forwarder, so it reports support
// only when the writer it wraps applies read deadlines (ADR 0032 H6).
func TestWAFInterceptorReportsReadDeadlineSupport(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	ic, _ := newWAFResponseInterceptor(httptest.NewRecorder(), r, nil, http.StatusForbidden, nil, nil)
	if ic.SupportsReadDeadline() {
		t.Fatal("interceptor over a recorder claimed read-deadline support")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ic, _ := newWAFResponseInterceptor(w, r, nil, http.StatusForbidden, nil, nil)
		if !ic.SupportsReadDeadline() {
			w.WriteHeader(http.StatusTeapot)
		}
	}))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusTeapot {
		t.Fatal("interceptor over the HTTP/1 transport writer reported no read-deadline support")
	}
}
