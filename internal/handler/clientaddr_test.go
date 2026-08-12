// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"jul/internal/clientaddr"
	"jul/internal/config"
)

// withIdentity attaches the identity a listener with policy would have derived
// for a request arriving from remoteAddr with these headers.
func withIdentity(t *testing.T, req *http.Request, trusted []string) *http.Request {
	t.Helper()
	policy, err := clientaddr.NewPolicy(trusted, nil, 0)
	if err != nil {
		t.Fatalf("NewPolicy: %v", err)
	}
	id := policy.Derive(clientaddr.PeerFromRemoteAddr(req.RemoteAddr), req.Header)
	return req.WithContext(clientaddr.NewContext(req.Context(), id))
}

// TestProxyForwardedChain pins the exact outbound X-Forwarded-For for every
// deployment shape named by ADR 0016. The emission is deliberately lossy: Jul
// sends what it knows — the canonical client and the peer it received the
// request from — and never preserves an inbound chain.
func TestProxyForwardedChain(t *testing.T) {
	tests := []struct {
		name       string
		trusted    []string
		remoteAddr string
		inboundXFF string
		wantXFF    string
	}{
		{
			name:       "direct client to Jul",
			remoteAddr: "203.0.113.7:5555",
			wantXFF:    "203.0.113.7",
		},
		{
			name:       "direct client sending a spoofed chain",
			remoteAddr: "203.0.113.7:5555",
			inboundXFF: "10.9.9.9, 192.0.2.1",
			wantXFF:    "203.0.113.7",
		},
		{
			name:       "client through one trusted proxy",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			inboundXFF: "198.51.100.9",
			wantXFF:    "198.51.100.9, 10.1.2.3",
		},
		{
			name:       "client through multiple trusted proxies drops the middle hops",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			inboundXFF: "198.51.100.9, 10.8.8.8, 10.9.9.9",
			wantXFF:    "198.51.100.9, 10.1.2.3",
		},
		{
			name:       "untrusted sender's chain is discarded entirely",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "203.0.113.7:5555",
			inboundXFF: "198.51.100.9",
			wantXFF:    "203.0.113.7",
		},
		{
			name:       "trusted peer with no asserted chain",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			wantXFF:    "10.1.2.3",
		},
		{
			name:       "malformed chain from a trusted peer falls back to the peer",
			trusted:    []string{"10.0.0.0/8"},
			remoteAddr: "10.1.2.3:5555",
			inboundXFF: "not-an-address",
			wantXFF:    "10.1.2.3",
		},
		{
			name:       "ipv6 client through an ipv6 proxy",
			trusted:    []string{"2001:db8:100::/48"},
			remoteAddr: "[2001:db8:100::5]:443",
			inboundXFF: "2001:db8:900::1",
			wantXFF:    "2001:db8:900::1, 2001:db8:100::5",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotXFF string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotXFF = r.Header.Get("X-Forwarded-For")
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()

			h := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
			req := httptest.NewRequest(http.MethodGet, "http://edge.example/api", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.inboundXFF != "" {
				req.Header.Set("X-Forwarded-For", tt.inboundXFF)
			}
			req = withIdentity(t, req, tt.trusted)

			h.ServeHTTP(httptest.NewRecorder(), req)

			if gotXFF != tt.wantXFF {
				t.Fatalf("X-Forwarded-For = %q, want %q", gotXFF, tt.wantXFF)
			}
		})
	}
}

