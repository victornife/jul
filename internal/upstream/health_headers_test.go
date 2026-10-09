// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl
package upstream

import (
	"context"
	"jul/internal/config"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHealthHTTPHeadersAndHostEveryProbe(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Host != "health.internal:8080" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Forwarded-Proto") != "https" || r.Header.Get("User-Agent") != "custom-probe" {
			w.WriteHeader(403)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	cfg := config.HealthCheckConfig{Type: "http", Path: "/healthz", Host: "health.internal:8080", Headers: map[string]string{"Authorization": "Bearer secret", "X-Forwarded-Proto": "https", "User-Agent": "custom-probe"}}
	hc := &healthChecker{params: healthParamsFrom(cfg), client: srv.Client()}
	cfg.Headers["Authorization"] = "wrong"
	b := &Backend{URL: u, Address: u.Host}
	for i := 0; i < 3; i++ {
		if !hc.probeHTTP(context.Background(), b) {
			t.Fatal("probe failed or snapshot mutated")
		}
	}
	if calls != 3 {
		t.Fatal(calls)
	}
	hc.params.host = "wrong.internal"
	if hc.probeHTTP(context.Background(), b) {
		t.Fatal("wrong vhost passed")
	}
}
func TestHealthConfigIdentityAndOwnership(t *testing.T) {
	a := config.HealthCheckConfig{Enabled: true, Type: "http", Host: "one", Headers: map[string]string{"X": "one"}, ExpectStatus: []int{200}}
	b := healthCfgOrZero(&a)
	a.Headers["X"] = "two"
	a.ExpectStatus[0] = 201
	if b.Headers["X"] != "one" || b.ExpectStatus[0] != 200 {
		t.Fatal("config snapshot aliases input")
	}
	if healthConfigEqual(a, b) {
		t.Fatal("headers/status changes ignored")
	}
	for _, mutate := range []func(*config.HealthCheckConfig){func(c *config.HealthCheckConfig) { c.Host = "two" }, func(c *config.HealthCheckConfig) { c.Headers["X"] = "two" }, func(c *config.HealthCheckConfig) { c.Service = "changed" }} {
		c := healthCfgOrZero(&b)
		mutate(&c)
		if healthConfigEqual(b, c) {
			t.Fatal("identity mutation ignored")
		}
	}
	r := NewRegistry(RegistryOptions{})
	defer r.CloseAll()
	up := upstreamCfg("api", "round_robin", "127.0.0.1:8080")
	up.HealthCheck = &b
	r.startHealthChecks = func(*Pool, config.HealthCheckConfig, HealthHook, ProbeHook) {}
	r.Begin()
	p, err := r.For(context.Background(), up, "http")
	if err != nil {
		t.Fatal(err)
	}
	r.Commit()
	r.Activate()
	up.HealthCheck = func() *config.HealthCheckConfig { v := healthCfgOrZero(&b); v.Headers["X"] = "three"; return &v }()
	r.Begin()
	replacement, err := r.For(context.Background(), up, "http")
	if err != nil {
		t.Fatal(err)
	}
	if replacement == p {
		t.Fatal("changed headers reused live pool")
	}
	r.Commit()
	r.Activate()
	select {
	case <-p.Done():
	case <-time.After(time.Second):
		t.Fatal("old checker owner not retired")
	}
}
