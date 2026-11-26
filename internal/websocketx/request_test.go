// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package websocketx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidExtendedWebSocketRequestUsesGETApplicationMethod(t *testing.T) {
	for _, tc := range []struct {
		name       string
		proto      string
		protoMajor int
		header     http.Header
	}{
		{
			name: "h2", proto: "HTTP/2.0", protoMajor: 2,
			header: http.Header{":protocol": {"websocket"}, "Sec-Websocket-Version": {"13"}},
		},
		{
			name: "h3", proto: "websocket", protoMajor: 3,
			header: http.Header{"Sec-Websocket-Version": {"13"}},
		},
		{
			name: "h2 protocol token is case-insensitive", proto: "HTTP/2.0", protoMajor: 2,
			header: http.Header{":protocol": {"WebSocket"}, "Sec-Websocket-Version": {"13"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodConnect, "https://app.example/ws", nil)
			r.Proto, r.ProtoMajor, r.Header = tc.proto, tc.protoMajor, tc.header
			if !ValidExtendedRequest(r) {
				t.Fatal("valid WebSocket Extended CONNECT was rejected")
			}
			appReq := AsApplicationRequest(r)
			if appReq.Method != http.MethodGet || r.Method != http.MethodConnect || !IsExtendedRequest(appReq) {
				t.Fatalf("wire=%q application=%q extended=%t", r.Method, appReq.Method, IsExtendedRequest(appReq))
			}
			if IsExtendedRequest(r) {
				t.Fatal("classification mutated the original wire request")
			}
		})
	}
}

func TestExtendedConnectRejectsNonWebSocketAndMalformedHandshakes(t *testing.T) {
	base := func() *http.Request {
		r := httptest.NewRequest(http.MethodConnect, "https://app.example/ws", nil)
		r.Proto, r.ProtoMajor = "HTTP/2.0", 2
		r.Header.Set(":protocol", "websocket")
		r.Header.Set("Sec-WebSocket-Version", "13")
		return r
	}
	for _, tc := range []struct {
		name string
		edit func(*http.Request)
	}{
		{"plain connect", func(r *http.Request) { r.Header.Del(":protocol") }},
		{"unsupported protocol", func(r *http.Request) { r.Header.Set(":protocol", "webtransport") }},
		{"whitespace in protocol", func(r *http.Request) { r.Header.Set(":protocol", " websocket ") }},
		{"duplicate protocol", func(r *http.Request) { r.Header.Add(":protocol", "websocket") }},
		{"not CONNECT", func(r *http.Request) { r.Method = http.MethodGet }},
		{"HTTP/1 request", func(r *http.Request) { r.ProtoMajor = 1 }},
		{"ordinary HTTP/3 CONNECT", func(r *http.Request) { r.Proto, r.ProtoMajor = "HTTP/3.0", 3 }},
		{"missing URL", func(r *http.Request) { r.URL = nil }},
		{"missing path", func(r *http.Request) { r.URL.Path = "" }},
		{"missing authority", func(r *http.Request) { r.Host = "" }},
		{"missing version", func(r *http.Request) { r.Header.Del("Sec-WebSocket-Version") }},
		{"duplicate version", func(r *http.Request) { r.Header.Add("Sec-WebSocket-Version", "13") }},
		{"h1 connection header", func(r *http.Request) { r.Header.Set("Connection", "Upgrade") }},
		{"h1 websocket key", func(r *http.Request) { r.Header.Set("Sec-WebSocket-Key", "client-controlled") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base()
			tc.edit(r)
			if ValidExtendedRequest(r) {
				t.Fatal("invalid Extended CONNECT was accepted")
			}
		})
	}
	if ValidExtendedRequest(nil) || IsExtendedRequest(nil) {
		t.Fatal("nil request was classified as WebSocket Extended CONNECT")
	}
}
