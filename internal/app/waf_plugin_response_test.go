// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins && waf

package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
)

type blockingResponseWriter struct {
	header  http.Header
	entered chan []byte
	release <-chan struct{}
	status  int
}

func (w *blockingResponseWriter) Header() http.Header { return w.header }

func (w *blockingResponseWriter) WriteHeader(status int) { w.status = status }

func (w *blockingResponseWriter) Write(body []byte) (int, error) {
	w.entered <- append([]byte(nil), body...)
	<-w.release
	return len(body), nil
}

func earlyWAFPluginHandler(t *testing.T, backend string) (http.Handler, *HandlerFactory) {
	t.Helper()
	redactor := config.PluginConfig{
		Path:        filepath.Join("..", "..", "testdata", "plugins", "v2-redact.wasm"),
		ABI:         config.PluginABIV2,
		MemoryLimit: config.Size(32 << 20),
		Config:      map[string]string{"secrets": "secret"},
	}
	return buildV2Handler(t, backend, redactor, func(c *config.Config) {
		budget := config.Size(8 << 30)
		c.Global.Inspection.MaxBufferedBytes = &budget
		c.Servers[0].Locations[0].WAF = &config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			BlockStatus: http.StatusForbidden,
			InlineRules: `SecRule ARGS:probe "@streq attack" "id:9702,phase:1,deny,status:403,log"`,
		}
	})
}

func TestWAFInspectsPluginTransformedResponseOnRealServer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "secret")
	}))
	defer backend.Close()

	h, _ := buildV2Handler(t, backend.URL, v2Plugin("upper"), func(c *config.Config) {
		budget := config.Size(8 << 30)
		c.Global.Inspection.MaxBufferedBytes = &budget
		c.Servers[0].Locations[0].WAF = &config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       http.StatusForbidden,
			ResponseBodyCheck: true,
			InlineRules:       `SecRule RESPONSE_BODY "@streq SECRET" "id:9701,phase:4,deny,status:403,log"`,
		}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Mode", "body")
	client := srv.Client()
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "SECRET") {
		t.Fatalf("WAF did not inspect and block the plugin-transformed response: %d %q", resp.StatusCode, body)
	}
}

func TestNestedWAFPluginResponseKeepsChargesDuringBackpressure(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "secret")
	}))
	defer backend.Close()

	h, factory := buildV2Handler(t, backend.URL, v2Plugin("upper"), func(c *config.Config) {
		budget := config.Size(8 << 30)
		c.Global.Inspection.MaxBufferedBytes = &budget
		c.Servers[0].Locations[0].WAF = &config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       http.StatusForbidden,
			ResponseBodyCheck: true,
			InlineRules:       "SecResponseBodyAccess On\nSecResponseBodyMimeType text/plain\nSecResponseBodyLimit 1024\nSecResponseBodyLimitAction ProcessPartial",
		}
	})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWriter := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWriter()
	writer := &blockingResponseWriter{
		header:  make(http.Header),
		entered: make(chan []byte, 1),
		release: release,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		req, _ := http.NewRequest(http.MethodGet, "http://example.test/", nil)
		req.Header.Set("X-Mode", "body")
		h.ServeHTTP(writer, req)
	}()
	select {
	case body := <-writer.entered:
		if string(body) != "SECRET" {
			t.Fatalf("nested response = %q, want the plugin transform", body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nested response did not reach the blocked downstream writer")
	}
	during := factory.RT.Inspection.Snapshot()
	if during.ActiveLeases != 1 || during.Usage[inspection.WAFStateBytes] == 0 ||
		during.Usage[inspection.BufferBytes] < 2*uint64(len("SECRET")) {
		t.Fatalf("nested response charges were released under backpressure: %+v", during)
	}
	releaseWriter()
	<-done
	if writer.status != http.StatusOK {
		t.Fatalf("nested response status = %d, want 200", writer.status)
	}
	if snapshot := factory.RT.Inspection.Snapshot(); snapshot.ActiveLeases != 0 ||
		snapshot.Usage != ([len(snapshot.Usage)]uint64{}) {
		t.Fatalf("nested response charges did not drain: %+v", snapshot)
	}
}

func TestNestedWAFPluginResponseDrainsAfterClientDisconnect(t *testing.T) {
	backendStarted := make(chan struct{})
	backendCanceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(backendStarted)
		<-r.Context().Done()
		close(backendCanceled)
	}))
	defer backend.Close()

	redactor := config.PluginConfig{
		Path:        filepath.Join("..", "..", "testdata", "plugins", "v2-redact.wasm"),
		ABI:         config.PluginABIV2,
		MemoryLimit: config.Size(32 << 20),
		Config:      map[string]string{"secrets": "secret"},
	}
	h, factory := buildV2Handler(t, backend.URL, redactor, func(c *config.Config) {
		budget := config.Size(8 << 30)
		c.Global.Inspection.MaxBufferedBytes = &budget
		c.Servers[0].Locations[0].WAF = &config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       http.StatusForbidden,
			ResponseBodyCheck: true,
			InlineRules:       "SecResponseBodyAccess On\nSecResponseBodyMimeType text/plain\nSecResponseBodyLimit 1024\nSecResponseBodyLimitAction ProcessPartial",
		}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Mode", "body")
	result := make(chan error, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		result <- err
	}()
	select {
	case <-backendStarted:
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("proxy did not start the backend request")
	}
	cancel()
	select {
	case <-backendCanceled:
	case <-time.After(5 * time.Second):
		t.Fatal("client cancellation did not reach the backend")
	}
	select {
	case <-result:
	case <-time.After(5 * time.Second):
		t.Fatal("client request did not terminate after cancellation")
	}
	deadline := time.Now().Add(5 * time.Second)
	for factory.RT.Inspection.Snapshot().ActiveLeases != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := factory.RT.Inspection.Snapshot()
	if snapshot.ActiveLeases != 0 || snapshot.Usage != ([len(snapshot.Usage)]uint64{}) {
		t.Fatalf("nested WAF/plugin charges survived client disconnect: %+v", snapshot)
	}
}

