// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !http3

package handler

import (
	"context"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestWebSocketHTTP3UpstreamRequiresBuildTag(t *testing.T) {
	_, err := NewProxy(context.Background(), config.ServerConfig{}, config.LocationConfig{
		ProxyPass: "https://inventory",
	}, map[string]config.UpstreamConfig{
		"inventory": {
			Name: "inventory", WebSocketUpstreamProtocol: "http3",
			Servers: []config.UpstreamServer{{Address: "127.0.0.1:443", Weight: 1}},
		},
	}, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "requires rebuilding with -tags http3") {
		t.Fatalf("NewProxy error = %v, want explicit http3 build-tag requirement", err)
	}
}
