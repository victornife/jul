//go:build consul

package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
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
	})
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
	if _, err := newConsulDiscoverer(config.DiscoveryConfig{Type: "consul", Consul: &config.ConsulDiscovery{}}); err == nil {
		t.Fatal("expected error: consul without service")
	}
}
