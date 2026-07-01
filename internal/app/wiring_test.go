package app

import (
	"context"
	"testing"
	"time"

	"jul/internal/config"
)

func TestRateKeyKind(t *testing.T) {
	cases := map[string]string{
		"header:X-Api-Key": "header",
		"jwt:sub":          "jwt",
		"ip":               "ip",
		"":                 "ip",
		"something else":   "ip",
	}
	for spec, want := range cases {
		if got := RateKeyKind(spec); got != want {
			t.Errorf("RateKeyKind(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestAuthAndWAFScopeAreStableAndDistinctPerLocation(t *testing.T) {
	srv := config.ServerConfig{Listen: ":8080", ServerNames: []string{"a.example", "b.example"}}
	loc1 := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}}
	loc2 := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/api"}}

	// Stable: same inputs -> same key. Auth and WAF share the scope shape.
	if AuthScope(srv, loc1) != AuthScope(srv, loc1) {
		t.Error("AuthScope not stable for identical inputs")
	}
	if AuthScope(srv, loc1) != WAFScope(srv, loc1) {
		t.Error("AuthScope and WAFScope should produce the same scope for a location")
	}
	// Distinct: different location paths -> different keys.
	if AuthScope(srv, loc1) == AuthScope(srv, loc2) {
		t.Error("AuthScope must differ for different location paths")
	}
}

func TestEffectiveWAF(t *testing.T) {
	global := config.WAFConfig{Enabled: true}
	c := &config.Config{WAF: global}

	// No override -> the global policy applies.
	loc := config.LocationConfig{}
	if got, ok := EffectiveWAF(c, loc); !ok || got.Enabled != true {
		t.Errorf("EffectiveWAF(no override) = (%+v, %v), want global enabled", got, ok)
	}

	// A disabled override wins over an enabled global.
	off := config.WAFConfig{Enabled: false}
	loc.WAF = &off
	if got, ok := EffectiveWAF(c, loc); ok || got.Enabled {
		t.Errorf("EffectiveWAF(disabled override) = (%+v, %v), want disabled", got, ok)
	}
}

func TestUniqueListenAddrs(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":8080"},
		{Listen: ":8443"},
		{Listen: ":8080"}, // duplicate
		{Listen: ""},      // skipped
	}
	got := UniqueListenAddrs(servers)
	want := []string{":8080", ":8443"}
	if len(got) != len(want) {
		t.Fatalf("UniqueListenAddrs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UniqueListenAddrs[%d] = %q, want %q (order should be first-seen)", i, got[i], want[i])
		}
	}
}

func TestAddrServesTLS(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":80"},
		{Listen: ":443", TLS: &config.TLSConfig{Enabled: true}},
		{Listen: ":8443", TLS: &config.TLSConfig{Enabled: false}},
	}
	if AddrServesTLS(servers, ":443") != true {
		t.Error("AddrServesTLS(:443) = false, want true")
	}
	if AddrServesTLS(servers, ":80") != false {
		t.Error("AddrServesTLS(:80) = true, want false")
	}
	if AddrServesTLS(servers, ":8443") != false {
		t.Error("AddrServesTLS(:8443 disabled) = true, want false")
	}
}

func TestIndexUpstreams(t *testing.T) {
	ups := []config.UpstreamConfig{
		{Name: "a", Servers: []config.UpstreamServer{{Address: "10.0.0.1:80"}}},
		{Name: "b"},
	}
	idx := IndexUpstreams(ups)
	if len(idx) != 2 {
		t.Fatalf("IndexUpstreams len = %d, want 2", len(idx))
	}
	if got, ok := idx["a"]; !ok || len(got.Servers) != 1 {
		t.Errorf("IndexUpstreams[a] = (%+v, %v), want the 'a' upstream", got, ok)
	}
	if _, ok := idx["missing"]; ok {
		t.Error("IndexUpstreams should not contain an unknown name")
	}
}

func TestMergeReloadFansInAndSkipsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := make(chan struct{}, 1)
	b := make(chan struct{}, 1)
	out := MergeReload(ctx, a, nil, b)

	recv := func() bool {
		select {
		case <-out:
			return true
		case <-time.After(time.Second):
			return false
		}
	}

	a <- struct{}{}
	if !recv() {
		t.Error("signal on source a did not propagate to merged channel")
	}
	b <- struct{}{}
	if !recv() {
		t.Error("signal on source b did not propagate to merged channel")
	}
}

func TestValidateRuntimeConfig(t *testing.T) {
	// A well-formed proxy config passes the runtime preflight.
	good := config.ProxyTarget("127.0.0.1:9000", ":8080")
	if err := ValidateRuntimeConfig(good); err != nil {
		t.Fatalf("ValidateRuntimeConfig(valid) = %v, want nil", err)
	}

	// A structurally invalid config (a location with no match) is rejected.
	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}},
		}},
	}
	if err := ValidateRuntimeConfig(bad); err == nil {
		t.Error("ValidateRuntimeConfig(invalid) = nil, want an error")
	}
}
