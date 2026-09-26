// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package affinity

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// FuzzRequestKey feeds arbitrary header and cookie lines to the extractors.
// Whatever arrives, extraction must not panic, a hashed key must equal the Sum
// of material no longer than MaxKeyBytes, and an unhashed key must carry no
// sum a caller could mistake for placement.
func FuzzRequestKey(f *testing.F) {
	f.Add("tenant-a", "sid=abc; x=1", "sid")
	f.Add("", "", "sid")
	f.Add(" \t ", `sid=""`, "sid")
	f.Add(strings.Repeat("x", MaxKeyBytes+1), "sid="+strings.Repeat("y", 300), "sid")
	f.Add("a,b", "sid; sid=; sid=v", "sid")
	f.Fuzz(func(t *testing.T, header, cookie, cookieName string) {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "203.0.113.7:1"
		r.Header["X-Tenant"] = []string{header}
		r.Header["Cookie"] = []string{cookie}
		for _, src := range []Source{NewSource("header", "X-Tenant"), NewSource("cookie", cookieName), NewSource("client_ip", "")} {
			k := src.FromRequest(r)
			switch k.Status() {
			case StatusHashed:
			case StatusMissing, StatusInvalid:
				if k.Sum() != 0 {
					t.Fatalf("%s: unhashed key carries sum %x", src.Kind(), k.Sum())
				}
			default:
				t.Fatalf("%s: unexpected status %v", src.Kind(), k.Status())
			}
		}
		if k := NewSource("header", "X-Tenant").FromRequest(r); k.Hashed() {
			v := strings.Trim(header, " \t")
			if len(v) == 0 || len(v) > MaxKeyBytes || k.Sum() != Sum(v) {
				t.Fatalf("header %q hashed inconsistently", header)
			}
		}
	})
}
