// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"

	"jul/internal/affinity"
	"jul/internal/clientaddr"
	"jul/internal/config"
	"jul/internal/upstream"
)

// namedBackends starts n HTTP backends that answer with their own name and
// returns the upstream servers plus a name lookup by address.
func namedBackends(t *testing.T, n int) ([]config.UpstreamServer, map[string]string) {
	t.Helper()
	var servers []config.UpstreamServer
	names := map[string]string{}
	for i := 0; i < n; i++ {
		name := "b" + strconv.Itoa(i)
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, name)
		}))
		t.Cleanup(s.Close)
		addr := strings.TrimPrefix(s.URL, "http://")
		servers = append(servers, config.UpstreamServer{Address: addr, Weight: 1})
		names[addr] = name
	}
	return servers, names
}

func affinityUpstream(servers []config.UpstreamServer, h *config.HashConfig) map[string]config.UpstreamConfig {
	return map[string]config.UpstreamConfig{"pool": {
		Name: "pool", Strategy: "consistent_hash", Hash: h, Servers: servers, MaxFails: 1,
	}}
}

// expectedBackend ranks the configured servers for a key with the mapping
// contract itself, independent of the proxy under test.
func expectedBackend(servers []config.UpstreamServer, material string) []string {
	cands := make([]affinity.Candidate, len(servers))
	for i, s := range servers {
		cands[i] = affinity.NewCandidate("tcp", s.Address, s.Weight)
	}
	order := affinity.Rank(affinity.Sum(material), cands)
	out := make([]string, len(order))
	for i, idx := range order {
		out[i] = servers[idx].Address
	}
	return out
}

func serve(t *testing.T, h http.Handler, r *http.Request) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestProxyConsistentHashByHeader(t *testing.T) {
	servers, names := namedBackends(t, 5)
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, affinityUpstream(servers, &config.HashConfig{Key: "header", Name: "X-Tenant"}))
	for i := 0; i < 40; i++ {
		tenant := "tenant-" + strconv.Itoa(i)
		want := names[expectedBackend(servers, tenant)[0]]
		for j := 0; j < 3; j++ {
			r := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
			r.Header.Set("X-Tenant", tenant)
			if got := serve(t, h, r); got != want {
				t.Fatalf("tenant %s request %d reached %s, want %s", tenant, j, got, want)
			}
		}
	}
	// Keyless requests are spread by the fallback, not pinned.
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		seen[serve(t, h, httptest.NewRequest(http.MethodGet, "http://edge/", nil))] = true
	}
	if len(seen) < 5 {
		t.Fatalf("keyless requests reached %d backends under round_robin fallback, want 5", len(seen))
	}
}

func TestProxyConsistentHashByCookie(t *testing.T) {
	servers, names := namedBackends(t, 4)
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, affinityUpstream(servers, &config.HashConfig{Key: "cookie", Name: "sid"}))
	for i := 0; i < 20; i++ {
		sid := "s" + strconv.Itoa(i)
		r := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		r.Header.Set("Cookie", "theme=dark; sid="+sid)
		if got, want := serve(t, h, r), names[expectedBackend(servers, sid)[0]]; got != want {
			t.Fatalf("sid %s reached %s, want %s", sid, got, want)
		}
	}
}

// client_ip follows the canonical client identity: behind a trusted proxy the
// forwarded client places the request, for IPv4 and IPv6 alike.
func TestProxyConsistentHashByClientIP(t *testing.T) {
	servers, names := namedBackends(t, 4)
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, affinityUpstream(servers, &config.HashConfig{Key: "client_ip"}))
	for _, client := range []string{"203.0.113.7", "198.51.100.23", "2001:db8::42", "2001:db8:1::7"} {
		want := names[expectedBackend(servers, client)[0]]

		direct := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		direct.RemoteAddr = net.JoinHostPort(client, "40000")
		if got := serve(t, h, direct); got != want {
			t.Fatalf("direct %s reached %s, want %s", client, got, want)
		}

		proxied := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		proxied.RemoteAddr = "10.0.0.9:5555"
		proxied = proxied.WithContext(clientaddr.NewContext(context.Background(), clientaddr.Identity{
			Client: netip.MustParseAddr(client), Peer: netip.MustParseAddr("10.0.0.9"), Result: clientaddr.ResultAccepted,
		}))
		if got := serve(t, h, proxied); got != want {
			t.Fatalf("via trusted proxy %s reached %s, want %s", client, got, want)
		}
	}
}

// A dead preferred backend is retried onto the key's next-ranked backend, and
// the key then stays there while the circuit is open.
func TestProxyConsistentHashRetriesToNextRanked(t *testing.T) {
	servers, names := namedBackends(t, 2)
	const dead = "127.0.0.1:1" // nothing listens here
	servers = append(servers, config.UpstreamServer{Address: dead, Weight: 1})
	var tenant string
	for i := 0; ; i++ {
		tenant = "t" + strconv.Itoa(i)
		if expectedBackend(servers, tenant)[0] == dead {
			break
		}
	}
	next := names[expectedBackend(servers, tenant)[1]]
	h := newProxy(t, config.LocationConfig{ProxyPass: "http://pool"}, affinityUpstream(servers, &config.HashConfig{Key: "header", Name: "X-Tenant"}))
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "http://edge/", nil)
		r.Header.Set("X-Tenant", tenant)
		if got := serve(t, h, r); got != next {
			t.Fatalf("request %d reached %s, want next-ranked %s", i, got, next)
		}
	}
}

func TestProxyConsistentHashPoolReportsKeyOutcomes(t *testing.T) {
	servers, _ := namedBackends(t, 2)
	ups := affinityUpstream(servers, &config.HashConfig{Key: "header", Name: "X-Tenant"})
	pool, err := upstream.NewPool(ups["pool"], "http")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	pool.SetAffinityHook(func(_, status string) { counts[status]++ })
	tr := &balancingTransport{pool: pool, base: newProxyTransport(config.LocationConfig{}, nil, 0, nil)}
	for _, v := range []string{"a", "", "b"} {
		r := httptest.NewRequest(http.MethodGet, "http://"+servers[0].Address+"/", nil)
		if v != "" {
			r.Header.Set("X-Tenant", v)
		}
		resp, err := tr.RoundTrip(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
	}
	if counts["hashed"] != 2 || counts["missing"] != 1 {
		t.Fatalf("key outcomes = %v, want 2 hashed + 1 missing, one per request", counts)
	}
}
