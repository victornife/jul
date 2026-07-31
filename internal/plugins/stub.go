// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build !wasmplugins

// Package plugins is compiled without the WASM plugin runtime in this build. It
// provides the same public API as the "wasm" build so the server wires plugins
// unconditionally, but Build refuses any configuration that declares plugins so
// a lean binary fails the reload with a clear message instead of silently
// ignoring them (mirroring the gRPC/ACME/compression "not compiled" pattern).
package plugins

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"jul/internal/config"
	"jul/internal/middleware"
)

// Compiled reports whether this build includes the WASM plugin runtime. It is
// false here and true in the "wasm" build.
const Compiled = false

// ABIJulV1 is the native Jul.IA ABI identifier (declared for API symmetry).
const ABIJulV1 = "jul-abi/v1"

// DialFunc matches net.Dialer.DialContext (declared for API symmetry).
type DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error)

// KVStore mirrors the compiled build's interface for API symmetry.
type KVStore interface {
	Get(key string) ([]byte, bool)
	Set(key string, value []byte)
}

// Options mirrors the compiled build's Options for API symmetry.
type Options struct {
	Logger       *slog.Logger
	OnInvocation func(plugin, result string, d time.Duration)
	OnPanic      func(plugin string)
	KV           KVStore
	// EgressWrap mirrors the compiled build's global egress guard hook.
	EgressWrap func(base DialFunc) DialFunc
}

// Manager is a no-op plugin manager in the lean build.
type Manager struct{}

// NewManager returns a no-op manager.
func NewManager(Options) (*Manager, error) { return &Manager{}, nil }

// Close is a no-op.
func (*Manager) Close() error { return nil }

// Build returns an empty Set when no plugins are configured, and an error when
// any are, since this build cannot run them.
func (*Manager) Build(ctx context.Context, cfg map[string]config.PluginConfig) (*Set, error) {
	if len(cfg) > 0 {
		return nil, errors.New("plugins are configured but this build was compiled without the \"wasmplugins\" tag")
	}
	return &Set{}, nil
}

// Set is an empty plugin set in the lean build.
type Set struct{}

// Close is a no-op.
func (*Set) Close() error { return nil }

// Has always reports false in the lean build.
func (*Set) Has(string) bool { return false }

// Middleware returns nil in the lean build (never reached: Build rejects any
// configured plugin first).
func (*Set) Middleware(string) middleware.Middleware { return nil }

// Handler returns nil in the lean build (never reached: Build rejects any
// configured plugin first).
func (*Set) Handler(string) http.Handler { return nil }
