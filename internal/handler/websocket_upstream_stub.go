// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !http3

package handler

import (
	"fmt"

	"jul/internal/backendtls"
	"jul/internal/config"
	"jul/internal/upstream"
)

func newHTTP3WebSocketUpstreamTransport(config.LocationConfig, *backendtls.Policy, *upstream.Pool) (websocketUpstreamTransport, error) {
	return nil, fmt.Errorf("websocket_upstream_protocol = %q requires rebuilding with -tags http3", "http3")
}
