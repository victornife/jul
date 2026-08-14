// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package clientaddr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func mustPolicy(t *testing.T, trusted []string, headers []string, maxHops int) *Policy {
	t.Helper()
	p, err := NewPolicy(trusted, headers, maxHops)
	if err != nil {
		t.Fatalf("NewPolicy(%v, %v, %d): %v", trusted, headers, maxHops, err)
	}
	return p
}

func header(pairs ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		h.Add(pairs[i], pairs[i+1])
	}
	return h
}

func TestDerive(t *testing.T) {
	privateProxies := []string{"10.0.0.0/8", "2001:db8:100::/48"}

	tests := []struct {
		name       string
		trusted    []string
		headers    []string
		maxHops    int
		peer       string
		reqHeaders http.Header
		wantClient string
		wantSource Source
		wantResult Result
	}{
		{
			name:       "direct deployment has no policy and client equals peer",
			peer:       "203.0.113.7:5555",
			reqHeaders: header("X-Forwarded-For", "1.2.3.4"),
			wantClient: "203.0.113.7",
			wantSource: SourcePeer,
			wantResult: ResultAccepted,
		},
		{
			name:       "untrusted peer ignores forwarded headers",
			trusted:    privateProxies,
			peer:       "203.0.113.7:5555",
			reqHeaders: header("X-Forwarded-For", "1.2.3.4"),
			wantClient: "203.0.113.7",
			wantSource: SourcePeer,
			wantResult: ResultUntrustedPeer,
		},
		{
			name:       "trusted peer without headers stays on the peer",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header(),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultAccepted,
		},
		{
			name:       "single trusted proxy with xff",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "multiple proxies strip trusted hops right to left",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9, 10.9.9.9, 10.8.8.8"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "spoofed leftmost entries are not reached",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "127.0.0.1, 192.168.5.5, 198.51.100.9, 10.8.8.8"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "all asserted hops trusted yields the leftmost",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "10.4.4.4, 10.5.5.5"),
			wantClient: "10.4.4.4",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "forwarded wins over xff only when explicitly enabled",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=198.51.100.9", "X-Forwarded-For", "1.2.3.4"),
			wantClient: "198.51.100.9",
			wantSource: SourceForwarded,
			wantResult: ResultAccepted,
		},
		{
			// The default list omits Forwarded, so a client-supplied Forwarded
			// cannot displace the value the proxy actually wrote.
			name:       "default policy ignores a client-supplied forwarded header",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=203.0.113.1", "X-Forwarded-For", "198.51.100.9"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "configured order selects xff first",
			trusted:    privateProxies,
			headers:    []string{HeaderXFF, HeaderForwarded},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=198.51.100.9", "X-Forwarded-For", "203.0.113.4"),
			wantClient: "203.0.113.4",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "malformed selected header never falls through to the other",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", `for="unterminated`, "X-Forwarded-For", "198.51.100.9"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "absent forwarded falls through to xff",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "   ", "X-Forwarded-For", "198.51.100.9"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "obfuscated hop reached by the walk fails closed",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=_hidden;proto=https, for=10.5.5.5"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "unknown hop left of an untrusted address is never reached",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=unknown, for=198.51.100.9, for=10.5.5.5"),
			wantClient: "198.51.100.9",
			wantSource: SourceForwarded,
			wantResult: ResultAccepted,
		},
		{
			name:       "hostnames are never canonical clients",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "client.example.com"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "ipv6 client through an ipv6 proxy",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "[2001:db8:100::5]:443",
			reqHeaders: header("Forwarded", `for="[2001:db8:900::1]:4711"`),
			wantClient: "2001:db8:900::1",
			wantSource: SourceForwarded,
			wantResult: ResultAccepted,
		},
		{
			name:       "ipv4-mapped ipv6 peer matches an ipv4 prefix",
			trusted:    privateProxies,
			peer:       "[::ffff:10.1.2.3]:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "port in an xff entry is not guessed at",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9:1234"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "chain longer than max_hops fails closed",
			trusted:    privateProxies,
			maxHops:    2,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9, 10.7.7.7, 10.8.8.8"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultTooManyHops,
		},
		{
			name:       "chain exactly at max_hops is accepted",
			trusted:    privateProxies,
			maxHops:    2,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9, 10.8.8.8"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			name:       "repeated header lines form one chain",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9", "X-Forwarded-For", "10.8.8.8"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
		{
			// The same RFC 9110 rule applies to Forwarded, and a chain split
			// across field lines must not let a spoofed leftmost line escape
			// the right-to-left walk by looking like a separate assertion.
			name:       "repeated forwarded lines form one chain",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=127.0.0.1", "Forwarded", "for=198.51.100.9, for=10.8.8.8"),
			wantClient: "198.51.100.9",
			wantSource: SourceForwarded,
			wantResult: ResultAccepted,
		},
		{
			name:       "empty header list keeps peer identity for a trusted peer",
			trusted:    privateProxies,
			headers:    []string{},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultAccepted,
		},
		{
			name:       "two for parameters in one element are ambiguous",
			trusted:    privateProxies,
			headers:    []string{HeaderForwarded, HeaderXFF},
			peer:       "10.1.2.3:5555",
			reqHeaders: header("Forwarded", "for=198.51.100.9;for=203.0.113.1"),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "oversized header fails closed without parsing",
			trusted:    privateProxies,
			peer:       "10.1.2.3:5555",
			reqHeaders: header("X-Forwarded-For", oversizedChain()),
			wantClient: "10.1.2.3",
			wantSource: SourcePeer,
			wantResult: ResultMalformed,
		},
		{
			name:       "single-host trusted proxy entry",
			trusted:    []string{"192.0.2.10"},
			peer:       "192.0.2.10:5555",
			reqHeaders: header("X-Forwarded-For", "198.51.100.9"),
			wantClient: "198.51.100.9",
			wantSource: SourceXFF,
			wantResult: ResultAccepted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := mustPolicy(t, tt.trusted, tt.headers, tt.maxHops)
			id := policy.Derive(PeerFromRemoteAddr(tt.peer), tt.reqHeaders)
			if got := id.Client.String(); got != tt.wantClient {
				t.Errorf("client = %s, want %s", got, tt.wantClient)
			}
			if id.Source != tt.wantSource {
				t.Errorf("source = %s, want %s", id.Source, tt.wantSource)
			}
			if id.Result != tt.wantResult {
				t.Errorf("result = %s, want %s", id.Result, tt.wantResult)
			}
			if want := PeerFromRemoteAddr(tt.peer); id.Peer != want {
				t.Errorf("peer = %s, want %s", id.Peer, want)
			}
		})
	}
}

// oversizedChain builds an X-Forwarded-For value past the byte bound.
func oversizedChain() string {
	entry := "198.51.100.9, "
	out := make([]byte, 0, maxHeaderBytes+len(entry))
	for len(out) <= maxHeaderBytes {
		out = append(out, entry...)
	}
	return string(out) + "203.0.113.1"
}

func TestDeriveUnparseablePeerFailsClosed(t *testing.T) {
	policy := mustPolicy(t, []string{"10.0.0.0/8"}, nil, 0)
	id := policy.Derive(PeerFromRemoteAddr("not-an-address"), header("X-Forwarded-For", "198.51.100.9"))
	if id.Client.IsValid() || id.Peer.IsValid() {
		t.Fatalf("client %v / peer %v, want both invalid", id.Client, id.Peer)
	}
	if id.Result != ResultMalformed || id.Source != SourcePeer {
		t.Fatalf("result = %s, source = %s, want malformed/peer", id.Result, id.Source)
	}
}

// TestAttributedSeparatesClientsFromUnresolvedHops pins the predicate consumers
// making an access decision rely on: a fallback to the peer of a *trusted*
// proxy names a hop, not a client, while an ignored header from an untrusted
// sender still leaves that sender as the client.
func TestAttributedSeparatesClientsFromUnresolvedHops(t *testing.T) {
	for _, tt := range []struct {
		result Result
		want   bool
	}{
		{result: ResultAccepted, want: true},
		{result: ResultUntrustedPeer, want: true},
		{result: ResultMalformed, want: false},
		{result: ResultTooManyHops, want: false},
	} {
		t.Run(tt.result.String(), func(t *testing.T) {
			if got := (Identity{Result: tt.result}).Attributed(); got != tt.want {
				t.Fatalf("Attributed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNilPolicyTrustsNothing(t *testing.T) {
	var policy *Policy
	id := policy.Derive(PeerFromRemoteAddr("10.1.2.3:80"), header("X-Forwarded-For", "198.51.100.9"))
	if id.Client.String() != "10.1.2.3" || id.Result != ResultAccepted || id.Source != SourcePeer {
		t.Fatalf("identity = %+v, want the peer accepted", id)
	}
	if policy.TrustedCount() != 0 || policy.Trusts(netip.MustParseAddr("10.1.2.3")) {
		t.Fatal("nil policy must trust nothing")
	}
}

func TestNewPolicyRejectsUnusableInput(t *testing.T) {
	tests := []struct {
		name    string
		trusted []string
		headers []string
		maxHops int
	}{
		{name: "host bits set", trusted: []string{"10.1.2.3/8"}},
		{name: "not an address", trusted: []string{"proxy.example.com"}},
		{name: "empty entry", trusted: []string{" "}},
		{name: "unknown header", headers: []string{"x-real-ip"}},
		{name: "duplicate header", headers: []string{HeaderXFF, HeaderXFF}},
		{name: "max hops beyond the limit", maxHops: MaxHopsLimit + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewPolicy(tt.trusted, tt.headers, tt.maxHops); err == nil {
				t.Fatal("NewPolicy accepted unusable input")
			}
		})
	}
}

func TestPolicyOrderIndependence(t *testing.T) {
	a := mustPolicy(t, []string{"10.0.0.0/8", "192.0.2.0/24"}, nil, 0)
	b := mustPolicy(t, []string{"192.0.2.0/24", "10.0.0.0/8"}, nil, 0)
	for _, addr := range []string{"10.0.0.1", "192.0.2.9", "203.0.113.1"} {
		parsed := netip.MustParseAddr(addr)
		if a.Trusts(parsed) != b.Trusts(parsed) {
			t.Fatalf("trust of %s depends on listing order", addr)
		}
	}
}

func TestContextAccessors(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	req.RemoteAddr = "10.1.2.3:5555"

	if got := Client(req); got.String() != "10.1.2.3" {
		t.Fatalf("Client without identity = %s, want the peer", got)
	}
	if got := Peer(req); got.String() != "10.1.2.3" {
		t.Fatalf("Peer without identity = %s, want the peer", got)
	}
	if _, ok := FromContext(req.Context()); ok {
		t.Fatal("FromContext reported an identity that was never installed")
	}

	id := Identity{Client: netip.MustParseAddr("198.51.100.9"), Peer: netip.MustParseAddr("10.1.2.3"), Source: SourceXFF}
	req = req.WithContext(NewContext(req.Context(), id))
	if got := Client(req); got.String() != "198.51.100.9" {
		t.Fatalf("Client = %s, want the canonical client", got)
	}
	if got := Peer(req); got.String() != "10.1.2.3" {
		t.Fatalf("Peer = %s, want the direct peer", got)
	}

	got, ok := FromContext(req.Context())
	if !ok {
		t.Fatal("FromContext found no identity")
	}
	got.Client = netip.MustParseAddr("203.0.113.99")
	again, _ := FromContext(req.Context())
	if again.Client.String() != "198.51.100.9" {
		t.Fatalf("mutating a returned copy changed the stored identity: %s", again.Client)
	}
}

func TestFromContextRejectsForeignValue(t *testing.T) {
	//nolint:staticcheck // deliberately storing under a foreign key shape
	ctx := context.WithValue(context.Background(), ctxKey{}, "not an identity")
	if _, ok := FromContext(ctx); ok {
		t.Fatal("FromContext accepted a value of the wrong type")
	}
}

func TestSpoofedAndEnumStrings(t *testing.T) {
	if !(Identity{Result: ResultUntrustedPeer}).Spoofed() {
		t.Error("untrusted_peer must report Spoofed")
	}
	if (Identity{Result: ResultAccepted}).Spoofed() {
		t.Error("accepted must not report Spoofed")
	}
	for _, tc := range []struct{ got, want string }{
		{SourcePeer.String(), "peer"},
		{SourceForwarded.String(), "forwarded"},
		{SourceXFF.String(), "xff"},
		{Source(200).String(), "peer"},
		{ResultAccepted.String(), "accepted"},
		{ResultUntrustedPeer.String(), "untrusted_peer"},
		{ResultMalformed.String(), "malformed"},
		{ResultTooManyHops.String(), "too_many_hops"},
		{Result(200).String(), "accepted"},
	} {
		if tc.got != tc.want {
			t.Errorf("enum string = %q, want %q", tc.got, tc.want)
		}
	}
}

func BenchmarkDeriveDirect(b *testing.B) {
	policy, err := NewPolicy(nil, nil, 0)
	if err != nil {
		b.Fatal(err)
	}
	h := header()
	peer := netip.MustParseAddr("203.0.113.7")
	b.ReportAllocs()
	for b.Loop() {
		_ = policy.Derive(peer, h)
	}
}

func BenchmarkDeriveProxied(b *testing.B) {
	policy, err := NewPolicy([]string{"10.0.0.0/8"}, nil, 0)
	if err != nil {
		b.Fatal(err)
	}
	h := header("X-Forwarded-For", "198.51.100.9, 10.8.8.8")
	peer := netip.MustParseAddr("10.1.2.3")
	b.ReportAllocs()
	for b.Loop() {
		_ = policy.Derive(peer, h)
	}
}
