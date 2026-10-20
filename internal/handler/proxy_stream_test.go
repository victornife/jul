// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
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
				if err := connection.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var received []byte
				if err := websocket.Message.Receive(connection, &received); err == nil {
					t.Fatalf("expected owned WebSocket to be closed by server shutdown drain (#562), got %q", string(received))
				}
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

func TestAudit653BackendInitiatedCloseIsForwardedBothWays(t *testing.T) {
	reply := make(chan error, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, b, err := http.NewResponseController(w).Hijack()
		if err != nil {
			reply <- err
			return
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
		_, _ = fmt.Fprintf(b, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
		_ = b.Flush()
		_, err = c.Write([]byte{0x88, 2, 3, 0xe8})
		if err != nil {
			reply <- err
			return
		}
		var frame [8]byte
		_, err = io.ReadFull(b, frame[:])
		reply <- err
	}))
	defer backend.Close()
	fixture := newLongLivedServer(t, newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
	c, err := net.Dial("tcp", fixture.address)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	_, _ = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	var frame [4]byte
	if _, err := io.ReadFull(br, frame[:]); err != nil {
		t.Fatal(err)
	}
	if frame[0] != 0x88 {
		t.Fatalf("not Close: %x", frame)
	}
	// Valid masked reciprocal Close frame with code 1000.
	_, err = c.Write([]byte{0x88, 0x82, 1, 2, 3, 4, 3 ^ 1, 0xe8 ^ 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-reply; err != nil {
		t.Fatalf("backend never receives reciprocal client Close: %v", err)
	}
}

func TestRealProxyWebSocketRetirementHandshakeAndForcedFallback(t *testing.T) {
	t.Run("real proxy WebSocket narrowing retirement sends unmasked 1008 to client and masked 1008 to backend", func(t *testing.T) {
		backendGotMaskedClose := make(chan []byte, 1)
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, b, err := http.NewResponseController(w).Hijack()
			if err != nil {
				return
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			_, _ = fmt.Fprintf(b, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
			_ = b.Flush()
			var frame [8]byte
			if _, err := io.ReadFull(b, frame[:]); err == nil {
				backendGotMaskedClose <- frame[:]
			}
		}))
		defer backend.Close()

		reg := respwriter.NewExchangeRegistry()
		reg.Grace = 300 * time.Millisecond
		reg.Bound = 600 * time.Millisecond
		reg.SetInitialGeneration(1)

		proxyH := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
		wrappedH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := respwriter.WithExchangeRegistry(r.Context(), reg, 1)
			r = r.WithContext(ctx)
			wAdmitted, rAdmitted, _, release := reg.Admit(w, r, respwriter.EntryCoord{
				GenID:      1,
				ListenAddr: "127.0.0.1:8080",
				RouteID:    "ws-real",
			})
			defer release()
			proxyH.ServeHTTP(wAdmitted, rAdmitted)
		})
		ts := httptest.NewServer(wrappedH)
		defer ts.Close()

		c, err := net.Dial("tcp", ts.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		_, _ = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
		br := bufio.NewReader(c)
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			if line == "\r\n" {
				break
			}
		}

		// Client reads Jul's unmasked 1008 Close frame and replies with a masked 1008 Close frame.
		clientGotClose := make(chan uint16, 1)
		go func() {
			var frame [4]byte
			if _, rErr := io.ReadFull(br, frame[:]); rErr == nil && frame[0] == 0x88 && frame[1] == 0x02 {
				code := binary.BigEndian.Uint16(frame[2:4])
				clientGotClose <- code
				// Reply with masked 1008 (0x03f0).
				_, _ = c.Write([]byte{0x88, 0x82, 1, 2, 3, 4, 0x03 ^ 1, 0xf0 ^ 2})
			}
		}()

		results := reg.PublishGeneration(1, 2, []respwriter.EntryTransition{{
			FromRouteID: "ws-real",
			ToRouteID:   "ws-real",
			Retire:      true,
			Mandatory:   true,
		}})()

		if len(results) != 1 || results[0].GracefulCount != 1 || results[0].ForcedCount != 0 || !results[0].CompletedWithinBound {
			t.Fatalf("expected graceful_count=1 forced_count=0 on real proxy WebSocket retirement, got %+v", results)
		}
		if code := <-clientGotClose; code != respwriter.WSClosePolicyViolation {
			t.Fatalf("client got Close code %d, want %d", code, respwriter.WSClosePolicyViolation)
		}
		upFrame := <-backendGotMaskedClose
		if upFrame[0] != 0x88 || (upFrame[1]&0x80) == 0 {
			t.Fatalf("expected masked Close frame on real backend leg, got %x", upFrame)
		}
	})
}

