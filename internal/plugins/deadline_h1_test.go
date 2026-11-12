// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/reqdeadline"
)

// refusingDeadlineWriter exposes SetReadDeadline but refuses every deadline.
type refusingDeadlineWriter struct{ http.ResponseWriter }

func (refusingDeadlineWriter) SetReadDeadline(time.Time) error { return http.ErrNotSupported }

// h1Upload configures stalledH1With.
type h1Upload struct {
	// unregistered serves without reqdeadline.ConnContext, as an embedder's
	// own http.Server would.
	unregistered bool
	// native passes net/http's own writer instead of one refusing deadlines.
	native bool
	// wait is how long the client waits for a response; 5s when zero.
	wait time.Duration
	// declared and sent override the 100-byte declaration and the one byte
	// sent before the stall.
	declared int
	sent     string
}

// stalledH1 serves h through a real HTTP/1 server that registers its
// connections as Jul's server does, behind a writer refusing every read
// deadline. The client declares 100 body bytes, sends one and stalls; onStall
// runs once the upload is blocked. It returns the response status, or 0 if
// none arrived within 5 seconds.
func stalledH1(t *testing.T, h http.Handler, onStall func()) int {
	t.Helper()
	return stalledH1With(t, h, h1Upload{}, onStall)
}

func stalledH1With(t *testing.T, h http.Handler, u h1Upload, onStall func()) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !u.native {
			w = refusingDeadlineWriter{w}
		}
		h.ServeHTTP(w, r)
	}))
	if !u.unregistered {
		srv.Config.ConnContext = reqdeadline.ConnContext
	}
	srv.Start()
	defer srv.Close()
	c, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if u.declared == 0 {
		u.declared, u.sent = 100, "a"
	}
	if _, err := fmt.Fprintf(c, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: %d\r\n\r\n%s", u.declared, u.sent); err != nil {
		t.Fatal(err)
	}
	if onStall != nil {
		onStall()
	}
	if u.wait == 0 {
		u.wait = 5 * time.Second
	}
	_ = c.SetReadDeadline(time.Now().Add(u.wait))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestOverloadReachesStalledH1Upload: an overload refusal, at admission or
// when the body's buffer cannot grow, reaches a real HTTP/1 client whose
// upload has stalled. Without Connection: close, net/http would drain the
// unread body before the 503 and, with no read timeout, wait indefinitely.
func TestOverloadReachesStalledH1Upload(t *testing.T) {
	for _, c := range []struct {
		name  string
		limit uint64
		u     h1Upload
		hold  bool
	}{
		{"admission", 4096, h1Upload{native: true}, true},
		{"body allocation", 4096, h1Upload{native: true, declared: 20000, sent: strings.Repeat("m", 8000)}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, hooks, g, _ := govManager(t, limitsWith(c.limit, func(l *inspection.Limits) { l.BodyTimeout = 0 }))
			s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
			if c.hold {
				holder, _, _, err := g.AcquireLease(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer holder.Close()
			}
			var hits atomic.Int64
			if got := stalledH1With(t, s.Middleware("r")(echoNext(&hits)), c.u, nil); got != http.StatusServiceUnavailable {
				t.Fatalf("status %d, want 503 before the stalled body", got)
			}
			if inv, _, _, panics, _ := hooks.snapshot(); inv != 0 || panics != 0 || hits.Load() != 0 {
				t.Fatalf("invocations=%d panics=%d origin=%d", inv, panics, hits.Load())
			}
		})
	}
}

