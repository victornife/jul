// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"
	"jul/internal/websocketx"
)

var errExtendedConnectUnsupported = errors.New("backend did not advertise WebSocket Extended CONNECT")

type websocketUpstreamTransport interface {
	RoundTrip(*http.Request) (*http.Response, error)
	Close() error
}

type websocketUpstreamSelector struct {
	http1 http.RoundTripper
	http3 websocketUpstreamTransport
}

func (t *websocketUpstreamSelector) RoundTrip(req *http.Request) (*http.Response, error) {
	if !websocketx.IsExtendedRequest(req) && !isHTTP1WebSocketUpgrade(req) {
		return t.http1.RoundTrip(req)
	}

	out := req
	http1Upgrade := isHTTP1WebSocketUpgrade(req)
	if http1Upgrade {
		out = req.Clone(req.Context())
		out.Header = req.Header.Clone()
		out.Method = http.MethodConnect
		out.Proto = "websocket"
		out.Body = nil
		out.GetBody = nil
		out.ContentLength = 0
		out.RequestURI = ""
		removeWebSocketHopHeaders(out.Header)
		out.Header.Del("Sec-WebSocket-Key")
		out.Header.Del("Sec-WebSocket-Accept")
	}
	response, err := t.http3.RoundTrip(out)
	if err != nil || !http1Upgrade {
		return response, err
	}
	if response.StatusCode != http.StatusOK {
		return response, nil
	}
	if len(response.Header.Values("Connection")) > 0 || len(response.Header.Values("Upgrade")) > 0 ||
		!validSelectedSubprotocol(req.Header, response.Header) ||
		!validSelectedExtensions(req.Header, response.Header) {
		_ = response.Body.Close()
		return nil, errors.New("backend returned an invalid WebSocket Extended CONNECT response")
	}
	keys := req.Header.Values("Sec-WebSocket-Key")
	if len(keys) != 1 {
		_ = response.Body.Close()
		return nil, errors.New("client sent an invalid WebSocket key")
	}
	key := strings.TrimSpace(keys[0])
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 16 {
		_ = response.Body.Close()
		return nil, errors.New("client sent an invalid WebSocket key")
	}
	response.StatusCode = http.StatusSwitchingProtocols
	response.Status = "101 Switching Protocols"
	response.Proto = "HTTP/1.1"
	response.ProtoMajor = 1
	response.ProtoMinor = 1
	response.Header.Set("Connection", "Upgrade")
	response.Header.Set("Upgrade", "websocket")
	response.Header.Set("Sec-WebSocket-Accept", websocketAccept(key))
	response.Request = req
	if _, ok := response.Body.(io.ReadWriteCloser); !ok {
		_ = response.Body.Close()
		return nil, fmt.Errorf("HTTP/3 WebSocket response stream is not duplex")
	}
	return response, nil
}

func newWebSocketUpstreamTransport(loc config.LocationConfig, policy *backendtls.Policy, pool *upstream.Pool) (websocketUpstreamTransport, error) {
	if strings.TrimSpace(loc.WebSocketUpstreamProtocol) != "http3" {
		return nil, nil
	}
	return newHTTP3WebSocketUpstreamTransport(loc, policy, pool)
}

func isHTTP1WebSocketUpgrade(req *http.Request) bool {
	if req == nil || req.Method != http.MethodGet || req.ProtoMajor != 1 {
		return false
	}
	upgrade := req.Header.Values("Upgrade")
	if len(upgrade) != 1 || !strings.EqualFold(strings.TrimSpace(upgrade[0]), "websocket") {
		return false
	}
	for _, value := range req.Header.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(token), "upgrade") {
				return true
			}
		}
	}
	return false
}

func isExtendedWebSocketUpstreamRequest(req *http.Request) bool {
	return req != nil && req.Method == http.MethodConnect && strings.EqualFold(req.Proto, "websocket")
}
