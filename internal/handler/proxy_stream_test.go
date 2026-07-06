// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bufio"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"jul/internal/config"
)

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
	front := httptest.NewServer(newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil))
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
