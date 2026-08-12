// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"jul/internal/clientaddr"
	"jul/internal/config"
)

// remoteAddrRule blocks whenever REMOTE_ADDR equals the given address, so the
// test observes exactly what the engine believes the client is.
func remoteAddrRule(addr string) config.WAFConfig {
	return config.WAFConfig{
		Enabled:     true,
		InlineRules: `SecRule REMOTE_ADDR "@streq ` + addr + `" "id:900100,phase:1,deny,status:403,msg:'blocked by remote addr'"`,
	}
}

// serveWithIdentity serves one request carrying id through a firewall built
// from cfg, returning the recorded response.
func serveWithIdentity(t *testing.T, cfg config.WAFConfig, remoteAddr string, id *clientaddr.Identity) *httptest.ResponseRecorder {
	t.Helper()
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = remoteAddr
	if id != nil {
		req = req.WithContext(clientaddr.NewContext(req.Context(), *id))
	}
	rr := httptest.NewRecorder()
	fw.Middleware()(newOKHandler()).ServeHTTP(rr, req)
	if got := req.RemoteAddr; got != remoteAddr {
		t.Fatalf("RemoteAddr = %q, want it untouched (%q)", got, remoteAddr)
	}
	return rr
}

func identity(client, peer string) *clientaddr.Identity {
	return &clientaddr.Identity{
		Client: netip.MustParseAddr(client),
		Peer:   netip.MustParseAddr(peer),
		Source: clientaddr.SourceXFF,
	}
}

// TestWAFSeesCanonicalClientAddress is the assertion the ADR requires: the
// canonical client reaches REMOTE_ADDR, and the proxy's own address does not.
func TestWAFSeesCanonicalClientAddress(t *testing.T) {
	tests := []struct {
		name       string
		rule       string
		remoteAddr string
		identity   *clientaddr.Identity
		wantStatus int
	}{
		{
			name:       "rule matches the canonical client behind a trusted proxy",
			rule:       "198.51.100.9",
			remoteAddr: "10.1.2.3:5555",
			identity:   identity("198.51.100.9", "10.1.2.3"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "rule on the proxy address no longer matches",
			rule:       "10.1.2.3",
			remoteAddr: "10.1.2.3:5555",
			identity:   identity("198.51.100.9", "10.1.2.3"),
			wantStatus: http.StatusOK,
		},
		{
			name:       "direct client is unchanged",
			rule:       "203.0.113.7",
			remoteAddr: "203.0.113.7:5555",
			identity:   identity("203.0.113.7", "203.0.113.7"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "ipv6 client is not mis-split",
			rule:       "2001:db8::1",
			remoteAddr: "[2001:db8:100::5]:443",
			identity:   identity("2001:db8::1", "2001:db8:100::5"),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "no identity falls back to coraza's own parse",
			rule:       "203.0.113.7",
			remoteAddr: "203.0.113.7:5555",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := serveWithIdentity(t, remoteAddrRule(tt.rule), tt.remoteAddr, tt.identity)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rr.Code, tt.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestWAFIgnoresForwardingHeadersFromAnUntrustedPeer proves the spoofing case:
// with no trusted proxy the identity is the peer, so an attacker-supplied
// header cannot move the request out of a rule that targets its real address.
func TestWAFIgnoresForwardingHeadersFromAnUntrustedPeer(t *testing.T) {
	cfg := remoteAddrRule("203.0.113.7")
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	policy, err := clientaddr.NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	id := policy.Derive(clientaddr.PeerFromRemoteAddr(req.RemoteAddr), req.Header)
	req = req.WithContext(clientaddr.NewContext(req.Context(), id))

	rr := httptest.NewRecorder()
	fw.Middleware()(newOKHandler()).ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: a spoofed header bypassed a WAF rule", rr.Code)
	}
}
