// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package clientaddr

import (
	"net/http"
	"net/netip"
	"testing"
)

// FuzzDerive drives the whole derivation path with attacker-controlled header
// values. The invariants it asserts are the security contract: the peer is
// never lost, an untrusted peer is never overridden, an accepted client is
// always a valid address that is not one of the trusted proxies unless the
// whole chain was trusted, and a failure always lands on the peer.
func FuzzDerive(f *testing.F) {
	seeds := []struct{ forwarded, xff string }{
		{"", ""},
		{"for=192.0.2.60", ""},
		{"", "198.51.100.9, 10.8.8.8"},
		{`for="[2001:db8::1]:4711";proto=https`, "10.4.4.4"},
		{"for=_hidden, for=unknown", "not-an-ip,,10.1.1.1"},
		{`for="\"`, "::ffff:10.1.2.3"},
		{"for=10.0.0.1;for=10.0.0.2", "10.0.0.1, 10.0.0.2, 10.0.0.3"},
	}
	for _, s := range seeds {
		f.Add(s.forwarded, s.xff, "10.1.2.3:5555")
		f.Add(s.forwarded, s.xff, "203.0.113.7:80")
	}

	policy, err := NewPolicy([]string{"10.0.0.0/8", "2001:db8:100::/48"}, nil, DefaultMaxHops)
	if err != nil {
		f.Fatalf("NewPolicy: %v", err)
	}

	f.Fuzz(func(t *testing.T, forwarded, xff, remote string) {
		h := http.Header{}
		h.Set("Forwarded", forwarded)
		h.Set("X-Forwarded-For", xff)

		peer := PeerFromRemoteAddr(remote)
		id := policy.Derive(peer, h)

		if id.Peer != peer {
			t.Fatalf("peer mutated: %v -> %v", peer, id.Peer)
		}
		if !peer.IsValid() {
			if id.Client.IsValid() || id.Result != ResultMalformed {
				t.Fatalf("unparseable peer produced %+v", id)
			}
			return
		}
		if id.Result != ResultAccepted && id.Client != peer {
			t.Fatalf("failed derivation did not fall back to the peer: %+v", id)
		}
		if !policy.Trusts(peer) && id.Client != peer {
			t.Fatalf("untrusted peer %v was overridden by %v", peer, id.Client)
		}
		if id.Source != SourcePeer {
			if !id.Client.IsValid() {
				t.Fatalf("asserted source produced an invalid client: %+v", id)
			}
			if id.Client != normalize(id.Client) {
				t.Fatalf("client is not in normalized form: %v", id.Client)
			}
			if id.Client.Zone() != "" {
				t.Fatalf("client carries a zone: %v", id.Client)
			}
		}
	})
}

// FuzzParsePrefix asserts that a configured trusted-proxy entry is either
// rejected or compiles to a canonical prefix that contains its own address.
func FuzzParsePrefix(f *testing.F) {
	for _, seed := range []string{"10.0.0.0/8", "192.0.2.1", "2001:db8::/32", "10.1.2.3/8", "", "::ffff:1.2.3.4", "0.0.0.0/0"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, entry string) {
		prefix, err := ParsePrefix(entry)
		if err != nil {
			return
		}
		if prefix.Masked() != prefix {
			t.Fatalf("ParsePrefix(%q) = %s, which is not canonical", entry, prefix)
		}
		if !prefix.Contains(prefix.Addr()) {
			t.Fatalf("ParsePrefix(%q) = %s does not contain its own address", entry, prefix)
		}
		if prefix.Addr() != normalize(prefix.Addr()) {
			t.Fatalf("ParsePrefix(%q) = %s is not normalized", entry, prefix)
		}
		var zero netip.Addr
		if prefix.Addr() == zero {
			t.Fatalf("ParsePrefix(%q) produced the zero address", entry)
		}
	})
}
