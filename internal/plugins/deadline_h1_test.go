// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
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

// stalledH1 serves h through a real HTTP/1 server that registers its
// connections as Jul's server does, behind a writer refusing every read
// deadline. The client declares 100 body bytes, sends one and stalls; onStall
// runs once the upload is blocked. It returns the response status, or 0 if
// none arrived within 5 seconds.
func stalledH1(t *testing.T, h http.Handler, onStall func()) int {
	t.Helper()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(refusingDeadlineWriter{w}, r)
	}))
	srv.Config.ConnContext = reqdeadline.ConnContext
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
	if onStall != nil {
		onStall()
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		return 0
	}
	_ = resp.Body.Close()
	return resp.StatusCode
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
