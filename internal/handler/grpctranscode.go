// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package handler

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"jul/internal/config"
	"jul/internal/transcode"
	"jul/internal/upstream"
)

// NewGRPCTranscode builds a gRPC-JSON transcoding handler for a location. It
// loads the method routing table from the configured descriptor source, resolves
// the target (named upstream or direct host:port) to a pool for load-balanced
// per-request backend selection, and returns a handler that maps REST/JSON
// requests to gRPC calls. The caller closes the handler to release connections
// when the configuration is replaced. ctx bounds descriptor loading/reflection.
func NewGRPCTranscode(ctx context.Context, _ config.ServerConfig, loc config.LocationConfig, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry, log *slog.Logger, onResult func(method, code string), onStreamMsg func(method, direction string)) (http.Handler, error) {
	cfg := loc.GRPCTranscode
	if cfg == nil {
		return nil, fmt.Errorf("grpc_transcode location missing config")
	}

	pool, err := resolveGRPCTranscodePool(ctx, cfg.Target, upstreams, reg)
	if err != nil {
		return nil, err
	}

	var reflectSnap *upstream.PoolSnapshot
	if cfg.UseReflection && reg != nil {
		reflectSnap = reg.CandidateSnapshot(cfg.Target, "http")
	}

	// Resolved here, while the handler generation is prepared, so unreadable or
	// malformed trust material aborts the reload instead of failing a call. The
	// reflection fetch below uses the same policy as the transcoded calls.
	policy, err := resolveBackendTLSFor(loc, upstreams, cfg.Target)
	if err != nil {
		return nil, err
	}

	tc, err := transcode.New(ctx, *cfg, pool, reflectSnap, transcode.Options{
		Logger:      log,
		OnResult:    onResult,
		OnStreamMsg: onStreamMsg,
		BackendTLS:  policy,
		Retry:       newLocationRetry(loc),
	})
	if err != nil {
		return nil, err
	}
	// Admission wraps the transcoder rather than living inside it: a transcoded
	// call holds its slot for the whole call, and for a streaming method that is
	// the stream's lifetime, which is exactly ServeHTTP's duration.
	return newAdmittedHandler(tc, pool.Admission(), tc.Close), nil
}

// resolveGRPCTranscodePool maps a grpc_transcode target to an upstream.Pool.
// A name matching a configured upstream resolves through the registry; otherwise
// the target is treated as a single-host pool.
func resolveGRPCTranscodePool(ctx context.Context, target string, upstreams map[string]config.UpstreamConfig, reg *upstream.Registry) (*upstream.Pool, error) {
	if up, ok := upstreams[target]; ok {
		if reg != nil {
			return reg.For(ctx, up, "http")
		}
		return upstream.NewPool(up, "http")
	}
	// Concrete host:port target → ad-hoc pool of one.
	single := config.UpstreamConfig{
		Name:     target,
		Strategy: "round_robin",
		Servers:  []config.UpstreamServer{{Address: target, Weight: 1}},
		MaxFails: 3,
	}
	return upstream.NewPool(single, "http")
}
