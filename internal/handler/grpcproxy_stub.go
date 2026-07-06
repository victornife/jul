// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !grpc

package handler

import (
	"fmt"
	"log/slog"
	"net/http"

	"jul/internal/config"
	"jul/internal/upstream"
)

// NewGRPCProxy is the stub used in builds without the "grpc" tag. Native gRPC
// passthrough relies on golang.org/x/net/http2 for end-to-end HTTP/2 with
// trailers, compiled in only with -tags grpc. Returning an error here fails the
// reload with a clear message when a location sets grpc = true in a build that
// cannot serve it, instead of silently downgrading it to a plain HTTP proxy.
func NewGRPCProxy(_ config.ServerConfig, _ config.LocationConfig, _ map[string]config.UpstreamConfig, _ *upstream.Registry, _ *slog.Logger, _ func()) (http.Handler, error) {
	return nil, fmt.Errorf("grpc = true (native gRPC passthrough) requires a build with the \"grpc\" tag")
}
