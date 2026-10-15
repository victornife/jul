// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminoidc

import (
	"context"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestProviderEgressGenerationTighteningAndRecovery(t *testing.T) {
	r, _, c, srv := fixture(t)
	manager, err := egress.NewManager(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Current().CloseIdleConnections()
	client := manager.Client(egress.SubsystemAuth, 10*time.Second)
	r.client.Transport = client.Transport
	if login(t, c, srv).StatusCode != 200 {
		t.Fatal("allowed OIDC flow failed")
	}
	start := request(t, c, "POST", srv.URL+"/auth/oidc/login", srv.URL, false)
	callback := request(t, c, "GET", start.Header.Get("Location"), "", false).Header.Get("Location")
	denied, _, err := manager.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"192.0.2.1"}})
	if err != nil {
		t.Fatal(err)
	}
	manager.Publish(denied).CloseIdleConnections()
	if request(t, c, "GET", callback, "", false).Header.Get("Location") != "/?oidc_error=login_failed" {
		t.Fatal("token exchange bypassed tightened egress")
	}
	r.providerUntil = time.Time{}
	if _, err := r.discover(context.Background()); err == nil {
		t.Fatal("discovery bypassed tightened egress")
	}
	if request(t, c, "GET", srv.URL, "", false).StatusCode != 200 {
		t.Fatal("existing local session depends on provider egress")
	}
	allowed, _, err := manager.Prepare(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	manager.Publish(allowed).CloseIdleConnections()
	defer allowed.CloseIdleConnections()
	r.providerRetry = time.Time{}
	if _, err := r.discover(context.Background()); err != nil {
		t.Fatal("allow-list recovery failed", err)
	}
}

func BenchmarkGroupMapping(b *testing.B) {
	r := &Runtime{cfg: config.AdminOIDCConfig{Issuer: "https://idp.invalid", GroupClaim: "groups", Groups: []config.AdminOIDCGroup{{Group: "operators", Role: "operator"}}}, policy: policy(b)}
	claims := map[string]any{"sub": "operator-1", "groups": []any{"unmapped", "operators"}}
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if _, err := r.mapIdentity(claims); err != nil {
				b.Fatal(err)
			}
		}
	})
}
