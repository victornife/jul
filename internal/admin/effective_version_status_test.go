// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"net/http"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/server"
	"jul/internal/versionid"
)

// Embeddings may provide snapshots without a precomputed version. The public
// status boundary must use the injected private context in that case, preserve
// an already-published identifier, and never invent serving state when absent.
func TestV1ServingVersionPreservesPublicationAndProtectsEmbeddingFallback(t *testing.T) {
	key, err := versionid.New(bytes.Repeat([]byte{31}, 32))
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ProxyTarget("http://127.0.0.1:9000", ":8080")
	cfg.Servers[0].Name = "synthetic-resolved-value"
	published := key.Digest("effective-config", []byte("published immutable snapshot"))
	for _, tt := range []struct {
		name     string
		snapshot func() server.LiveSnapshot
		want     string
	}{
		{name: "unavailable"},
		{name: "no effective configuration", snapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveVersion: published}
		}},
		{name: "embedding fallback", snapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: cfg}
		}, want: key.Config(cfg)},
		{name: "published identity", snapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{EffectiveConfig: cfg, EffectiveVersion: published}
		}, want: published},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, config.AdminConfig{}, Deps{EffectiveVersions: key, LiveSnapshot: tt.snapshot})
			rr := getV1(t, s, "/api/v1/status", "")
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d", rr.Code)
			}
			got := decodeInto[adminapi.StatusResponse](t, rr)
			if got.ServingVersion != tt.want {
				t.Fatal("serving identity lost its publication/context boundary")
			}
			if got.ServingVersion != "" {
				if versionid.Comparison(got.ServingVersion, key.Config(cfg)) != "same_context" || got.ServingVersion == server.CanonicalVersion(cfg) {
					t.Fatal("public status exposed an unkeyed or foreign-context identity")
				}
			}
		})
	}
}