func TestWAFPluginChainRespondsBeforeUnreadHTTP2BodyAndKeepsSibling(t *testing.T) {
	var originHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHits.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "healthy")
	}))
	defer backend.Close()

	h, factory := earlyWAFPluginHandler(t, backend.URL)
	srv := httptest.NewUnstartedServer(h)
	srv.EnableHTTP2 = true
	var conns atomic.Int32
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			conns.Add(1)
		}
	}

	srv.StartTLS()
	defer srv.Close()
	client := srv.Client()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?probe=attack", pr)
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
		t.Fatal("WAF did not respond while the H2 request body was still open")
	}
	_ = pw.Close()
	if rejected.err != nil {
		t.Fatal(rejected.err)
	}
	body, err := io.ReadAll(rejected.resp.Body)
	_ = rejected.resp.Body.Close()
	if err != nil || rejected.resp.StatusCode != http.StatusForbidden || rejected.resp.ProtoMajor != 2 {
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

	sibling, err := client.Get(srv.URL + "/?probe=clean")
	if err != nil {
		t.Fatal(err)
	}
	siblingBody, err := io.ReadAll(sibling.Body)
	_ = sibling.Body.Close()
	if err != nil || sibling.StatusCode != http.StatusOK || sibling.ProtoMajor != 2 || string(siblingBody) != "healthy" {
		t.Fatalf("healthy sibling = HTTP/%d %d %q, err=%v", sibling.ProtoMajor, sibling.StatusCode, siblingBody, err)
	}
	if conns.Load() != 1 {
		t.Fatalf("early response closed the shared H2 connection: %d connections", conns.Load())
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

func TestWAFPluginChainRespondsBeforeUnreadHTTP1Body(t *testing.T) {
	var originHits atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		originHits.Add(1)
		_, _ = io.WriteString(w, "unexpected")
	}))
	defer backend.Close()
	h, factory := earlyWAFPluginHandler(t, backend.URL)
	srv := httptest.NewServer(h)
	defer srv.Close()

	pr, pw := io.Pipe()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/?probe=attack", pr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	result := make(chan struct {
		resp *http.Response
		err  error
	}, 1)
	go func() {
		resp, err := srv.Client().Do(req)
		result <- struct {
			resp *http.Response
			err  error
		}{resp: resp, err: err}
	}()
	bodyWrite := make(chan error, 1)
	go func() {
		_, err := pw.Write([]byte("partial="))
		bodyWrite <- err
	}()
	var rejected struct {
		resp *http.Response
		err  error
	}
	select {
	case rejected = <-result:
	case <-time.After(5 * time.Second):
		_ = pw.Close()
		t.Fatal("WAF did not respond while the H1 request body was still open")
	}
	_ = pw.Close()
	if rejected.err != nil {
		t.Fatal(rejected.err)
	}
	body, err := io.ReadAll(rejected.resp.Body)
	_ = rejected.resp.Body.Close()
	if err != nil || rejected.resp.StatusCode != http.StatusForbidden || rejected.resp.ProtoMajor != 1 {
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
	deadline := time.Now().Add(5 * time.Second)
	for factory.RT.Inspection.Snapshot().ActiveLeases != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	snapshot := factory.RT.Inspection.Snapshot()
	if snapshot.ActiveLeases != 0 || snapshot.Usage != ([len(snapshot.Usage)]uint64{}) {
		t.Fatalf("early-response chain did not drain: %+v", snapshot)
	}
}
