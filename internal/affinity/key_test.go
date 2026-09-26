// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package affinity

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"jul/internal/clientaddr"
)

func req(t *testing.T, remote string, h http.Header) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for k, v := range h {
		r.Header[k] = v
	}
	return r
}

func withIdentity(r *http.Request, id clientaddr.Identity) *http.Request {
	return r.WithContext(clientaddr.NewContext(context.Background(), id))
}

func TestClientIPKey(t *testing.T) {
	src := NewSource("client_ip", "")
	v4 := src.FromRequest(req(t, "203.0.113.7:5555", nil))
	if !v4.Hashed() || v4.Sum() != Sum("203.0.113.7") {
		t.Fatalf("IPv4 key = %+v, want hashed Sum(\"203.0.113.7\")", v4)
	}
	// The port never participates; the same client on a new connection keeps
	// its backend.
	if src.FromRequest(req(t, "203.0.113.7:6666", nil)) != v4 {
		t.Fatal("client port changed the key")
	}
	v6 := src.FromRequest(req(t, "[2001:DB8::1]:443", nil))
	if !v6.Hashed() || v6.Sum() != Sum("2001:db8::1") {
		t.Fatalf("IPv6 key not canonical: %+v", v6)
	}
	mapped := src.FromRequest(req(t, "[::ffff:203.0.113.7]:1", nil))
	if mapped != v4 {
		t.Fatal("IPv4-mapped IPv6 does not hash as its IPv4 form")
	}
	// A header carrying the same address text lands on the same backend.
	if NewSource("header", "X-Real-IP").FromRequest(req(t, "10.0.0.1:1", http.Header{"X-Real-Ip": {"203.0.113.7"}})) != v4 {
		t.Fatal("client_ip and an equal header value hash differently")
	}
	if k := src.FromRequest(req(t, "garbage", nil)); k.Status() != StatusInvalid {
		t.Fatalf("unparseable peer = %v, want invalid", k.Status())
	}
}

// Behind a trusted proxy the canonical client identity is used, not the
// proxy; an identity that fell back to the proxy hop is invalid, never hashed.
func TestClientIPKeyUsesTrustedProxyIdentity(t *testing.T) {
	src := NewSource("client_ip", "")
	client := netip.MustParseAddr("198.51.100.23")
	proxy := netip.MustParseAddr("10.0.0.9")
	accepted := withIdentity(req(t, "10.0.0.9:1234", nil), clientaddr.Identity{Client: client, Peer: proxy, Result: clientaddr.ResultAccepted})
	if k := src.FromRequest(accepted); !k.Hashed() || k.Sum() != Sum("198.51.100.23") {
		t.Fatalf("trusted-proxy client key = %+v, want the forwarded client", k)
	}
	spoofed := withIdentity(req(t, "192.0.2.1:1", nil), clientaddr.Identity{Client: netip.MustParseAddr("192.0.2.1"), Peer: netip.MustParseAddr("192.0.2.1"), Result: clientaddr.ResultUntrustedPeer})
	if k := src.FromRequest(spoofed); !k.Hashed() || k.Sum() != Sum("192.0.2.1") {
		t.Fatalf("untrusted peer key = %+v, want the peer itself", k)
	}
	for _, res := range []clientaddr.Result{clientaddr.ResultMalformed, clientaddr.ResultTooManyHops} {
		degraded := withIdentity(req(t, "10.0.0.9:1", nil), clientaddr.Identity{Client: proxy, Peer: proxy, Result: res})
		if k := src.FromRequest(degraded); k.Status() != StatusInvalid {
			t.Fatalf("unattributed identity (%v) = %v, want invalid", res, k.Status())
		}
	}
}

