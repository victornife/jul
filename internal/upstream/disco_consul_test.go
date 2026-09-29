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
	"sync/atomic"
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

func TestConsulRejectsMalformedBaseURL(t *testing.T) {
	for _, address := range []string{"ftp://consul.example.test", "https://user:secret@consul.example.test", "https://consul.example.test?token=secret", "https://consul.example.test?", "https://consul.example.test/#fragment", "not-a-url"} {
		t.Run(address, func(t *testing.T) {
			_, err := newConsulDiscoverer(config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{
				Service: "web", Address: address,
			}}, nil)
			if err == nil || !strings.Contains(err.Error(), "address") {
				t.Fatalf("invalid base URL %q: %v", address, err)
			}
		})
	}
}

func TestConsulRejectsServicePathInjection(t *testing.T) {
	for _, service := range []string{"../agent/self", `web\other`, "web?passing=false", "web#fragment"} {
		_, err := newConsulDiscoverer(config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{
			Service: service, Address: "https://consul.example.test",
		}}, nil)
		if err == nil {
			t.Errorf("service %q must not alter the Consul API path", service)
		}
	}
}

func TestConsulDiscovererDoesNotForwardTokenOnRedirect(t *testing.T) {
	var forwarded bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = true
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer redirect.Close()
	d, err := newConsulDiscoverer(config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{
		Address: redirect.URL, Service: "web", Token: "secret",
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Resolve(context.Background()); err == nil {
		t.Fatal("redirected discovery response must fail")
	}
	if forwarded {
		t.Fatal("redirect target received a request carrying the discovery token")
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

func TestConsulEgressDoesNotUseAllowedEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		_, _ = w.Write([]byte(`[{"Service":{"Address":"10.0.0.1","Port":8080}}]`))
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	policy, err := egress.New(config.EgressConfig{Enabled: true, Allow: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	configured := config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{
		Address: "http://blocked.example.invalid:8500", Service: "web",
	}}
	guard := policy.For(egress.SubsystemDiscovery).DialContext(nil)
	d, err := newConsulDiscoverer(configured, guard)
	if err != nil {
		t.Fatal(err)
	}
	if transport := d.(*consulDiscoverer).client.Transport.(*http.Transport); transport.Proxy != nil {
		t.Fatal("guarded Consul transport may route a blocked target through an environment proxy")
	}
	if _, err := d.Resolve(context.Background()); err == nil {
		t.Error("blocked Consul target resolved through an allowed proxy")
	}
	if got := proxyCalls.Load(); got != 0 {
		t.Errorf("proxy received %d requests for a blocked target", got)
	}

	open, err := newConsulDiscoverer(configured, nil)
	if err != nil {
		t.Fatal(err)
	}
	if transport := open.(*consulDiscoverer).client.Transport.(*http.Transport); transport.Proxy == nil {
		t.Error("disabled egress lost default environment-proxy configuration")
	}
}

// TestConsulDiscovererReadsServiceID pins that Consul's ServiceID becomes the
// backend's logical identity, so a re-registered service resets its state
// rather than inheriting the previous registration's failures.
func TestConsulDiscovererReadsServiceID(t *testing.T) {
	const payload = `[
	  {"Node":{"Address":"10.0.0.9"},"Service":{"ID":"web-1","Address":"10.0.0.1","Port":8080,"Weights":{"Passing":1}}},
	  {"Node":{"Address":"10.0.0.10"},"Service":{"Address":"10.0.0.2","Port":8081,"Weights":{"Passing":1}}}
	]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	d, err := newConsulDiscoverer(config.DiscoveryConfig{
		Type:   "consul",
		Consul: &config.ConsulDiscovery{Address: srv.URL, Service: "web"},
	}, nil)
	if err != nil {
		t.Fatalf("newConsulDiscoverer: %v", err)
	}
	targets, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got := map[string]string{}
	for _, tg := range targets {
		got[tg.Address] = tg.ID
	}
	if got["10.0.0.1:8080"] != "web-1" {
		t.Fatalf("ServiceID = %q, want web-1", got["10.0.0.1:8080"])
	}
	if got["10.0.0.2:8081"] != "" {
		t.Fatalf("a registration without an ID reported %q, want empty", got["10.0.0.2:8081"])
	}
}
