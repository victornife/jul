// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build acme

package app

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"jul/internal/admin"
	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/server"
)

func TestDNS01PreflightUsesServingEffectiveCredential(t *testing.T) {
	t.Setenv("JUL_DNS_PREFLIGHT_TSIG", "MDEyMzQ1Njc4OWFiY2RlZg==")
	raw := config.ServeDir(t.TempDir(), "127.0.0.1:0")
	raw.Servers[0].ServerNames = []string{"example.test"}
	raw.Servers[0].TLS = &config.TLSConfig{Enabled: true, ACME: &config.ACMEConfig{
		Enabled: true, Email: "ops@example.test", CA: "letsencrypt-staging",
		Challenge: "dns-01", DNSProvider: "rfc2136", CacheDir: t.TempDir(), Domains: []string{"example.test"},
		RFC2136: &config.RFC2136Config{Server: "127.0.0.1:53", Zone: "example.test",
			TSIGKeyName: "jul.example.test", TSIGSecret: "${env:JUL_DNS_PREFLIGHT_TSIG}"},
	}}
	live, err := config.NewCandidate(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := Preflight{
		BuildHandlers: func(context.Context, *config.Config, bool) (map[string]http.Handler, func(), error) {
			return map[string]http.Handler{}, nil, nil
		},
		Stream:    &mockStreamPreflighter{},
		StartupFP: lifecycle.ComputeFingerprint(live.Effective),
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{RawConfig: live.Raw, EffectiveConfig: live.Effective}
		},
	}
	if _, err := p.Apply(context.Background(), raw, live.Raw, PreflightHot); err != nil {
		t.Fatalf("unchanged referenced credential falsely required restart: %v", err)
	}
	staged, err := p.Apply(context.Background(), raw, live.Raw, PreflightStageRestart)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range staged.Lifecycle {
		if change.Class == lifecycle.RestartRequiredClass {
			t.Fatalf("unchanged referenced credential produced a staged restart: %+v", change)
		}
	}
	t.Setenv("JUL_DNS_PREFLIGHT_TSIG", "ZmVkY2JhOTg3NjU0MzIxMA==")
	if _, err := p.Apply(context.Background(), raw, live.Raw, PreflightHot); !errors.Is(err, admin.ErrRestartRequired) {
		t.Fatalf("resolved credential rotation did not require restart: %v", err)
	}
}
