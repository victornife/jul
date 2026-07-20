// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package rbac

import "testing"

func TestCatalogAllKnown(t *testing.T) {
	for _, p := range Catalog() {
		if !Known(p) {
			t.Errorf("catalog entry %q is not Known", p)
		}
	}
}

func TestWildcardKnown(t *testing.T) {
	if !Known(Wildcard) {
		t.Error("wildcard should be known")
	}
}

func TestResourceWildcardKnown(t *testing.T) {
	cases := []struct {
		perm  Permission
		known bool
	}{
		{"config:*", true},
		{"history:*", true},
		{"unknown:*", false},
		{"status:read", true},
		{"nonesuch", false},
	}
	for _, c := range cases {
		if got := Known(c.perm); got != c.known {
			t.Errorf("Known(%q) = %v, want %v", c.perm, got, c.known)
		}
	}
}

func TestMatchesWildcard(t *testing.T) {
	if !Matches(Wildcard, StatusRead) {
		t.Error("wildcard should match any permission")
	}
	if !Matches(Wildcard, AdminManage) {
		t.Error("wildcard should match admin:manage")
	}
}

func TestMatchesResourceWildcard(t *testing.T) {
	if !Matches("config:*", ConfigRead) {
		t.Error("config:* should match config:read")
	}
	if !Matches("config:*", ConfigApply) {
		t.Error("config:* should match config:apply")
	}
	if Matches("config:*", StatusRead) {
		t.Error("config:* should not match status:read")
	}
}

func TestMatchesExact(t *testing.T) {
	if !Matches(StatusRead, StatusRead) {
		t.Error("exact match should work")
	}
	if Matches(StatusRead, MetricsRead) {
		t.Error("different permissions should not match")
	}
}
