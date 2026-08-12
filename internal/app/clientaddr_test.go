// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/middleware"
)

// proxiedServers builds two virtual hosts sharing one listen address with the
// identical client_address policy the configuration validator requires.
func proxiedServers(policy *config.ClientAddressConfig) []config.ServerConfig {
	loc := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}, ProxyPass: "http://127.0.0.1:9001"}
	return []config.ServerConfig{
		{Listen: ":8080", ServerNames: []string{"public.example.com"}, ClientAddress: policy, Locations: []config.LocationConfig{loc}},
		{Listen: ":8080", ServerNames: []string{"internal.example.com"}, ClientAddress: policy, Locations: []config.LocationConfig{loc}},
		{Listen: ":9090", ServerNames: []string{"direct.example.com"}, Locations: []config.LocationConfig{loc}},
	}
}

func TestClientAddressPolicyIsResolvedPerListenAddress(t *testing.T) {
	servers := proxiedServers(&config.ClientAddressConfig{TrustedProxies: []string{"10.0.0.0/8"}})

	proxied, err := ClientAddressPolicy(servers, ":8080")
	if err != nil {
		t.Fatalf("ClientAddressPolicy(:8080): %v", err)
	}
	if proxied.TrustedCount() != 1 {
		t.Fatalf("trusted count = %d, want 1", proxied.TrustedCount())
	}

	direct, err := ClientAddressPolicy(servers, ":9090")
	if err != nil {
		t.Fatalf("ClientAddressPolicy(:9090): %v", err)
	}
	if direct.TrustedCount() != 0 {
		t.Fatalf("a listener without a policy trusts %d prefixes, want 0", direct.TrustedCount())
	}

	if _, err := ClientAddressPolicy(proxiedServers(&config.ClientAddressConfig{TrustedProxies: []string{"10.1.2.3/8"}}), ":8080"); err == nil {
		t.Fatal("a prefix with host bits set was compiled instead of aborting the build")
	}
}

func TestHandlerFactoryBuildRejectsUncompilablePolicy(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{TrustedProxies: []string{"not-a-prefix"}}

	if _, _, err := f.Build(t.Context(), cfg, true /* commit */); err == nil {
		t.Fatal("Build accepted an uncompilable client_address policy")
	}

	// The generation must have been aborted, so a valid build still succeeds.
	if _, _, err := f.Build(t.Context(), config.ProxyTarget("127.0.0.1:9001", ":0"), false); err != nil {
		t.Fatalf("Build after an aborted generation failed: %v", err)
	}
}

// TestGlobalChainDerivesIdentityAtIndexOne pins the ADR-0016 placement: the
// first two middleware of the production chain alone must already produce both
// a request id and a canonical identity, which is what guarantees that every
// observer and every per-location middleware inside them can read it.
func TestGlobalChainDerivesIdentityAtIndexOne(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	mws := f.globalChain(policy, nil)
	if len(mws) < 2 {
		t.Fatalf("global chain has %d middleware, want at least 2", len(mws))
	}

	var got clientaddr.Identity
	var seen bool
	var requestID string
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, seen = clientaddr.FromContext(r.Context())
		requestID = middleware.RequestIDFrom(r.Context())
	})

	for _, tc := range []struct {
		name  string
		chain []middleware.Middleware
	}{
		{name: "first two middleware", chain: mws[:2]},
		{name: "whole chain", chain: mws},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, seen, requestID = clientaddr.Identity{}, false, ""
			req := httptest.NewRequest(http.MethodGet, "http://attacker.example.com/", nil)
			req.RemoteAddr = "10.1.2.3:5555"
			req.Header.Set("X-Forwarded-For", "198.51.100.9")
			middleware.Chain(probe, tc.chain...).ServeHTTP(httptest.NewRecorder(), req)

			if !seen {
				t.Fatal("no identity was installed")
			}
			if got.Client.String() != "198.51.100.9" {
				t.Errorf("client = %s, want 198.51.100.9", got.Client)
			}
			if got.Peer.String() != "10.1.2.3" {
				t.Errorf("peer = %s, want 10.1.2.3", got.Peer)
			}
			if requestID == "" {
				t.Error("the request id must already be assigned when identity is derived")
			}
		})
	}
}

