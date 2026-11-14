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
	"sync"
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

func wafResponseProtoServer(t *testing.T, fw *Firewall, h2 bool) (*http.Client, string, *atomic.Int64) {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle("/w", fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Large", strings.Repeat("x", 256))
		_, _ = io.WriteString(w, "must not be delivered")
	})))
	mux.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, r.Body) })
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

func TestGovernedWAFSSECommitReleasesH2TransactionAndPreservesSibling(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{}, g, Hooks{})
	flushed := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseHandler := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseHandler()

	mux := http.NewServeMux()
	mux.Handle("/events", fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		flushed <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = io.WriteString(w, "data: next\n\n")
		w.(http.Flusher).Flush()
	})))
	mux.HandleFunc("/other", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "sibling")
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.EnableHTTP2 = true
	srv.Config.ConnContext = reqdeadline.ConnContext
	var conns atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	client := srv.Client()

	type result struct {
		resp *http.Response
		err  error
	}
	event := make(chan result, 1)
	go func() {
		resp, err := client.Get(srv.URL + "/events")
		event <- result{resp: resp, err: err}
	}()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("event stream did not flush response headers")
	}
	var response result
	select {
	case response = <-event:
	case <-time.After(5 * time.Second):
		t.Fatal("client did not receive streaming response headers")
	}
	if response.err != nil {
		t.Fatal(response.err)
	}
	defer response.resp.Body.Close()
	if response.resp.ProtoMajor != 2 || response.resp.StatusCode != http.StatusOK {
		t.Fatalf("stream response = HTTP/%d %d", response.resp.ProtoMajor, response.resp.StatusCode)
	}
	during := g.Snapshot()
	if during.ActiveLeases != 0 || during.ActiveEvaluations != 0 ||
		during.Usage[inspection.WAFStateBytes] != 0 || during.Usage[inspection.AuditBytes] != 0 {
		t.Fatalf("H2 stream retained WAF transaction ownership after flush: %+v", during)
	}

	sibling, err := client.Get(srv.URL + "/other")
	if err != nil {
		t.Fatal(err)
	}
	siblingBody, err := io.ReadAll(sibling.Body)
	_ = sibling.Body.Close()
	if err != nil || sibling.StatusCode != http.StatusOK || string(siblingBody) != "sibling" {
		t.Fatalf("H2 sibling = %d %q, err=%v", sibling.StatusCode, siblingBody, err)
	}
	if conns.Load() != 1 {
		t.Fatalf("stream blocked H2 connection reuse: %d connections", conns.Load())
	}

	releaseHandler()
	body, err := io.ReadAll(response.resp.Body)
	if err != nil || string(body) != "data: next\n\n" {
		t.Fatalf("continued H2 stream = %q, err=%v", body, err)
	}
	waitDrained(t, g)
}

func TestGovernedWAFResponseHeaderCeilingOverH1H2(t *testing.T) {
	for _, p := range []struct {
		name  string
		major int
	}{{"h1", 1}, {"h2", 2}} {
		t.Run(p.name, func(t *testing.T) {
			limits := governedLimits(inspection.Enforce)
			limits.MaxResponseHeaderBytes = 128
			g := newGovernor(t, limits)
			fw := governedFirewall(t, config.WAFConfig{}, g, Hooks{})
			client, base, conns := wafResponseProtoServer(t, fw, p.major == 2)

			r := protoPost(client, base+"/w", strings.NewReader("a=1"))
			if r.err != nil || r.status != http.StatusInternalServerError || r.major != p.major || r.body != "" {
				t.Fatalf("oversized response: %+v", r)
			}
			if r := protoPost(client, base+"/other", strings.NewReader("sibling")); r.err != nil ||
				r.status != http.StatusOK || r.major != p.major || r.body != "sibling" {
				t.Fatalf("sibling after response rejection: %+v", r)
			}
			if conns.Load() != 1 {
				t.Fatalf("response rejection did not preserve connection reuse: %d connections", conns.Load())
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
