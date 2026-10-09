// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/respwriter"
	"jul/internal/server"
	"jul/internal/upstream"
)

type longLivedServer struct {
	address string
	grace   time.Duration
	retired chan struct{}
	reloads chan server.ReloadRequest
	stop    func()
}

func newLongLivedServer(t *testing.T, handler http.Handler, grace ...time.Duration) *longLivedServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cfg := config.ProxyTarget("http://127.0.0.1:9001", address)
	cfg.Global.ShutdownTimeout = config.Duration(100 * time.Millisecond)
	if len(grace) != 0 {
		cfg.Global.ShutdownTimeout = config.Duration(grace[0])
	}
	cfg.Servers[0].H2C = true
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &longLivedServer{address: address, grace: cfg.Global.ShutdownTimeout.Std(), retired: make(chan struct{}), reloads: make(chan server.ReloadRequest)}
	var generation uint64
	var retireOnce sync.Once
	factory := func(context.Context, *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		generation++
		var retire func()
		if generation > 1 {
			retire = func() {
				retireOnce.Do(func() {
					if closer, ok := handler.(io.Closer); ok {
						if err := closer.Close(); err != nil {
							t.Errorf("retire handler: %v", err)
						}
					}
					close(fixture.retired)
				})
			}
		}
		return map[string]http.Handler{address: handler}, generation, func() (upstream.SnapshotMap, func()) { return nil, retire }, func() {}, nil
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := server.New(candidate.Effective, candidate.Raw, lifecycle.ComputeFingerprint(candidate.Effective), log, factory, config.NewTOMLSource(""), func(context.Context, *config.Config) error { return nil })
	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx, fixture.reloads, candidate.Redaction) }()
	var stopOnce sync.Once
	fixture.stop = func() {
		stopOnce.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("server shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("server shutdown did not return")
			}
		})
	}
	t.Cleanup(fixture.stop)
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("server start: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("server did not become ready")
	}
	return fixture
}

func (fixture *longLivedServer) reload(t *testing.T) {
	t.Helper()
	cfg := config.ProxyTarget("http://127.0.0.1:9001", fixture.address)
	cfg.Global.ShutdownTimeout = config.Duration(fixture.grace)
	cfg.Global.LogLevel = "debug"
	cfg.Servers[0].H2C = true
	candidate, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan server.ReloadResult, 1)
	fixture.reloads <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: candidate, Result: result}
	select {
	case reload := <-result:
		if reload.Outcome != server.ReloadAppliedLive {
			t.Fatalf("reload=%+v", reload)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("reload did not finish")
	}
}

func (fixture *longLivedServer) waitRetired(t *testing.T) {
	t.Helper()
	select {
	case <-fixture.retired:
	case <-time.After(10 * time.Second):
		t.Fatal("old generation resources did not retire")
	}
}

func TestLongLivedWebSocketReloadAndShutdown(t *testing.T) {
	for _, phase := range []string{"reload_and_forced_retirement", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			backend := httptest.NewServer(websocket.Handler(func(connection *websocket.Conn) {
				for {
					var message []byte
					if err := websocket.Message.Receive(connection, &message); err != nil {
						return
					}
					if err := websocket.Message.Send(connection, message); err != nil {
						return
					}
				}
			}))
			t.Cleanup(backend.Close)
			fixture := newLongLivedServer(t, newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
			connection, err := websocket.Dial("ws://"+fixture.address+"/", "", "http://"+fixture.address)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = connection.Close() })
			echo := func(value string) {
				t.Helper()
				if err := connection.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				if err := websocket.Message.Send(connection, []byte(value)); err != nil {
					t.Fatal(err)
				}
				var received []byte
				if err := websocket.Message.Receive(connection, &received); err != nil {
					t.Fatal(err)
				}
				if string(received) != value {
					t.Fatalf("echo=%q, want %q", received, value)
				}
			}
			echo("before")
			if phase == "shutdown" {
				fixture.stop()
				echo("hijacked tunnel still alive after server drain returns")
			} else {
				fixture.reload(t)
				echo("after reload")
				fixture.waitRetired(t)
				echo("after forced retirement")
			}
		})
	}
}

func TestLongLivedSSEReloadAndShutdown(t *testing.T) {
	for _, phase := range []string{"reload_and_forced_retirement", "shutdown"} {
		t.Run(phase, func(t *testing.T) {
			events := make(chan string, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "text/event-stream")
				writer.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(writer, "data: before\n\n")
				_ = http.NewResponseController(writer).Flush()
				for {
					select {
					case <-request.Context().Done():
						return
					case event := <-events:
						if _, err := fmt.Fprintf(writer, "data: %s\n\n", event); err != nil {
							return
						}
						if err := http.NewResponseController(writer).Flush(); err != nil {
							return
						}
					}
				}
			}))
			t.Cleanup(backend.Close)
			fixture := newLongLivedServer(t, newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
			client := &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}
			response, err := client.Get("http://" + fixture.address + "/events")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = response.Body.Close() })
			reader := bufio.NewReader(response.Body)
			if event := readSSEDataWithin(t, reader, 5*time.Second); event != "before" {
				t.Fatalf("first event=%q", event)
			}
			if phase == "shutdown" {
				fixture.stop()
				if _, err := reader.ReadString('\n'); err != nil {
					t.Fatalf("event separator: %v", err)
				}
				if _, err := reader.ReadString('\n'); err == nil {
					t.Fatal("SSE response still open after shutdown grace")
				}
			} else {
				fixture.reload(t)
				events <- "after reload"
				if event := readSSEDataWithin(t, reader, 5*time.Second); event != "after reload" {
					t.Fatalf("reload event=%q", event)
				}
				fixture.waitRetired(t)
				events <- "after forced retirement"
				if event := readSSEDataWithin(t, reader, 5*time.Second); event != "after forced retirement" {
					t.Fatalf("retirement event=%q", event)
				}
			}
		})
	}
}

