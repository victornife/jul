// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package reqdeadline

import (
	"bufio"
	"errors"
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

// TestUnregisteredH1FailsClosedBeforeRead: on an HTTP/1 server that does not
// register its connections nothing could end a read whose abort is refused,
// so an enforced body deadline fails closed before reading, whether the
// writer is net/http's own or one that refuses deadlines. The client, which
// never sends the rest of its body, receives 500.
func TestUnregisteredH1FailsClosedBeforeRead(t *testing.T) {
	for name, wrap := range map[string]func(http.ResponseWriter) http.ResponseWriter{
		"native":   func(w http.ResponseWriter) http.ResponseWriter { return w },
		"refusing": func(w http.ResponseWriter) http.ResponseWriter { return refusingWriter{w} },
	} {
		t.Run(name, func(t *testing.T) {
			var results []string
			got := stalledH1Upload(t, false, func(w http.ResponseWriter, r *http.Request) {
				o := NewForRequest(wrap(w), r)
				defer o.End()
				o.InterruptWith(r.Body)
				err := o.BeginBodyRead(25*time.Millisecond, false, func(proto, result string) {
					results = append(results, proto+"/"+result)
				})
				o.EndBodyRead()
				if !errors.Is(err, ErrUnsupported) {
					t.Errorf("BeginBodyRead = %v, want ErrUnsupported before reading", err)
					_, _ = io.ReadAll(r.Body)
				}
				// As the plugin path does: net/http would otherwise drain
				// the unread body before answering.
				w.Header().Set("Connection", "close")
				w.WriteHeader(http.StatusInternalServerError)
			}, 5*time.Second)
			if got != http.StatusInternalServerError {
				t.Fatalf("status %d, want 500 before the client gave up", got)
			}
			if len(results) != 1 || results[0] != "h1/unsupported" {
				t.Fatalf("results %v, want one h1/unsupported", results)
			}
		})
	}
}

// TestUnregisteredH1RefusedAbortNeverWaitsForRead: without an enforced body
// deadline the read starts, and a refused retirement abort must not close the
// HTTP/1 body, which would wait for the stalled read while the caller holds
// the plugin's preparation lock.
func TestUnregisteredH1RefusedAbortNeverWaitsForRead(t *testing.T) {
	abortTook := make(chan time.Duration, 1)
	stalledH1Upload(t, false, func(w http.ResponseWriter, r *http.Request) {
		o := NewForRequest(refusingWriter{w}, r)
		defer o.End()
		o.InterruptWith(r.Body)
		if err := o.BeginBodyRead(0, false, nil); err != nil {
			t.Errorf("BeginBodyRead: %v", err)
		}
		time.AfterFunc(25*time.Millisecond, func() {
			start := time.Now()
			if err := o.Abort(); !errors.Is(err, ErrUnsupported) {
				t.Errorf("Abort = %v, want ErrUnsupported", err)
			}
			abortTook <- time.Since(start)
		})
		_, _ = io.ReadAll(r.Body)
		o.EndBodyRead()
	}, time.Second)
	select {
	case d := <-abortTook:
		if d > 500*time.Millisecond {
			t.Fatalf("refused abort took %v: it waited for the stalled read", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("refused abort never returned")
	}
}