// TestRefusedAbortEndsRealH1Upload: when the writer exposes SetReadDeadline
// but refuses the abort, a real stalled HTTP/1 upload is still ended, on
// expiry and on retirement, and the request fails closed with 500 without a
// guest call. Closing the body could not end it: net/http's HTTP/1 body
// holds its lock while the read blocks.
func TestRefusedAbortEndsRealH1Upload(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		m, hooks, g, rec := govManager(t, limitsWith(1<<20, func(l *inspection.Limits) {
			l.Mode, l.BodyTimeout = inspection.Enforce, 25*time.Millisecond
		}))
		s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
		var hits atomic.Int64
		if got := stalledH1(t, s.Middleware("r")(echoNext(&hits)), nil); got != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500", got)
		}
		if _, timeouts := rec.snapshot(); !mapsEqual(timeouts, map[string]int{"h1/unsupported": 1}) {
			t.Fatalf("body timeout metrics %v", timeouts)
		}
		if inv, _, _, panics, _ := hooks.snapshot(); inv != 0 || panics != 0 || hits.Load() != 0 {
			t.Fatalf("inv=%d panics=%d origin=%d", inv, panics, hits.Load())
		}
		assertDrained(t, g)
	})
	t.Run("retirement", func(t *testing.T) {
		m, hooks := bodyManager(t)
		s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV1, guestEcho)})
		got := stalledH1(t, s.Handler("g"), func() {
			waitFor(t, "upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
			_ = s.Close()
		})
		if got != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500", got)
		}
		if reason := hooks.awaitFail(t); reason != bodyFailRetired {
			t.Fatalf("reason %s", reason)
		}
		if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
			t.Fatalf("invocations=%d panics=%d", n, panics)
		}
	})
}

// TestUnregisteredH1UploadFailsClosed: an embedder serving the plugin chain on
// its own http.Server without reqdeadline.ConnContext gives the owner no way
// to end a read whose abort is refused. An enforced body deadline then fails
// closed before reading: a real stalled HTTP/1 upload gets 500 and
// unsupported promptly, with no guest call, for net/http's own writer and for
// one refusing deadlines. Without an enforced deadline the read starts, and
// retirement must not wait for it: the request still fails as retired once
// the client gives up.
func TestUnregisteredH1UploadFailsClosed(t *testing.T) {
	for _, native := range []bool{true, false} {
		t.Run(fmt.Sprintf("expiry native=%v", native), func(t *testing.T) {
			m, hooks, g, rec := govManager(t, limitsWith(1<<20, func(l *inspection.Limits) {
				l.Mode, l.BodyTimeout = inspection.Enforce, 25*time.Millisecond
			}))
			s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
			var hits atomic.Int64
			got := stalledH1With(t, s.Middleware("r")(echoNext(&hits)), h1Upload{unregistered: true, native: native}, nil)
			if got != http.StatusInternalServerError {
				t.Fatalf("status %d, want 500 while the upload stalls", got)
			}
			if _, timeouts := rec.snapshot(); !mapsEqual(timeouts, map[string]int{"h1/unsupported": 1}) {
				t.Fatalf("body timeout metrics %v", timeouts)
			}
			if inv, _, _, panics, _ := hooks.snapshot(); inv != 0 || panics != 0 || hits.Load() != 0 {
				t.Fatalf("inv=%d panics=%d origin=%d", inv, panics, hits.Load())
			}
			assertDrained(t, g)
		})
	}
	t.Run("retirement without deadline", func(t *testing.T) {
		m, hooks := bodyManager(t)
		s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV1, guestEcho)})
		closed := make(chan struct{})
		var blocked bool
		stalledH1With(t, s.Handler("g"), h1Upload{unregistered: true, wait: 300 * time.Millisecond}, func() {
			waitFor(t, "upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
			go func() { defer close(closed); _ = s.Close() }()
			select {
			case <-closed:
			case <-time.After(150 * time.Millisecond):
				// The client gives up after its wait and frees the read.
				blocked = true
			}
		})
		<-closed
		if blocked {
			t.Fatal("retirement waited for the stalled HTTP/1 read")
		}
		if reason := hooks.awaitFail(t); reason != bodyFailRetired {
			t.Fatalf("reason %s", reason)
		}
		if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
			t.Fatalf("invocations=%d panics=%d", n, panics)
		}
	})
}
