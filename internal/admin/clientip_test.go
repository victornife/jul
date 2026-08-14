// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAdminClientIPHandlesBothAddressFamilies pins the address-family parity
// invariant on the one surface that deliberately keeps peer-only identity: the
// admin listener parses its peer itself rather than going through
// internal/clientaddr, so it needs its own proof that IPv6 is not a variant.
func TestAdminClientIPHandlesBothAddressFamilies(t *testing.T) {
	for _, tt := range []struct {
		name       string
		remoteAddr string
		want       string
	}{
		{name: "ipv4", remoteAddr: "203.0.113.7:5555", want: "203.0.113.7"},
		{name: "ipv6", remoteAddr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{name: "ipv6 loopback", remoteAddr: "[::1]:8080", want: "::1"},
		{name: "ipv4-mapped ipv6", remoteAddr: "[::ffff:203.0.113.7]:5555", want: "::ffff:203.0.113.7"},
		// A synthetic request carries no port; the raw value is the best answer.
		{name: "no port", remoteAddr: "203.0.113.7", want: "203.0.113.7"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "http://admin.local/", nil)
			r.RemoteAddr = tt.remoteAddr
			if got := adminClientIP(r); got != tt.want {
				t.Fatalf("adminClientIP(%q) = %q, want %q", tt.remoteAddr, got, tt.want)
			}
		})
	}
}
