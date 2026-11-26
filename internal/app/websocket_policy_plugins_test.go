// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf && http3 && wasmplugins

package app

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
	"jul/internal/plugins/wasmtest"
)

func TestWebSocketWASMHandshake(t *testing.T) {
	tests := []struct {
		name  string
		guest []byte
		op    string
		cases []webSocketPolicyCase
	}{
		{
			name:  "request-body",
			guest: wasmtest.BodyReaderV2(0),
			cases: []webSocketPolicyCase{{probe: "clean", status: http.StatusOK, backend: true}},
		},
		{
			name: "response-body",
			op:   "echo",
			cases: []webSocketPolicyCase{{
				probe: "clean", status: http.StatusOK, backend: true,
				headers:         http.Header{"X-Mode": {"body"}},
				responseHeaders: http.Header{"X-Body-State": {"7"}, "X-Body-Rc": {"-4"}},
			}},
		},
		{
			name:  "request-denial",
			guest: wasmtest.BodyReaderV2(http.StatusForbidden),
			cases: []webSocketPolicyCase{{probe: "clean", status: http.StatusForbidden}},
		},
		{
			name:  "request-success",
			guest: wasmtest.BodyReaderV2(http.StatusOK),
			cases: []webSocketPolicyCase{{probe: "clean", status: http.StatusInternalServerError}},
		},
		{
			name:  "request-informational",
			guest: wasmtest.BodyReaderV2(http.StatusEarlyHints),
			cases: []webSocketPolicyCase{{probe: "clean", status: http.StatusInternalServerError}},
		},
		{
			name: "response-denial",
			op:   "reject",
			cases: []webSocketPolicyCase{{
				probe: "clean", status: http.StatusForbidden, backend: true,
				headers: http.Header{"X-Mode": {"body"}, "X-Status-To": {"403"}},
			}},
		},
		{
			name: "response-false-success",
			op:   "set-status",
			cases: []webSocketPolicyCase{{
				probe: "backend-deny", status: http.StatusInternalServerError, backend: true,
				headers: http.Header{"X-Status-To": {"200"}},
			}},
		},
		{
			name: "response-alternate-success",
			op:   "set-status",
			cases: []webSocketPolicyCase{{
				probe: "clean", status: http.StatusInternalServerError, backend: true,
				headers: http.Header{"X-Mode": {"body"}, "X-Status-To": {"201"}},
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := v2Plugin(tc.op)
			p.MaxRequestBody = config.Size(1024)
			p.MaxResponseBody = config.Size(1024)
			if tc.guest != nil {
				p.Path = filepath.Join(t.TempDir(), "handshake.wasm")
				if err := os.WriteFile(p.Path, tc.guest, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			testWebSocketProductionPolicyHandshake(t, func(c *config.Config) {
				c.Plugins = map[string]config.PluginConfig{"handshake": p}
				c.Servers[0].Locations[0].Plugins = []string{"handshake"}
			}, tc.cases)
		})
	}
}
