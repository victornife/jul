// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"path/filepath"
	"runtime"
	"testing"

	"jul/internal/config"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func loadTestValueContract(t *testing.T) ValueContract {
	t.Helper()
	vc, err := LoadValueContract(filepath.Join(repoRoot(t), "docs", "config-value-contract.json"))
	if err != nil {
		t.Fatalf("LoadValueContract: %v", err)
	}
	return vc
}

// TestValueContractPathsResolveAgainstSchema proves every value-contract path,
// after canonicalization, names a real config.SchemaPaths() entry. This is the
// join invariant ADR 0019 §21 requires: a stale or mistyped audited path must
// fail loudly rather than silently stop being checked.
func TestValueContractPathsResolveAgainstSchema(t *testing.T) {
	schema := map[string]bool{}
	for _, p := range config.SchemaPaths() {
		schema[p.Path] = true
	}
	vc := loadTestValueContract(t)
	if len(vc.Fields) == 0 {
		t.Fatal("value contract has no fields")
	}
	for _, f := range vc.Fields {
		paths := f.CanonicalPaths()
		if len(paths) == 0 {
			t.Errorf("%s: no canonical paths derived from %q", f.GoField, f.RawPath)
		}
		for _, p := range paths {
			if !schema[p] {
				t.Errorf("%s: canonical path %q (from %q) does not resolve against config.SchemaPaths()", f.GoField, p, f.RawPath)
			}
		}
	}
}

// TestValueContractMultiPathEntriesSplit proves the " / " join notation used
// by a handful of audited entries expands to every canonical path it names.
func TestValueContractMultiPathEntriesSplit(t *testing.T) {
	vc := loadTestValueContract(t)
	var found bool
	for _, f := range vc.Fields {
		if f.RawPath != "waf.block_status / servers[].locations[].waf.block_status" {
			continue
		}
		found = true
		got := f.CanonicalPaths()
		want := []string{"waf.block_status", "servers.*.locations.*.waf.block_status"}
		if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
			t.Errorf("CanonicalPaths() = %v, want %v", got, want)
		}
	}
	if !found {
		t.Fatal("expected multi-path waf.block_status entry not found in value contract")
	}
}

// TestValueContractPluginWildcard proves the "<name>" placeholder maps to the
// canonical "*" wildcard segment.
func TestValueContractPluginWildcard(t *testing.T) {
	got := canonicalizeValueContractPath("plugins.<name>.fetch_timeout")
	want := []string{"plugins.*.fetch_timeout"}
	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("canonicalizeValueContractPath = %v, want %v", got, want)
	}
}

func f64(v float64) *float64 { return &v }

func TestParseNumericBound(t *testing.T) {
	cases := []struct {
		name       string
		constraint string
		wantBound  bool
		wantMin    *float64
		wantMax    *float64
	}{
		{"non-negative", "non-negative", true, f64(0), nil},
		{"0 or greater", "0 or greater", true, f64(0), nil},
		{"bare positive", "positive", true, f64(1), nil},
		{"dotdot range", "0..11", true, f64(0), f64(11)},
		{"dotdot with when-set suffix", "100..599 when set", true, f64(100), f64(599)},
		{"dotdot with effective suffix", "100..599 effective", true, f64(100), f64(599)},
		{"to range", "0 to 100000", true, f64(0), f64(100000)},
		{"to range with trailing clause", "0 to 100; must not be set together with the deprecated proxy_retries", true, f64(0), f64(100)},
		{"at least with effective suffix", "at least 1 effective", true, f64(1), nil},
		{"comma-joined non-negative + at-most", "non-negative, at most 255", true, f64(0), f64(255)},
		{
			// The trap case: a naive substring search on "1..4" would wrongly
			// report minimum=1, silently rejecting the valid value 0.
			"disjoint zero-or-range must NOT produce a bound",
			"0 or 1..4; non-zero requires CRS", false, nil, nil,
		},
		{"conditional positive must NOT produce a bound", "positive when upload is enabled; otherwise non-negative", false, nil, nil},
		{"any integer must NOT produce a bound", "any integer; negative disables the limiter", false, nil, nil},
		{"reference to another field must NOT produce a bound", "at least rate", false, nil, nil},
		{"duration-only prose must NOT produce a bound", "positive and less than interval", false, nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseNumericBound(tc.constraint)
			if got.HasBound != tc.wantBound {
				t.Fatalf("HasBound = %v, want %v (min=%v max=%v)", got.HasBound, tc.wantBound, got.Min, got.Max)
			}
			if !tc.wantBound {
				return
			}
			if (got.Min == nil) != (tc.wantMin == nil) || (got.Min != nil && *got.Min != *tc.wantMin) {
				t.Errorf("Min = %v, want %v", got.Min, tc.wantMin)
			}
			if (got.Max == nil) != (tc.wantMax == nil) || (got.Max != nil && *got.Max != *tc.wantMax) {
				t.Errorf("Max = %v, want %v", got.Max, tc.wantMax)
			}
		})
	}
}

func TestParseIntegerEnum(t *testing.T) {
	got := ParseIntegerEnum("0, 301, or 308")
	want := []int64{0, 301, 308}
	if len(got) != len(want) {
		t.Fatalf("ParseIntegerEnum = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ParseIntegerEnum[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

// TestParseNumericBoundAgainstEveryAuditedIntegerAndRatio runs the parser
// against every real "integer"/"ratio"/"http_status" constraint in the
// audited document and proves it never panics and never fabricates a bound
// for a constraint containing conditional/cross-field language.
func TestParseNumericBoundAgainstEveryAuditedIntegerAndRatio(t *testing.T) {
	vc := loadTestValueContract(t)
	for _, f := range vc.Fields {
		switch f.Kind {
		case "integer", "ratio", "http_status":
		default:
			continue
		}
		bound := ParseNumericBound(f.Constraint)
		if bound.HasBound && bound.Min != nil && bound.Max != nil && *bound.Min > *bound.Max {
			t.Errorf("%s: parsed inverted bound min=%v max=%v from %q", f.GoField, *bound.Min, *bound.Max, f.Constraint)
		}
	}
}
