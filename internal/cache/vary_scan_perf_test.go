// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"net/http"
	"testing"
)

// Compare streaming scans to the former allocating parser, including malformed
// list members. A faster scan must not weaken any sensitive/rewrite rejection.
func FuzzVaryListScanCompatibility(f *testing.F) {
	for _, value := range []string{"", ",,", "Accept-Encoding", " Cookie ,, X-Test ", "'AUTHORIZATION';foo", "*", "X-Connection", "X-Rewrite", "X_Header", "\xff,\x00", "Cookie\n"} {
		f.Add(value)
	}
	policies := []*varyTransformPolicy{nil, newProxyVaryTransformPolicy([]string{"X-Rewrite"}, true), newCGIVaryTransformPolicy([]string{"X-Rewrite"}, []string{"X-Prefix"}, true)}
	r := &http.Request{Header: http.Header{"Connection": {"X-Connection"}, "X-Header": {"one"}}}
	f.Fuzz(func(t *testing.T, value string) {
		fields := parseList(value)
		sensitive := false
		for _, field := range fields {
			sensitive = sensitive || isSensitiveVaryField(field)
		}
		if sensitiveVaryFields([]string{value}) != sensitive {
			t.Fatal("sensitive Vary scan changed")
		}
		for _, policy := range policies {
			want := ""
			if sensitive {
				want = "vary_sensitive_rejected"
			} else if policy != nil {
				for _, field := range fields {
					if field != "*" && proxyVaryField(r, policy, field) {
						want = varyRewriteReason(policy)
						break
					}
				}
			}
			if got := varyRejectionFields(r, policy, []string{value}); got != want {
				t.Fatalf("Vary rejection = %q, want %q", got, want)
			}
		}
	})
}