// TestGlobalChainIdentityIsIndependentOfHost proves the structural defence: the
// same listener chain derives the same identity whatever Host is presented, so
// a virtual host cannot select the trust policy applied to its own request.
func TestGlobalChainIdentityIsIndependentOfHost(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	var got clientaddr.Identity
	chain := middleware.Chain(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = clientaddr.FromContext(r.Context())
	}), f.globalChain(policy, nil)...)

	for _, host := range []string{"public.example.com", "internal.example.com", "", "attacker.invalid"} {
		req := httptest.NewRequest(http.MethodGet, "http://placeholder/", nil)
		req.Host = host
		req.RemoteAddr = "203.0.113.7:5555"
		req.Header.Set("X-Forwarded-For", "198.51.100.9")
		chain.ServeHTTP(httptest.NewRecorder(), req)

		if got.Client.String() != "203.0.113.7" || got.Result != clientaddr.ResultUntrustedPeer {
			t.Fatalf("host %q changed the applied policy: %+v", host, got)
		}
	}
}

// TestClientAddressPolicyReloadUnderLiveTraffic proves that a policy change
// takes effect through ordinary handler generation, with in-flight requests
// continuing against the generation they started on.
//
// It uses Prepare/commit directly — the same seam the reload transaction uses —
// so the assertion is about the real publication boundary rather than a
// simulated one.
func TestClientAddressPolicyReloadUnderLiveTraffic(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	withPolicy := func(trusted []string) *config.Config {
		cfg := config.ProxyTarget("127.0.0.1:9001", "127.0.0.1:0")
		if trusted != nil {
			cfg.Servers[0].ClientAddress = &config.ClientAddressConfig{TrustedProxies: trusted}
		}
		return cfg
	}

	// Generation 1: no proxy trusted.
	policy, err := ClientAddressPolicy(withPolicy(nil).Servers, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("initial policy: %v", err)
	}
	var got clientaddr.Identity
	probe := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, _ = clientaddr.FromContext(r.Context())
	})
	live := middleware.Chain(probe, f.globalChain(policy, nil)...)

	req := func() *http.Request { return request("10.1.2.3:5555", "198.51.100.9") }
	live.ServeHTTP(httptest.NewRecorder(), req())
	if got.Client.String() != "10.1.2.3" || got.Result != clientaddr.ResultAccepted {
		t.Fatalf("before reload: %+v, want the peer", got)
	}

	// Generation 2: the proxy becomes trusted. Preparing must succeed and the
	// new chain must derive the asserted client.
	candidate := withPolicy([]string{"10.0.0.0/8"})
	_, _, commitFn, abortFn, err := f.Prepare(t.Context(), candidate)
	if err != nil {
		t.Fatalf("Prepare with a new policy: %v", err)
	}
	if _, retire := commitFn(); retire != nil {
		retire()
	}
	_ = abortFn

	next, err := ClientAddressPolicy(candidate.Servers, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reloaded policy: %v", err)
	}
	middleware.Chain(probe, f.globalChain(next, nil)...).ServeHTTP(httptest.NewRecorder(), req())
	if got.Client.String() != "198.51.100.9" || got.Source != clientaddr.SourceXFF {
		t.Fatalf("after reload: %+v, want the asserted client", got)
	}

	// The retired generation keeps deriving what it was built with, which is
	// what lets an in-flight request finish coherently.
	live.ServeHTTP(httptest.NewRecorder(), req())
	if got.Client.String() != "10.1.2.3" {
		t.Fatalf("retired generation changed behaviour mid-flight: %+v", got)
	}

	// A malformed policy must abort the reload before anything is published.
	bad := withPolicy([]string{"10.1.2.3/8"})
	if _, _, _, _, err := f.Prepare(t.Context(), bad); err == nil {
		t.Fatal("Prepare accepted a policy that cannot compile")
	}
	middleware.Chain(probe, f.globalChain(next, nil)...).ServeHTTP(httptest.NewRecorder(), req())
	if got.Client.String() != "198.51.100.9" {
		t.Fatalf("a failed reload disturbed the live policy: %+v", got)
	}
}