func TestHeaderKey(t *testing.T) {
	src := NewSource("header", "x-tenant")
	if src.Name() != "X-Tenant" || src.Kind() != "header" {
		t.Fatalf("source = %q/%q, want canonical header name", src.Kind(), src.Name())
	}
	cases := []struct {
		name   string
		values []string
		status Status
		sum    string
	}{
		{"absent", nil, StatusMissing, ""},
		{"empty", []string{""}, StatusMissing, ""},
		{"whitespace only", []string{" \t "}, StatusMissing, ""},
		{"value", []string{"tenant-a"}, StatusHashed, "tenant-a"},
		{"trimmed", []string{"  tenant-a\t"}, StatusHashed, "tenant-a"},
		{"case preserved", []string{"Tenant-A"}, StatusHashed, "Tenant-A"},
		{"repeated lines", []string{"a", "b"}, StatusInvalid, ""},
		{"at bound", []string{strings.Repeat("x", MaxKeyBytes)}, StatusHashed, strings.Repeat("x", MaxKeyBytes)},
		{"over bound", []string{strings.Repeat("x", MaxKeyBytes+1)}, StatusInvalid, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.values != nil {
			h["X-Tenant"] = c.values
		}
		k := src.FromRequest(req(t, "10.0.0.1:1", h))
		if k.Status() != c.status {
			t.Errorf("%s: status %v, want %v", c.name, k.Status(), c.status)
			continue
		}
		if c.status == StatusHashed && k.Sum() != Sum(c.sum) {
			t.Errorf("%s: wrong sum", c.name)
		}
	}
}

func TestCookieKey(t *testing.T) {
	src := NewSource("cookie", "sid")
	cases := []struct {
		name   string
		lines  []string
		status Status
		sum    string
	}{
		{"no cookie header", nil, StatusMissing, ""},
		{"other cookies only", []string{"a=1; b=2"}, StatusMissing, ""},
		{"prefix is not a match", []string{"sidx=1; xsid=2"}, StatusMissing, ""},
		{"empty value", []string{"sid="}, StatusMissing, ""},
		{"quoted empty", []string{`sid=""`}, StatusMissing, ""},
		{"value", []string{"a=1; sid=abc; b=2"}, StatusHashed, "abc"},
		{"quoted", []string{`sid="abc"`}, StatusHashed, "abc"},
		{"first wins", []string{"sid=first; sid=second"}, StatusHashed, "first"},
		{"first across lines", []string{"a=1", "sid=line2", "sid=line3"}, StatusHashed, "line2"},
		{"no equals", []string{"sid; a=1"}, StatusMissing, ""},
		{"over bound", []string{"sid=" + strings.Repeat("v", MaxKeyBytes+1)}, StatusInvalid, ""},
	}
	for _, c := range cases {
		h := http.Header{}
		if c.lines != nil {
			h["Cookie"] = c.lines
		}
		k := src.FromRequest(req(t, "10.0.0.1:1", h))
		if k.Status() != c.status {
			t.Errorf("%s: status %v, want %v", c.name, k.Status(), c.status)
			continue
		}
		if c.status == StatusHashed && k.Sum() != Sum(c.sum) {
			t.Errorf("%s: wrong sum", c.name)
		}
	}
}

func TestFromAddr(t *testing.T) {
	src := NewSource("client_ip", "")
	want := Sum("203.0.113.7")
	for _, a := range []net.Addr{
		&net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 1},
		&net.UDPAddr{IP: net.ParseIP("203.0.113.7"), Port: 2},
		&net.TCPAddr{IP: net.ParseIP("::ffff:203.0.113.7"), Port: 3},
		fakeAddr("203.0.113.7:4"),
	} {
		if k := src.FromAddr(a); !k.Hashed() || k.Sum() != want {
			t.Errorf("FromAddr(%v) = %+v, want hashed 203.0.113.7", a, k)
		}
	}
	if k := src.FromAddr(&net.TCPAddr{IP: net.ParseIP("fe80::1"), Zone: "eth0", Port: 1}); !k.Hashed() || k.Sum() != Sum("fe80::1") {
		t.Errorf("zoned address not hashed without zone: %+v", k)
	}
	for _, a := range []net.Addr{nil, fakeAddr("not-an-address"), &net.TCPAddr{}} {
		if k := src.FromAddr(a); k.Status() != StatusInvalid {
			t.Errorf("FromAddr(%v) = %v, want invalid", a, k.Status())
		}
	}
	// HTTP-only kinds extract nothing at L4.
	if k := NewSource("header", "X-Tenant").FromAddr(fakeAddr("203.0.113.7:1")); k.Status() != StatusNone {
		t.Errorf("header source at L4 = %v, want none", k.Status())
	}
}

type fakeAddr string

func (a fakeAddr) Network() string { return "fake" }
func (a fakeAddr) String() string  { return string(a) }

func TestZeroSourceAndStatusStrings(t *testing.T) {
	var s Source
	if k := s.FromRequest(req(t, "10.0.0.1:1", nil)); k.Status() != StatusNone || k.Hashed() {
		t.Fatalf("zero source extracted %+v", k)
	}
	want := map[Status]string{StatusNone: "none", StatusHashed: "hashed", StatusMissing: "missing", StatusInvalid: "invalid"}
	for st, s := range want {
		if st.String() != s {
			t.Errorf("%d.String() = %q, want %q", st, st.String(), s)
		}
	}
	if k := KeyOf("abc"); !k.Hashed() || k.Sum() != Sum("abc") {
		t.Fatal("KeyOf does not hash its material")
	}
}

// Extraction runs on every keyed request; it must not allocate.
func TestKeyExtractionDoesNotAllocate(t *testing.T) {
	r := req(t, "203.0.113.7:5555", http.Header{"X-Tenant": {"tenant-a"}, "Cookie": {"a=1; sid=abc"}})
	for _, src := range []Source{NewSource("client_ip", ""), NewSource("header", "X-Tenant"), NewSource("cookie", "sid")} {
		if n := testing.AllocsPerRun(100, func() { _ = src.FromRequest(r) }); n != 0 {
			t.Errorf("%s extraction allocates %.0f times per request", src.Kind(), n)
		}
	}
}

func BenchmarkKeyExtraction(b *testing.B) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = "203.0.113.7:5555"
	r.Header.Set("X-Tenant", "tenant-a")
	r.Header.Set("Cookie", "a=1; b=2; sid=abcdef0123456789")
	for _, src := range []Source{NewSource("client_ip", ""), NewSource("header", "X-Tenant"), NewSource("cookie", "sid")} {
		b.Run(src.Kind(), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = src.FromRequest(r)
			}
		})
	}
}
