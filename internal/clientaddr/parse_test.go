// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package clientaddr

import (
	"net/netip"
	"strings"
	"testing"
)

func TestParseForwardedElements(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    []string // "" marks a hop with no usable address
		wantErr bool
	}{
		{name: "simple", value: "for=192.0.2.60", want: []string{"192.0.2.60"}},
		{name: "quoted ipv6 with port", value: `for="[2001:db8:cafe::17]:4711"`, want: []string{"2001:db8:cafe::17"}},
		{name: "quoted ipv6 without port", value: `for="[2001:db8::1]"`, want: []string{"2001:db8::1"}},
		{name: "ipv4 with port", value: "for=192.0.2.60:4711", want: []string{"192.0.2.60"}},
		{name: "case insensitive parameter", value: "FOR=192.0.2.60", want: []string{"192.0.2.60"}},
		{name: "other parameters ignored", value: "by=203.0.113.43;for=192.0.2.60;proto=http;host=example.com", want: []string{"192.0.2.60"}},
		{name: "multiple elements keep order", value: "for=192.0.2.43, for=198.51.100.17", want: []string{"192.0.2.43", "198.51.100.17"}},
		{name: "obfuscated identifier", value: "for=_gazonk", want: []string{""}},
		{name: "unknown identifier", value: "for=Unknown", want: []string{""}},
		{name: "element without for", value: "proto=https", want: []string{""}},
		{name: "empty element", value: "for=192.0.2.60,", want: []string{"192.0.2.60", ""}},
		{name: "hostname is not an address", value: "for=client.example.com", want: []string{""}},
		{name: "unbracketed ipv6", value: "for=2001:db8::1", want: []string{""}},
		{name: "bracketed ipv4", value: `for="[192.0.2.60]"`, want: []string{""}},
		{name: "zoned address", value: `for="[fe80::1%eth0]"`, want: []string{""}},
		{name: "escaped quote inside value", value: `for="\"192.0.2.60"`, want: []string{""}},
		{name: "comma inside quotes is not a separator", value: `for="[2001:db8::1]";host="a,b"`, want: []string{"2001:db8::1"}},
		{name: "invalid port", value: "for=192.0.2.60:notaport", want: []string{""}},
		{name: "oversized port", value: "for=192.0.2.60:1234567", want: []string{""}},
		{name: "unterminated quote", value: `for="192.0.2.60`, wantErr: true},
		{name: "trailing escape", value: `for="192.0.2.60\`, wantErr: true},
		{name: "parameter without value", value: "for", wantErr: true},
		{name: "duplicate for", value: "for=192.0.2.60;for=198.51.100.17", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hops, err := parseChain(SourceForwarded, []string{tt.value}, DefaultMaxHops)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseChain(%q) = %v, want an error", tt.value, hops)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseChain(%q): %v", tt.value, err)
			}
			assertHops(t, hops, tt.want)
		})
	}
}

func TestParseXFFEntries(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  []string
	}{
		{name: "single", value: "192.0.2.60", want: []string{"192.0.2.60"}},
		{name: "spaces trimmed", value: "  192.0.2.60 ,198.51.100.17 ", want: []string{"192.0.2.60", "198.51.100.17"}},
		{name: "ipv6", value: "2001:db8::1, 192.0.2.60", want: []string{"2001:db8::1", "192.0.2.60"}},
		{name: "ipv4-mapped ipv6 normalized", value: "::ffff:192.0.2.60", want: []string{"192.0.2.60"}},
		{name: "port is not standard", value: "192.0.2.60:4711", want: []string{""}},
		{name: "brackets are not standard", value: "[2001:db8::1]", want: []string{""}},
		{name: "zone rejected", value: "fe80::1%eth0", want: []string{""}},
		{name: "empty entry", value: "192.0.2.60,,198.51.100.17", want: []string{"192.0.2.60", "", "198.51.100.17"}},
		{name: "garbage entry", value: "not-an-ip", want: []string{""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hops, err := parseChain(SourceXFF, []string{tt.value}, DefaultMaxHops)
			if err != nil {
				t.Fatalf("parseChain(%q): %v", tt.value, err)
			}
			assertHops(t, hops, tt.want)
		})
	}
}

func TestParseChainStopsAtTheHopLimit(t *testing.T) {
	long := strings.Repeat("192.0.2.60, ", 40) + "198.51.100.17"
	if _, err := parseChain(SourceXFF, []string{long}, 8); err != errTooManyHops {
		t.Fatalf("xff over the hop limit: err = %v, want errTooManyHops", err)
	}
	if _, err := parseChain(SourceForwarded, []string{strings.Repeat("for=192.0.2.60, ", 40)}, 8); err != errTooManyHops {
		t.Fatalf("forwarded over the hop limit: err = %v, want errTooManyHops", err)
	}
}

func TestParsePrefix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "ipv4 cidr", in: "10.0.0.0/8", want: "10.0.0.0/8"},
		{name: "ipv6 cidr", in: "2001:db8:100::/48", want: "2001:db8:100::/48"},
		{name: "bare ipv4 becomes a host prefix", in: "192.0.2.10", want: "192.0.2.10/32"},
		{name: "bare ipv6 becomes a host prefix", in: "2001:db8::1", want: "2001:db8::1/128"},
		{name: "surrounding space", in: " 10.0.0.0/8 ", want: "10.0.0.0/8"},
		{name: "ipv4-mapped bare address normalized", in: "::ffff:192.0.2.10", want: "192.0.2.10/32"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePrefix(tt.in)
			if err != nil {
				t.Fatalf("ParsePrefix(%q): %v", tt.in, err)
			}
			if got.String() != tt.want {
				t.Fatalf("ParsePrefix(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}

	for _, bad := range []string{"", "  ", "10.1.2.3/8", "2001:db8::1/32", "10.0.0.0/33", "example.com", "10.0.0.0/8 extra", "::ffff:10.0.0.0/104"} {
		if got, err := ParsePrefix(bad); err == nil {
			t.Errorf("ParsePrefix(%q) = %s, want an error", bad, got)
		}
	}
}

func assertHops(t *testing.T, got []netip.Addr, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hops = %v, want %v", got, want)
	}
	for i, w := range want {
		if w == "" {
			if got[i].IsValid() {
				t.Fatalf("hop %d = %s, want an unusable hop", i, got[i])
			}
			continue
		}
		if got[i].String() != w {
			t.Fatalf("hop %d = %s, want %s", i, got[i], w)
		}
	}
}
