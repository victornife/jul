// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build consul

package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestConsulDiscovererResolve(t *testing.T) {
	const payload = `[
	  {"Node":{"Address":"10.0.0.9"},"Service":{"Address":"10.0.0.1","Port":8080,"Weights":{"Passing":7}}},
	  {"Node":{"Address":"10.0.0.10"},"Service":{"Address":"","Port":8081,"Weights":{"Passing":1}}},
	  {"Node":{"Address":""},"Service":{"Address":"","Port":0}}
	]`

	var gotPath, gotQuery, gotToken string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotToken = r.Header.Get("X-Consul-Token")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	passing := true
	d, err := newConsulDiscoverer(config.DiscoveryConfig{
		Type: "consul",
		Consul: &config.ConsulDiscovery{
			Address:     srv.URL,
			Service:     "web",
			Tag:         "v1",
			Datacenter:  "dc1",
			Token:       "secret",
			PassingOnly: &passing,
		},
	}, nil)
	if err != nil {
		t.Fatalf("newConsulDiscoverer: %v", err)
	}

	targets, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2 (third has no address/port)", len(targets))
	}
	if targets[0].Address != "10.0.0.1:8080" || targets[0].Weight != 7 {
		t.Errorf("target[0] = %+v, want 10.0.0.1:8080 weight 7", targets[0])
	}
	// Second entry falls back to the Node address when Service.Address is empty.
	if targets[1].Address != "10.0.0.10:8081" {
		t.Errorf("target[1] = %+v, want 10.0.0.10:8081 (node fallback)", targets[1])
	}

	if gotPath != "/v1/health/service/web" {
		t.Errorf("path = %q, want /v1/health/service/web", gotPath)
	}
	for _, want := range []string{"passing=true", "tag=v1", "dc=dc1"} {
		if !strings.Contains(gotQuery, want) {
			t.Errorf("query %q missing %q", gotQuery, want)
		}
	}
	if gotToken != "secret" {
		t.Errorf("token header = %q, want secret", gotToken)
	}
	if d.Describe() != "consul:web" {
		t.Errorf("Describe = %q", d.Describe())
	}
}

func TestConsulDiscovererRequiresService(t *testing.T) {
	if _, err := newConsulDiscoverer(config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{}}, nil); err == nil {
		t.Fatal("expected error: consul without service")
	}
}

// TestConsulDiscovererEgressBlocked proves the discovery client honours the
// egress guard: a dial that refuses the destination fails the resolve rather
// than reaching an unapproved Consul endpoint.
func TestConsulDiscovererEgressBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()
	block := func(context.Context, string, string) (net.Conn, error) {
		return nil, errors.New("egress: blocked in test")
	}
	d, err := newConsulDiscoverer(config.DiscoveryConfig{
		Type:   "consul",
		Consul: &config.ConsulDiscovery{Address: srv.URL, Service: "web"},
	}, block)
	if err != nil {
		t.Fatalf("newConsulDiscoverer: %v", err)
	}
	if _, err := d.Resolve(context.Background()); err == nil {
		t.Error("expected Resolve to fail when the egress dial is blocked")
	}
}

// TestConsulDiscovererEgressAllowed is the allow counterpart: a real egress
// guard whose allow-list contains the Consul endpoint's loopback address lets
// the resolve complete unchanged.
func TestConsulDiscovererEgressAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"Service":{"Address":"10.0.0.1","Port":8080,"Weights":{"Passing":1}}}]`))
	}))
	defer srv.Close()

	// The test server listens on 127.0.0.1, which the allow-list permits.
	pol, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.0/8"}})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	dial := pol.For(egress.SubsystemDiscovery).DialContext(&net.Dialer{Timeout: 2 * time.Second})
	d, err := newConsulDiscoverer(config.DiscoveryConfig{
		Type:   "consul",
		Consul: &config.ConsulDiscovery{Address: srv.URL, Service: "web"},
	}, dial)
	if err != nil {
		t.Fatalf("newConsulDiscoverer: %v", err)
	}
	targets, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve through an allowing egress guard: %v", err)
	}
	if len(targets) != 1 || targets[0].Address != "10.0.0.1:8080" {
		t.Errorf("targets = %+v, want one 10.0.0.1:8080", targets)
	}
}
