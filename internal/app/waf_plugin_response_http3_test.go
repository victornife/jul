// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && waf && http3

package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

func TestWAFPluginChainRespondsBeforeUnreadHTTP3BodyAndKeepsSibling(t *testing.T) {
	var originHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "healthy")
	}))
	defer backend.Close()

	h, factory := earlyWAFPluginHandler(t, backend.URL)
	certs := httptest.NewTLSServer(http.NotFoundHandler())
	t.Cleanup(certs.Close)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var conns atomic.Int64
	srv := &http3.Server{
		Handler:   h,
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

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, base+"/?probe=attack", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	type response struct {
		resp *http.Response
		err  error
	}
	result := make(chan response, 1)
	go func() {
		resp, err := client.Do(req)
		result <- response{resp: resp, err: err}
	}()
	bodyWrite := make(chan error, 1)
	go func() {
		_, err := pw.Write([]byte("partial="))
		bodyWrite <- err
	}()
	var rejected response
	select {
	case rejected = <-result:
	case <-time.After(5 * time.Second):
		_ = pw.Close()
		t.Fatal("WAF did not respond while the H3 request body was still open")
	}
	_ = pw.Close()
	if rejected.err != nil {
		t.Fatal(rejected.err)
	}
	body, err := io.ReadAll(rejected.resp.Body)
	_ = rejected.resp.Body.Close()
	if err != nil || rejected.resp.StatusCode != http.StatusForbidden || rejected.resp.ProtoMajor != 3 {
		t.Fatalf("early WAF response = HTTP/%d %d %q, err=%v", rejected.resp.ProtoMajor, rejected.resp.StatusCode, body, err)
	}
	if originHits.Load() != 0 {
		t.Fatal("early WAF response reached the origin")
	}
	select {
	case <-bodyWrite:
	case <-time.After(5 * time.Second):
		t.Fatal("client upload did not stop after the early response")
	}

	sibling, err := client.Get(base + "/?probe=clean")
	if err != nil {
		t.Fatal(err)
	}
	siblingBody, err := io.ReadAll(sibling.Body)
	_ = sibling.Body.Close()
	if err != nil || sibling.StatusCode != http.StatusOK || sibling.ProtoMajor != 3 || string(siblingBody) != "healthy" {
		t.Fatalf("healthy sibling = HTTP/%d %d %q, err=%v", sibling.ProtoMajor, sibling.StatusCode, siblingBody, err)
	}
	if conns.Load() != 1 {
		t.Fatalf("early response closed the shared H3 connection: %d connections", conns.Load())
	}
	if originHits.Load() != 1 {
		t.Fatalf("healthy sibling origin hits = %d, want 1", originHits.Load())
	}
	deadline := time.Now().Add(5 * time.Second)
	for factory.RT.Inspection.Snapshot().ActiveLeases != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := factory.RT.Inspection.Snapshot()
	if snapshot.ActiveLeases != 0 || snapshot.Usage != ([len(snapshot.Usage)]uint64{}) {
		t.Fatalf("early-response chain did not drain: %+v", snapshot)
	}
}
