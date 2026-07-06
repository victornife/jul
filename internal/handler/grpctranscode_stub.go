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

// NewGRPCTranscode is the stub used in builds without the "grpc" tag. gRPC-JSON
// transcoding pulls in the protobuf and gRPC runtimes, which are compiled in
// only with -tags grpc. Returning an error here fails the reload with a clear
// message when a location configures grpc_transcode in a build that cannot serve
// it, instead of silently ignoring the action.
func NewGRPCTranscode(_ config.ServerConfig, _ config.LocationConfig, _ map[string]config.UpstreamConfig, _ *upstream.Registry, _ *slog.Logger, _ func(method, code string), _ func(method, direction string)) (http.Handler, error) {
	return nil, fmt.Errorf("grpc_transcode requires a build with the \"grpc\" tag")
}