func TestFifth653RealProxyRejectsOneByteCloseAsGraceful(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"one-byte-body", []byte{0x88, 0x81, 1, 2, 3, 4, 0x03 ^ 1}},
		{"reserved-rsv2", []byte{0xa8, 0x82, 0, 0, 0, 0, 0x03, 0xf0}},
		{"nonminimal-length-126", []byte{0x88, 0xfe, 0, 2, 0, 0, 0, 0, 0x03, 0xf0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Error("backend ResponseWriter not Hijacker")
					return
				}
				c, b, err := hj.Hijack()
				if err != nil {
					t.Errorf("backend Hijack: %v", err)
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				_, _ = fmt.Fprintf(b, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
				_ = b.Flush()
				_, _ = io.Copy(io.Discard, b)
			}))
			defer backend.Close()

			reg := respwriter.NewExchangeRegistry()
			reg.Grace = 120 * time.Millisecond
			reg.Bound = 240 * time.Millisecond
			reg.SetInitialGeneration(1)

			proxyH := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
			wrappedH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx := respwriter.WithExchangeRegistry(r.Context(), reg, 1)
				r = r.WithContext(ctx)
				wAdmitted, rAdmitted, _, release := reg.Admit(w, r, respwriter.EntryCoord{
					GenID:      1,
					ListenAddr: "127.0.0.1:8080",
					RouteID:    "ws-real",
				})
				defer release()
				proxyH.ServeHTTP(wAdmitted, rAdmitted)
			})
			ts := httptest.NewServer(wrappedH)
			defer ts.Close()

			c, err := net.Dial("tcp", ts.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = fmt.Fprintf(c, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
			br := bufio.NewReader(c)
			for {
				line, rErr := br.ReadString('\n')
				if rErr != nil {
					t.Fatal(rErr)
				}
				if line == "\r\n" {
					break
				}
			}

			go func() {
				var frame [4]byte
				if _, rErr := io.ReadFull(br, frame[:]); rErr == nil && frame[0] == 0x88 && frame[1] == 0x02 {
					_, _ = c.Write(tc.frame)
				}
			}()

			results := reg.PublishGeneration(1, 2, []respwriter.EntryTransition{{
				FromRouteID: "ws-real",
				ToRouteID:   "ws-real",
				Retire:      true,
				Mandatory:   true,
			}})()

			if len(results) != 1 || results[0].GracefulCount != 0 || results[0].ForcedCount != 1 {
				t.Fatalf("%s: expected graceful_count=0 forced_count=1, got %+v", tc.name, results)
			}
		})
	}
}

func TestR7BackendCloseMaskingValidityOnRealProxy(t *testing.T) {
	for _, masked := range []bool{false, true} {
		name := "valid-unmasked-backend-close"
		if masked {
			name = "invalid-masked-backend-close"
		}
		t.Run(name, func(t *testing.T) {
			frame := []byte{0x88, 2, 3, 0xe8}
			if masked {
				frame = []byte{0x88, 0x82, 1, 2, 3, 4, 3 ^ 1, 0xe8 ^ 2}
			}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				c, b, e := http.NewResponseController(w).Hijack()
				if e != nil {
					return
				}
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				_, _ = fmt.Fprintf(b, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
				_ = b.Flush()
				_, _ = c.Write(frame)
				_, _ = io.Copy(io.Discard, b)
			}))
			defer backend.Close()

			reg := respwriter.NewExchangeRegistry()
			reg.Grace = 120 * time.Millisecond
			reg.Bound = 240 * time.Millisecond
			reg.SetInitialGeneration(1)

			proxy := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				r = r.WithContext(respwriter.WithExchangeRegistry(r.Context(), reg, 1))
				w, r, _, release := reg.Admit(w, r, respwriter.EntryCoord{GenID: 1, RouteID: "ws"})
				defer release()
				proxy.ServeHTTP(w, r)
			}))
			defer ts.Close()

			c, e := net.Dial("tcp", ts.Listener.Addr().String())
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")
			br := bufio.NewReader(c)
			for {
				l, rErr := br.ReadString('\n')
				if rErr != nil {
					t.Fatal(rErr)
				}
				if l == "\r\n" {
					break
				}
			}
			received := make([]byte, len(frame))
			if _, rErr := io.ReadFull(br, received); rErr != nil {
				t.Fatal(rErr)
			}
			wait := reg.PublishGeneration(1, 2, []respwriter.EntryTransition{{FromRouteID: "ws", ToRouteID: "ws", Retire: true, Mandatory: true}})
			code := byte(0xe8)
			if masked {
				code = 0xea
			}
			_, _ = c.Write([]byte{0x88, 0x82, 5, 6, 7, 8, 3 ^ 5, code ^ 6})
			if masked {
				_ = c.Close()
			}
			results := wait()
			if len(results) != 1 {
				t.Fatalf("results=%+v", results)
			}
			if masked && (results[0].GracefulCount != 0 || results[0].ForcedCount != 1) {
				t.Fatalf("invalid masked server Close was accepted as graceful: %+v", results)
			}
			if !masked && (results[0].GracefulCount != 1 || results[0].ForcedCount != 0) {
				t.Fatalf("valid unmasked server Close control not graceful: %+v", results)
			}
		})
	}
}
