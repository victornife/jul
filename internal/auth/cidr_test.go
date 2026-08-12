// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package auth

import (
	"testing"

	"jul/internal/clientaddr"
)

func TestCIDRGate(t *testing.T) {
	tests := []struct {
		name       string
		allow      []string
		deny       []string
		remoteAddr string
		want       bool
	}{
		{"no rules allows all", nil, nil, "203.0.113.7:1234", true},
		{"allow match", []string{"10.0.0.0/8"}, nil, "10.1.2.3:9", true},
		{"allow miss", []string{"10.0.0.0/8"}, nil, "192.168.1.1:9", false},
		{"deny match", nil, []string{"192.168.0.0/16"}, "192.168.1.1:9", false},
		{"deny miss", nil, []string{"192.168.0.0/16"}, "10.0.0.1:9", true},
		{"deny wins over allow", []string{"10.0.0.0/8"}, []string{"10.0.0.0/24"}, "10.0.0.5:9", false},
		{"allow outside deny subnet", []string{"10.0.0.0/8"}, []string{"10.0.0.0/24"}, "10.0.1.5:9", true},
		{"ipv6 allow match", []string{"2001:db8::/32"}, nil, "[2001:db8::1]:443", true},
		{"ipv6 allow miss", []string{"2001:db8::/32"}, nil, "[2001:dead::1]:443", false},
		{"ipv4-mapped matches ipv4 prefix", []string{"10.0.0.0/8"}, nil, "[::ffff:10.0.0.1]:9", true},
		{"bare host no port", []string{"10.0.0.0/8"}, nil, "10.0.0.1", true},
		{"unparseable addr with allow fails closed", []string{"10.0.0.0/8"}, nil, "garbage", false},
		{"unparseable addr without allow passes", nil, []string{"10.0.0.0/8"}, "garbage", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := newCIDRGate(tt.allow, tt.deny)
			if got := g.allowed(clientaddr.PeerFromRemoteAddr(tt.remoteAddr)); got != tt.want {
				t.Errorf("allowed(%q) = %v, want %v", tt.remoteAddr, got, tt.want)
			}
		})
	}
}

func TestCIDRGateEmpty(t *testing.T) {
	if !newCIDRGate(nil, nil).empty() {
		t.Error("gate with no rules should be empty")
	}
	if newCIDRGate([]string{"10.0.0.0/8"}, nil).empty() {
		t.Error("gate with an allow rule should not be empty")
	}
	if newCIDRGate(nil, []string{"10.0.0.0/8"}).empty() {
		t.Error("gate with a deny rule should not be empty")
	}
}
