// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/upstream"
)

type selectorTestRoundTripper struct {
	response *http.Response
	err      error
	request  *http.Request
}

func (t *selectorTestRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	t.request = request
	return t.response, t.err
}

func (t *selectorTestRoundTripper) Close() error {
	return nil
}

type selectorTestBody struct {
	bytes.Buffer
	closed bool
}

func (b *selectorTestBody) Close() error {
	b.closed = true
	return nil
}

func TestWebSocketUpstreamSelectorPreservesOrdinaryHTTP(t *testing.T) {
	http1 := &selectorTestRoundTripper{response: &http.Response{StatusCode: http.StatusNoContent}}
	http3 := &selectorTestRoundTripper{}
	selector := &websocketUpstreamSelector{http1: http1, http3: http3}
	request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/api", nil)

	response, err := selector.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if response != http1.response || http1.request != request || http3.request != nil {
		t.Fatalf("ordinary request used the wrong transport: http1=%v http3=%v", http1.request, http3.request)
	}
}

func TestWebSocketUpstreamSelectorAdaptsHTTP1Handshake(t *testing.T) {
	body := &selectorTestBody{}
	http3 := &selectorTestRoundTripper{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Sec-WebSocket-Protocol": {"chat"}},
		Body:       body,
	}}
	selector := &websocketUpstreamSelector{http1: &selectorTestRoundTripper{}, http3: http3}
	request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/ws", nil)
	request.Header.Set("Connection", "keep-alive, Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Protocol", "chat")

	response, err := selector.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	forwarded := http3.request
	if forwarded.Method != http.MethodConnect || forwarded.Proto != "websocket" ||
		forwarded.Body != nil || forwarded.ContentLength != 0 ||
		len(forwarded.Header.Values("Connection")) != 0 ||
		len(forwarded.Header.Values("Upgrade")) != 0 ||
		len(forwarded.Header.Values("Sec-WebSocket-Key")) != 0 {
		t.Fatalf("backend request was not converted to Extended CONNECT: %#v", forwarded)
	}
	if response.StatusCode != http.StatusSwitchingProtocols ||
		response.Header.Get("Connection") != "Upgrade" ||
		response.Header.Get("Upgrade") != "websocket" ||
		response.Header.Get("Sec-WebSocket-Accept") != websocketAccept(request.Header.Get("Sec-WebSocket-Key")) ||
		response.Body != body {
		t.Fatalf("frontend response was not adapted: %#v", response)
	}
}

func TestWebSocketUpstreamSelectorRejectsInvalidH3Handshake(t *testing.T) {
	tests := []struct {
		name         string
		key          string
		protocol     string
		duplicateKey bool
		duplexBody   bool
	}{
		{name: "unoffered protocol", key: "dGhlIHNhbXBsZSBub25jZQ==", protocol: "other", duplexBody: true},
		{name: "invalid key", key: "bad", duplexBody: true},
		{name: "duplicate key", key: "dGhlIHNhbXBsZSBub25jZQ==", duplicateKey: true, duplexBody: true},
		{name: "non-duplex response", key: "dGhlIHNhbXBsZSBub25jZQ=="},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.ReadCloser
			var duplex *selectorTestBody
			if tt.duplexBody {
				duplex = &selectorTestBody{}
				body = duplex
			} else {
				body = io.NopCloser(strings.NewReader(""))
			}
			response := &http.Response{
				StatusCode: http.StatusOK,
				Header:     make(http.Header),
				Body:       body,
			}
			if tt.protocol != "" {
				response.Header.Set("Sec-WebSocket-Protocol", tt.protocol)
			}
			http3 := &selectorTestRoundTripper{response: response}
			selector := &websocketUpstreamSelector{http1: &selectorTestRoundTripper{}, http3: http3}
			request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/ws", nil)
			request.Header.Set("Connection", "Upgrade")
			request.Header.Set("Upgrade", "websocket")
			request.Header.Set("Sec-WebSocket-Key", tt.key)
			if tt.duplicateKey {
				request.Header.Add("Sec-WebSocket-Key", tt.key)
			}
			request.Header.Set("Sec-WebSocket-Protocol", "chat")

			if _, err := selector.RoundTrip(request); err == nil {
				t.Fatal("invalid backend handshake unexpectedly succeeded")
			}
			if duplex != nil && !duplex.closed {
				t.Fatal("invalid backend response body was not closed")
			}
		})
	}
}

func TestWebSocketUpstreamSelectorPreservesBackendRejection(t *testing.T) {
	response := &http.Response{StatusCode: http.StatusForbidden, Body: http.NoBody}
	http3 := &selectorTestRoundTripper{response: response}
	selector := &websocketUpstreamSelector{http1: &selectorTestRoundTripper{}, http3: http3}
	request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")

	got, err := selector.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if got != response || got.StatusCode != http.StatusForbidden {
		t.Fatalf("backend rejection changed: %#v", got)
	}
}

func TestWebSocketUpstreamSelectorPropagatesTransportError(t *testing.T) {
	want := errors.New("backend unavailable")
	http3 := &selectorTestRoundTripper{err: want}
	selector := &websocketUpstreamSelector{http1: &selectorTestRoundTripper{}, http3: http3}
	request := httptest.NewRequest(http.MethodGet, "http://frontend.internal/ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")

	_, err := selector.RoundTrip(request)
	if !errors.Is(err, want) {
		t.Fatalf("RoundTrip error = %v, want %v", err, want)
	}
}

func TestWebSocketUpstreamUnsupportedConnectIsHealthNeutral(t *testing.T) {
	pool, err := upstream.NewPool(config.UpstreamConfig{
		Name:     "backend",
		Strategy: "round_robin",
		Servers: []config.UpstreamServer{
			{Address: "127.0.0.1:1", Weight: 1},
			{Address: "127.0.0.1:2", Weight: 1},
		},
		MaxFails: 1,
	}, "https")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	attempts, dialFailures := 0, 0
	transport := &balancingTransport{
		pool: pool,
		base: websocketRoundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errExtendedConnectUnsupported
		}),
		tlsBackend:    true,
		retryOverride: upstream.RetryOverride{Attempts: 3},
		dialFailure:   func(string) { dialFailures++ },
	}
	request := httptest.NewRequest(http.MethodGet, "https://frontend.internal/ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	_, err = transport.RoundTrip(request)
	if !errors.Is(err, errExtendedConnectUnsupported) {
		t.Fatalf("RoundTrip error = %v, want Extended CONNECT refusal", err)
	}
	if attempts != 1 || dialFailures != 0 {
		t.Fatalf("attempts=%d dial failures=%d, want one attempt and no dial failure", attempts, dialFailures)
	}
	for _, backend := range pool.Backends() {
		if !backend.Available() || backend.FailCount() != 0 || backend.Inflight() != 0 {
			t.Fatalf("backend state after refusal: available=%t failures=%d inflight=%d",
				backend.Available(), backend.FailCount(), backend.Inflight())
		}
	}
}
