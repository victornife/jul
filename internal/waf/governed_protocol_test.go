// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"io"
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

type protoResult struct {
	status, major int
	body          string
	err           error
}

func protoPost(c *http.Client, url string, body io.Reader) protoResult {
	req, err := http.NewRequest(http.MethodPost, url, body)
	if err != nil {
		return protoResult{err: err}
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return protoResult{err: err}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return protoResult{status: resp.StatusCode, major: resp.ProtoMajor, body: strings.TrimSpace(string(b))}
}

// wafProtoServer serves a governed WAF route at /w and an uninspected sibling
// at /other over H1 or H2 (TLS), counting accepted connections.
func wafProtoServer(t *testing.T, fw *Firewall, h2 bool) (*http.Client, string, *atomic.Int64) {
	t.Helper()
	mux := http.NewServeMux()
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, r.Body) })
	mux.Handle("/w", fw.Middleware()(echo))
	mux.Handle("/other", echo)
	srv := httptest.NewUnstartedServer(mux)
	srv.Config.ConnContext = reqdeadline.ConnContext
	var conns atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	if h2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv.Client(), srv.URL, &conns
}

// TestGovernedWAFOutcomesOverH1H2 checks, on real servers, that WAF overload
// and body-deadline refusals reach the client, end only the affected H2
// stream, and leave an uninspected sibling route served.
func TestGovernedWAFOutcomesOverH1H2(t *testing.T) {
	for _, p := range []struct {
		name  string
		major int
	}{{"h1", 1}, {"h2", 2}} {
		t.Run(p.name+"/overload", func(t *testing.T) {
			g := newGovernor(t, governedLimits(inspection.Enforce))
			fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
			c, base, conns := wafProtoServer(t, fw, p.major == 2)
			if r := protoPost(c, base+"/other", strings.NewReader("warm")); r.err != nil || r.major != p.major {
				t.Fatalf("warm-up: %+v", r)
			}
			holder, _, _, err := g.AcquireLease(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			slot, _, err := g.TryEvaluation(context.Background(), holder)
			if err != nil {
				t.Fatal(err)
			}
			if r := protoPost(c, base+"/w", strings.NewReader("a=1")); r.err != nil || r.status != http.StatusServiceUnavailable ||
				r.major != p.major || r.body != inspection.ReasonOverloaded {
				t.Fatalf("saturated WAF route: %+v", r)
			}
			if r := protoPost(c, base+"/other", strings.NewReader("sibling")); r.status != http.StatusOK || r.body != "sibling" {
				t.Fatalf("sibling during saturation: %+v", r)
			}
			_ = slot.Release()
			_ = holder.Close()
			if r := protoPost(c, base+"/w", strings.NewReader("a=1")); r.status != http.StatusOK || r.body != "a=1" {
				t.Fatalf("after release: %+v", r)
			}
			if p.major == 2 && conns.Load() != 1 {
				t.Fatalf("overload closed the shared H2 connection (%d connections)", conns.Load())
			}
			waitDrained(t, g)
		})

		t.Run(p.name+"/body_timeout", func(t *testing.T) {
			const bodyTimeout = 300 * time.Millisecond
			limits := governedLimits(inspection.Enforce)
			limits.BodyTimeout = bodyTimeout
			g := newGovernor(t, limits)
			fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
			c, base, conns := wafProtoServer(t, fw, p.major == 2)
			if r := protoPost(c, base+"/other", strings.NewReader("warm")); r.err != nil || r.major != p.major {
				t.Fatalf("warm-up: %+v", r)
			}
			pr, pw := io.Pipe()
			defer pw.Close()
			res := make(chan protoResult, 1)
			begin := time.Now()
			go func() { res <- protoPost(c, base+"/w", pr) }()
			_, _ = pw.Write([]byte("a="))
			if r := protoPost(c, base+"/other", strings.NewReader("sibling")); r.status != http.StatusOK || r.body != "sibling" {
				t.Fatalf("sibling during stalled upload: %+v", r)
			}
			r := <-res
			elapsed := time.Since(begin)
			if r.err != nil || r.status != http.StatusRequestTimeout || r.major != p.major {
				t.Fatalf("stalled upload: %+v", r)
			}
			if elapsed < bodyTimeout || elapsed > 5*time.Second {
				t.Fatalf("408 after %s, want about %s", elapsed, bodyTimeout)
			}
			if p.major == 2 && conns.Load() != 1 {
				t.Fatalf("a stream deadline closed the shared H2 connection (%d connections)", conns.Load())
			}
			waitDrained(t, g)
		})
	}
}

func waitDrained(t *testing.T, g *inspection.Governor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for g.Snapshot().ActiveLeases != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	assertDrained(t, g)
}
