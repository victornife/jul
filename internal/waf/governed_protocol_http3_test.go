// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf && http3

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

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"jul/internal/config"
	"jul/internal/inspection"
)

func TestGovernedWAFResponseHeaderCeilingOverH3(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.MaxResponseHeaderBytes = 128
	g := newGovernor(t, limits)
	fw := governedFirewall(t, config.WAFConfig{}, g, Hooks{})
	mux := http.NewServeMux()
	mux.Handle("/w", fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Large", strings.Repeat("x", 256))
		_, _ = io.WriteString(w, "must not be delivered")
	})))
	mux.HandleFunc("/other", func(w http.ResponseWriter, r *http.Request) { _, _ = io.Copy(w, r.Body) })

	certs := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certs.Close)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns atomic.Int64
	srv := &http3.Server{
		Handler:   mux,
		TLSConfig: certs.TLS,
		ConnContext: func(ctx context.Context, _ *quic.Conn) context.Context {
			conns.Add(1)
			return ctx
		},
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(pc)
	}()
	tr := &http3.Transport{TLSClientConfig: certs.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	t.Cleanup(func() {
		_ = tr.Close()
		_ = srv.Close()
		<-served
		_ = pc.Close()
	})
	base := "https://" + pc.LocalAddr().String()

	request := func(path, body string) (int, int, string, error) {
		req, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
		if err != nil {
			return 0, 0, "", err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, "", err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.ProtoMajor, string(b), err
	}

	status, major, body, err := request("/w", "a=1")
	if err != nil || status != http.StatusInternalServerError || major != 3 || body != "" {
		t.Fatalf("oversized response = %d HTTP/%d %q, err=%v", status, major, body, err)
	}
	status, major, body, err = request("/other", "sibling")
	if err != nil || status != http.StatusOK || major != 3 || body != "sibling" {
		t.Fatalf("sibling after response rejection = %d HTTP/%d %q, err=%v", status, major, body, err)
	}
	if conns.Load() != 1 {
		t.Fatalf("response rejection did not preserve H3 connection reuse: %d connections", conns.Load())
	}
	waitDrained(t, g)
}

func TestGovernedWAFSSECommitReleasesH3TransactionAndPreservesSibling(t *testing.T) {
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
	certs := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certs.Close)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns atomic.Int64
	srv := &http3.Server{
		Handler:   mux,
		TLSConfig: certs.TLS,
		ConnContext: func(ctx context.Context, _ *quic.Conn) context.Context {
			conns.Add(1)
			return ctx
		},
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		_ = srv.Serve(pc)
	}()
	tr := &http3.Transport{TLSClientConfig: certs.Client().Transport.(*http.Transport).TLSClientConfig.Clone()}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	t.Cleanup(func() {
		_ = tr.Close()
		_ = srv.Close()
		<-served
		_ = pc.Close()
	})
	base := "https://" + pc.LocalAddr().String()

	type result struct {
		resp *http.Response
		err  error
	}
	event := make(chan result, 1)
	go func() {
		resp, err := client.Get(base + "/events")
		event <- result{resp: resp, err: err}
	}()
	select {
	case <-flushed:
	case <-time.After(5 * time.Second):
		t.Fatal("H3 event stream did not flush response headers")
	}
	var response result
	select {
	case response = <-event:
	case <-time.After(5 * time.Second):
		t.Fatal("H3 client did not receive streaming response headers")
	}
	if response.err != nil {
		t.Fatal(response.err)
	}
	defer response.resp.Body.Close()
	if response.resp.ProtoMajor != 3 || response.resp.StatusCode != http.StatusOK {
		t.Fatalf("H3 stream response = HTTP/%d %d", response.resp.ProtoMajor, response.resp.StatusCode)
	}
	during := g.Snapshot()
	if during.ActiveLeases != 0 || during.ActiveEvaluations != 0 ||
		during.Usage[inspection.WAFStateBytes] != 0 || during.Usage[inspection.AuditBytes] != 0 ||
		during.Usage[inspection.BufferBytes] != 0 {
		t.Fatalf("H3 stream retained WAF transaction ownership after flush: %+v", during)
	}

	sibling, err := client.Get(base + "/other")
	if err != nil {
		t.Fatal(err)
	}
	siblingBody, err := io.ReadAll(sibling.Body)
	_ = sibling.Body.Close()
	if err != nil || sibling.StatusCode != http.StatusOK || sibling.ProtoMajor != 3 || string(siblingBody) != "sibling" {
		t.Fatalf("H3 sibling = HTTP/%d %d %q, err=%v", sibling.ProtoMajor, sibling.StatusCode, siblingBody, err)
	}
	if conns.Load() != 1 {
		t.Fatalf("stream blocked H3 connection reuse: %d connections", conns.Load())
	}

	releaseHandler()
	body, err := io.ReadAll(response.resp.Body)
	if err != nil || string(body) != "data: next\n\n" {
		t.Fatalf("continued H3 stream = %q, err=%v", body, err)
	}
	waitDrained(t, g)
}
