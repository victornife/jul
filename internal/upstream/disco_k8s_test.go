// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build kubernetes

package upstream

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/egress"
)

func TestK8sDiscovererResolve(t *testing.T) {
	const payload = `{
	  "items": [
	    {
	      "ports": [{"name":"http","port":8080},{"name":"metrics","port":9090}],
	      "endpoints": [
	        {"addresses":["10.1.0.1"],"conditions":{"ready":true}},
	        {"addresses":["10.1.0.2"],"conditions":{"ready":false}},
	        {"addresses":["10.1.0.3"],"conditions":{}}
	      ]
	    }
	  ]
	}`

	var gotPath, gotQuery, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	d, err := newKubernetesDiscoverer(config.DiscoveryConfig{
		Type: "kubernetes",
		Kubernetes: &config.KubernetesDiscovery{
			Namespace:             "default",
			Service:               "web",
			Port:                  "http",
			APIServer:             srv.URL,
			Token:                 "tok",
			InsecureSkipTLSVerify: true,
		},
	}, nil)
	if err != nil {
		t.Fatalf("newKubernetesDiscoverer: %v", err)
	}

	targets, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Ready endpoint + the nil-condition endpoint (treated as ready); the
	// explicitly not-ready one is skipped. Port "http" -> 8080.
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2", len(targets))
	}
	want := map[string]bool{"10.1.0.1:8080": true, "10.1.0.3:8080": true}
	for _, tg := range targets {
		if !want[tg.Address] {
			t.Errorf("unexpected target %q", tg.Address)
		}
	}

	if gotPath != "/apis/discovery.k8s.io/v1/namespaces/default/endpointslices" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotQuery, "kubernetes.io%2Fservice-name") || !strings.Contains(gotQuery, "web") {
		t.Errorf("query = %q, want service-name label selector for web", gotQuery)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if d.Describe() != "kubernetes:default/web" {
		t.Errorf("Describe = %q", d.Describe())
	}
}

func TestK8sSelectPort(t *testing.T) {
	d := &k8sDiscoverer{}
	ports := []k8sPort{{Name: "http", Port: 8080}, {Name: "grpc", Port: 9090}}

	d.port = ""
	if got := d.selectPort(ports); got != 8080 {
		t.Errorf("empty port -> %d, want first 8080", got)
	}
	d.port = "grpc"
	if got := d.selectPort(ports); got != 9090 {
		t.Errorf("named grpc -> %d, want 9090", got)
	}
	d.port = "9090"
	if got := d.selectPort(ports); got != 9090 {
		t.Errorf("numeric 9090 -> %d, want 9090", got)
	}
	d.port = "nope"
	if got := d.selectPort(ports); got != 0 {
		t.Errorf("unmatched -> %d, want 0", got)
	}
}

func TestK8sRequiresNamespaceAndService(t *testing.T) {
	if _, err := newKubernetesDiscoverer(config.DiscoveryConfig{Type: "kubernetes", Kubernetes: &config.KubernetesDiscovery{Service: "web", APIServer: "https://x"}}, nil); err == nil {
		t.Fatal("expected error: kubernetes without namespace")
	}
}

// k8sDiscoverer builds a discoverer against srv guarded by an egress policy that
// allows the given entry. It shares the payload/setup of TestK8sDiscovererResolve.
func newK8sEgressDiscoverer(t *testing.T, apiServer string, allow ...string) Discoverer {
	t.Helper()
	pol, err := egress.New(config.EgressConfig{Enabled: true, Allow: allow})
	if err != nil {
		t.Fatalf("egress.New: %v", err)
	}
	dial := pol.For(egress.SubsystemDiscovery).DialContext(&net.Dialer{Timeout: 2 * time.Second})
	d, err := newKubernetesDiscoverer(config.DiscoveryConfig{
		Type: "kubernetes",
		Kubernetes: &config.KubernetesDiscovery{
			Namespace:             "default",
			Service:               "web",
			Port:                  "http",
			APIServer:             apiServer,
			Token:                 "tok",
			InsecureSkipTLSVerify: true,
		},
	}, dial)
	if err != nil {
		t.Fatalf("newKubernetesDiscoverer: %v", err)
	}
	return d
}

// TestK8sDiscovererEgressAllowed proves the Kubernetes API client honours an
// allowing egress guard: the endpointslice fetch to a permitted API server
// resolves normally.
func TestK8sDiscovererEgressAllowed(t *testing.T) {
	const payload = `{"items":[{"ports":[{"name":"http","port":8080}],"endpoints":[{"addresses":["10.1.0.1"],"conditions":{"ready":true}}]}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	// srv listens on 127.0.0.1, inside the allow-list.
	d := newK8sEgressDiscoverer(t, srv.URL, "127.0.0.0/8")
	targets, err := d.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve through an allowing egress guard: %v", err)
	}
	if len(targets) != 1 || targets[0].Address != "10.1.0.1:8080" {
		t.Errorf("targets = %+v, want one 10.1.0.1:8080", targets)
	}
}

// TestK8sDiscovererEgressBlocked proves a disallowed API server address fails
// the resolve rather than reaching an unapproved Kubernetes endpoint.
func TestK8sDiscovererEgressBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	}))
	defer srv.Close()

	// srv is on 127.0.0.1 but the allow-list only permits 10.0.0.0/8.
	d := newK8sEgressDiscoverer(t, srv.URL, "10.0.0.0/8")
	if _, err := d.Resolve(context.Background()); err == nil {
		t.Error("expected Resolve to fail when the API server is outside the egress allow-list")
	}
}
