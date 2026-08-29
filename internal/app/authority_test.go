// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import "testing"

// TestResolveConfigAuthority pins ADR 0019 §9.1's fixed-default rule across
// every combination of raw value and hasConfigPath.
func TestResolveConfigAuthority(t *testing.T) {
	cases := []struct {
		name          string
		raw           string
		hasConfigPath bool
		wantAuthority ConfigAuthority
		wantSource    ConfigAuthoritySource
	}{
		{"no config file wins regardless of raw", "managed", false, AuthorityFileOwned, AuthoritySourceNoConfigFile},
		{"no config file, empty raw", "", false, AuthorityFileOwned, AuthoritySourceNoConfigFile},
		{"explicit managed", "managed", true, AuthorityManaged, AuthoritySourceExplicit},
		{"explicit file_owned", "file_owned", true, AuthorityFileOwned, AuthoritySourceExplicit},
		{"omitted defaults to file_owned", "", true, AuthorityFileOwned, AuthoritySourceDefault},
		{"unrecognized value fails safe to file_owned/default", "controller_owned", true, AuthorityFileOwned, AuthoritySourceDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotAuthority, gotSource := ResolveConfigAuthority(tc.raw, tc.hasConfigPath)
			if gotAuthority != tc.wantAuthority || gotSource != tc.wantSource {
				t.Errorf("ResolveConfigAuthority(%q, %v) = (%v, %v), want (%v, %v)",
					tc.raw, tc.hasConfigPath, gotAuthority, gotSource, tc.wantAuthority, tc.wantSource)
			}
		})
	}
}

// TestConfigAuthorityString pins the wire rendering, including the default
// arm for any value other than AuthorityManaged.
func TestConfigAuthorityString(t *testing.T) {
	if got := AuthorityManaged.String(); got != "managed" {
		t.Errorf("AuthorityManaged.String() = %q, want managed", got)
	}
	if got := AuthorityFileOwned.String(); got != "file_owned" {
		t.Errorf("AuthorityFileOwned.String() = %q, want file_owned", got)
	}
	if got := ConfigAuthority(255).String(); got != "file_owned" {
		t.Errorf("unrecognized ConfigAuthority.String() = %q, want file_owned (fail-safe default)", got)
	}
}
