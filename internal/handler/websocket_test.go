// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
	"time"

	"jul/internal/websocketx"
)

type websocketRoundTripFunc func(*http.Request) (*http.Response, error)

func (f websocketRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestExtendedWebSocketUpstreamHandshakeIsH1AndRejectsBadAccept(t *testing.T) {
	var upstreamRequest *http.Request
	h := &proxyHandler{
		ReverseProxy: &httputil.ReverseProxy{
			Transport: websocketRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				upstreamRequest = r.Clone(context.Background())
				return &http.Response{
					StatusCode: http.StatusSwitchingProtocols,
					Header: http.Header{
						"Connection":           {"Upgrade"},
						"Upgrade":              {"websocket"},
						"Sec-WebSocket-Accept": {"not-the-computed-accept"},
					},
					Body: io.NopCloser(strings.NewReader("")),
				}, nil
			}),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "upstream handshake failed", http.StatusBadGateway)
			},
		},
	}

	wireRequest := &http.Request{
		Method:     http.MethodConnect,
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		Header:     make(http.Header),
		URL:        &url.URL{Path: "/ws"},
		Host:       "example.test",
	}
	wireRequest.Header[":protocol"] = []string{"websocket"}
	wireRequest.Header.Set("Sec-WebSocket-Version", "13")
	wireRequest.Header.Set("Sec-WebSocket-Protocol", "chat")
	if !websocketx.ValidExtendedRequest(wireRequest) {
		t.Fatal("test request is not a valid Extended CONNECT")
	}
	request := websocketx.AsApplicationRequest(wireRequest)
	recorder := httptest.NewRecorder()
	h.serveExtendedWebSocket(recorder, request)

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502", recorder.Code)
	}
	if upstreamRequest == nil {
		t.Fatal("upstream request was not sent")
	}
	if upstreamRequest.Method != http.MethodGet {
		t.Fatalf("upstream method=%q, want GET", upstreamRequest.Method)
	}
	if upstreamRequest.Header.Get("Connection") != "Upgrade" ||
		upstreamRequest.Header.Get("Upgrade") != "websocket" ||
		upstreamRequest.Header.Get("Sec-WebSocket-Version") != "13" {
		t.Fatalf("upstream handshake headers=%v", upstreamRequest.Header)
	}
	key, err := base64.StdEncoding.DecodeString(upstreamRequest.Header.Get("Sec-WebSocket-Key"))
	if err != nil || len(key) != 16 {
		t.Fatalf("generated upstream WebSocket key=%q, decoded bytes=%d, err=%v",
			upstreamRequest.Header.Get("Sec-WebSocket-Key"), len(key), err)
	}
	if len(upstreamRequest.Header[":protocol"]) != 0 {
		t.Fatal("HTTP/2 pseudo-header leaked to the HTTP/1.1 backend")
	}
}

func TestExtendedWebSocketWaitsForBackendAcceptance(t *testing.T) {
	h := &proxyHandler{
		ReverseProxy: &httputil.ReverseProxy{
			Transport: websocketRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader("denied")),
				}, nil
			}),
			Rewrite: func(pr *httputil.ProxyRequest) {
				pr.SetURL(&url.URL{Scheme: "http", Host: "backend"})
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "upstream handshake failed", http.StatusBadGateway)
			},
		},
	}
	request := httptest.NewRequest(http.MethodConnect, "https://example.test/ws", nil)
	request.Proto, request.ProtoMajor = "HTTP/2.0", 2
	request.Header[":protocol"] = []string{"websocket"}
	request.Header.Set("Sec-WebSocket-Version", "13")
	request = websocketx.AsApplicationRequest(request)

	recorder := httptest.NewRecorder()
	h.serveExtendedWebSocket(recorder, request)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status=%d, want 502 before Extended CONNECT success", recorder.Code)
	}
}

func TestWebSocketNegotiationCannotAddUnrequestedProtocolOrExtension(t *testing.T) {
	request := make(http.Header)
	request.Set("Sec-WebSocket-Protocol", "chat, superchat")
	selected := make(http.Header)
	selected.Set("Sec-WebSocket-Protocol", "chat")
	if !validSelectedSubprotocol(request, selected) {
		t.Fatal("offered subprotocol should be accepted")
	}

	selected.Set("Sec-WebSocket-Protocol", "admin")
	if validSelectedSubprotocol(request, selected) {
		t.Fatal("unoffered subprotocol must be rejected")
	}
	selected = make(http.Header)
	selected.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	if validSelectedExtensions(request, selected) {
		t.Fatal("unoffered extension must be rejected")
	}
	request.Set("Sec-WebSocket-Extensions", "permessage-deflate")
	if !validSelectedExtensions(request, selected) {
		t.Fatal("offered extension should be accepted")
	}
	selected.Set("Sec-WebSocket-Extensions", "x-unoffered")
	if validSelectedExtensions(request, selected) {
		t.Fatal("extension name not offered by the client must be rejected")
	}
	request.Set("Sec-WebSocket-Extensions", `permessage-deflate; note="x,y"`)
	selected.Set("Sec-WebSocket-Extensions", `permessage-deflate; server_no_context_takeover`)
	if !validSelectedExtensions(request, selected) {
		t.Fatal("offered extension with negotiated parameters should be accepted")
	}
	selected.Set("Sec-WebSocket-Extensions", `permessage-deflate; note="unterminated`)
	if validSelectedExtensions(request, selected) {
		t.Fatal("malformed selected extension must be rejected")
	}
}

func TestRelayExtendedWebSocketStopsWhenBackendCloses(t *testing.T) {
	client, front := net.Pipe()
	defer client.Close()
	backendPeer, backend := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- relayExtendedWebSocket(front, backend) }()

	if err := backendPeer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var one [1]byte
	if _, err := client.Read(one[:]); err == nil {
		t.Fatal("frontend remained open after the backend closed")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("relay after backend close: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("relay did not stop after the backend closed")
	}
}