// TestProxyVariablesUseCanonicalIdentity covers the NGINX-compatible variables:
// $remote_addr is the canonical client (as with NGINX's realip module),
// $realip_remote_addr is the address the connection actually came from, and
// $proxy_add_x_forwarded_for is the same trusted chain the proxy emits.
func TestProxyVariablesUseCanonicalIdentity(t *testing.T) {
	var got http.Header
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	loc := config.LocationConfig{
		ProxyPass: backend.URL,
		Headers: map[string]string{
			"X-Test-Remote": "$remote_addr",
			"X-Test-Peer":   "$realip_remote_addr",
			"X-Test-Chain":  "$proxy_add_x_forwarded_for",
		},
	}
	h := newProxy(t, loc, nil)

	req := httptest.NewRequest(http.MethodGet, "http://edge.example/api", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	req = withIdentity(t, req, []string{"10.0.0.0/8"})

	h.ServeHTTP(httptest.NewRecorder(), req)

	for _, tc := range []struct{ header, want string }{
		{"X-Test-Remote", "198.51.100.9"},
		{"X-Test-Peer", "10.1.2.3"},
		{"X-Test-Chain", "198.51.100.9, 10.1.2.3"},
		{"X-Forwarded-For", "198.51.100.9, 10.1.2.3"},
	} {
		if v := got.Get(tc.header); v != tc.want {
			t.Errorf("%s = %q, want %q", tc.header, v, tc.want)
		}
	}
}

// TestProxyExplicitHeaderOverrideWinsOverConstructedChain pins the ordering
// that makes an operator override authoritative: custom headers are applied
// after the forwarding headers are constructed, so an operator can still set
// X-Forwarded-For explicitly — and only an operator can.
func TestProxyExplicitHeaderOverrideWinsOverConstructedChain(t *testing.T) {
	var gotXFF string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	loc := config.LocationConfig{
		ProxyPass: backend.URL,
		Headers:   map[string]string{"X-Forwarded-For": "$remote_addr"},
	}
	h := newProxy(t, loc, nil)

	req := httptest.NewRequest(http.MethodGet, "http://edge.example/api", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")
	req = withIdentity(t, req, []string{"10.0.0.0/8"})

	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotXFF != "198.51.100.9" {
		t.Fatalf("X-Forwarded-For = %q, want the operator override 198.51.100.9", gotXFF)
	}
}

// TestProxyWithoutIdentityFallsBackToThePeer proves the fail-closed default:
// with no identity in context (no middleware installed), forwarding is
// peer-derived and an inbound chain is still discarded.
func TestProxyWithoutIdentityFallsBackToThePeer(t *testing.T) {
	var gotXFF string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotXFF = r.Header.Get("X-Forwarded-For")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	h := newProxy(t, config.LocationConfig{ProxyPass: backend.URL}, nil)
	req := httptest.NewRequest(http.MethodGet, "http://edge.example/api", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")

	h.ServeHTTP(httptest.NewRecorder(), req)

	if gotXFF != "203.0.113.7" {
		t.Fatalf("X-Forwarded-For = %q, want the transport peer 203.0.113.7", gotXFF)
	}
}

func TestForwardedChain(t *testing.T) {
	tests := []struct {
		client, peer, want string
	}{
		{"198.51.100.9", "10.1.2.3", "198.51.100.9, 10.1.2.3"},
		{"203.0.113.7", "203.0.113.7", "203.0.113.7"},
		{"", "10.1.2.3", "10.1.2.3"},
		{"198.51.100.9", "", "198.51.100.9"},
		{"", "", ""},
	}
	for _, tt := range tests {
		if got := forwardedChain(tt.client, tt.peer); got != tt.want {
			t.Errorf("forwardedChain(%q, %q) = %q, want %q", tt.client, tt.peer, got, tt.want)
		}
	}
}

func TestAddrText(t *testing.T) {
	if got := addrText(netip.Addr{}); got != "" {
		t.Errorf("addrText(invalid) = %q, want empty", got)
	}
	if got := addrText(netip.MustParseAddr("::ffff:10.0.0.1")); got != "::ffff:10.0.0.1" {
		t.Errorf("addrText = %q", got)
	}
}

// TestFastCGIParamsCarryCanonicalIdentity covers the CGI environment: the
// canonical client in REMOTE_ADDR, the transport peer in JUL_PEER_ADDR, and an
// inbound HTTP_X_FORWARDED_FOR overwritten rather than laundered through.
func TestFastCGIParamsCarryCanonicalIdentity(t *testing.T) {
	tests := []struct {
		name        string
		trusted     []string
		remoteAddr  string
		inboundXFF  string
		wantRemote  string
		wantPeer    string
		wantChain   string
		wantPortSet bool
	}{
		{
			name:        "direct client",
			remoteAddr:  "203.0.113.7:5555",
			inboundXFF:  "198.51.100.9",
			wantRemote:  "203.0.113.7",
			wantPeer:    "203.0.113.7",
			wantChain:   "203.0.113.7",
			wantPortSet: true,
		},
		{
			name:        "behind a trusted proxy",
			trusted:     []string{"10.0.0.0/8"},
			remoteAddr:  "10.1.2.3:5555",
			inboundXFF:  "198.51.100.9",
			wantRemote:  "198.51.100.9",
			wantPeer:    "10.1.2.3",
			wantChain:   "198.51.100.9, 10.1.2.3",
			wantPortSet: true,
		},
		{
			name:        "ipv6 peer",
			remoteAddr:  "[2001:db8::1]:443",
			wantRemote:  "2001:db8::1",
			wantPeer:    "2001:db8::1",
			wantChain:   "2001:db8::1",
			wantPortSet: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://app.example/index.php", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.inboundXFF != "" {
				req.Header.Set("X-Forwarded-For", tt.inboundXFF)
			}
			req = withIdentity(t, req, tt.trusted)

			p := buildCGIParams(config.LocationConfig{Root: "/srv"}, req)

			if p["REMOTE_ADDR"] != tt.wantRemote {
				t.Errorf("REMOTE_ADDR = %q, want %q", p["REMOTE_ADDR"], tt.wantRemote)
			}
			if p["JUL_PEER_ADDR"] != tt.wantPeer {
				t.Errorf("JUL_PEER_ADDR = %q, want %q", p["JUL_PEER_ADDR"], tt.wantPeer)
			}
			if p["HTTP_X_FORWARDED_FOR"] != tt.wantChain {
				t.Errorf("HTTP_X_FORWARDED_FOR = %q, want %q", p["HTTP_X_FORWARDED_FOR"], tt.wantChain)
			}
			if _, ok := p["REMOTE_PORT"]; ok != tt.wantPortSet {
				t.Errorf("REMOTE_PORT present = %v, want %v", ok, tt.wantPortSet)
			}
			if req.RemoteAddr != tt.remoteAddr {
				t.Errorf("RemoteAddr = %q, want it untouched", req.RemoteAddr)
			}
		})
	}
}

func TestFastCGIParamsWithoutIdentity(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://app.example/index.php", nil)
	req.RemoteAddr = "203.0.113.7:5555"
	req.Header.Set("X-Forwarded-For", "198.51.100.9")

	p := buildCGIParams(config.LocationConfig{Root: "/srv"}, req)

	if p["REMOTE_ADDR"] != "203.0.113.7" {
		t.Errorf("REMOTE_ADDR = %q, want the transport peer", p["REMOTE_ADDR"])
	}
	if p["HTTP_X_FORWARDED_FOR"] != "203.0.113.7" {
		t.Errorf("HTTP_X_FORWARDED_FOR = %q, want the untrusted chain replaced", p["HTTP_X_FORWARDED_FOR"])
	}
}