// TestProxyWebSocketPassthrough is the WebSocket conformance test: it proves an
// end-to-end Upgrade (RFC 6455 handshake + framed text and binary messages)
// survives the reverse proxy unchanged. This is the path Apollo GraphQL
// subscriptions (graphql-ws) and Socket.IO/engine.io rely on.
func TestProxyWebSocketPassthrough(t *testing.T) {
	// Backend: a real WebSocket server that echoes every message back.
	backend := httptest.NewServer(websocket.Handler(func(ws *websocket.Conn) {
		for {
			var msg []byte
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			if err := websocket.Message.Send(ws, msg); err != nil {
				return
			}
		}
	}))
	defer backend.Close()

	// Front: the bare reverse-proxy handler served over a real (hijackable)
	// HTTP server, so the 101 Switching Protocols upgrade is spliced for real.
	proxy := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
	front := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		bounded := respwriter.WithSendTimeout(writer, 100*time.Millisecond, time.Time{})
		proxy.ServeHTTP(bounded, request)
		_ = http.NewResponseController(bounded).Flush()
	}))
	defer front.Close()

	wsURL := "ws" + strings.TrimPrefix(front.URL, "http")
	ws, err := websocket.Dial(wsURL, "", front.URL)
	if err != nil {
		t.Fatalf("WebSocket dial through proxy: %v", err)
	}
	defer ws.Close()

	// Text frames (graphql-ws / Socket.IO control frames are UTF-8 JSON).
	for _, want := range []string{"hello", `{"type":"connection_init"}`, "subscription-data"} {
		if err := websocket.Message.Send(ws, []byte(want)); err != nil {
			t.Fatalf("send %q: %v", want, err)
		}
		var got []byte
		if err := websocket.Message.Receive(ws, &got); err != nil {
			t.Fatalf("receive after %q: %v", want, err)
		}
		if string(got) != want {
			t.Fatalf("echo = %q, want %q", got, want)
		}
	}

	// Binary frame (engine.io/Socket.IO upgrade probes and binary payloads).
	binPayload := []byte{0x00, 0x01, 0x02, 0xfe, 0xff}
	if err := websocket.Message.Send(ws, binPayload); err != nil {
		t.Fatalf("send binary: %v", err)
	}
	var gotBin []byte
	if err := websocket.Message.Receive(ws, &gotBin); err != nil {
		t.Fatalf("receive binary: %v", err)
	}
	if !bytes.Equal(gotBin, binPayload) {
		t.Fatalf("binary echo = %v, want %v", gotBin, binPayload)
	}
}

// TestProxyServerSentEventsStreaming is the SSE conformance test: it proves the
// proxy streams a text/event-stream response incrementally (flushing each
// event) instead of buffering the whole body. The backend blocks before the
// second event until the client has received the first, so a buffering proxy
// would dead-lock and the test would time out. This is the path Node/Python
// apps use for Server-Sent Events.
func TestProxyServerSentEventsStreaming(t *testing.T) {
	releaseSecond := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			t.Error("backend ResponseWriter is not an http.Flusher")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "data: first\n\n")
		fl.Flush()
		<-releaseSecond // hold the second event until the client has the first
		_, _ = io.WriteString(w, "data: second\n\n")
		fl.Flush()
	}))
	defer backend.Close()

	front := httptest.NewServer(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
	defer front.Close()

	req, _ := http.NewRequest(http.MethodGet, front.URL+"/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /events: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	br := bufio.NewReader(resp.Body)

	// Read the first event with a timeout guard: if the proxy buffered the
	// response, this read blocks forever because the backend is waiting on
	// releaseSecond, which we only close after the first event arrives.
	first := readSSEDataWithin(t, br, 5*time.Second)
	if first != "first" {
		t.Fatalf("first event = %q, want first", first)
	}

	// Got event 1 before the backend wrote event 2 => genuinely streamed.
	close(releaseSecond)

	second := readSSEDataWithin(t, br, 5*time.Second)
	if second != "second" {
		t.Fatalf("second event = %q, want second", second)
	}
}

// readSSEDataWithin reads one `data:` line from an SSE stream, failing the test
// if nothing arrives within d (the signal that the proxy buffered the body).
func readSSEDataWithin(t *testing.T, br *bufio.Reader, d time.Duration) string {
	t.Helper()
	type result struct {
		data string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				ch <- result{err: err}
				return
			}
			if strings.HasPrefix(line, "data:") {
				ch <- result{data: strings.TrimSpace(strings.TrimPrefix(line, "data:"))}
				return
			}
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("reading SSE event: %v", r.err)
		}
		return r.data
	case <-time.After(d):
		t.Fatalf("timed out after %s waiting for an SSE event (proxy buffered the response instead of streaming)", d)
		return ""
	}
}
