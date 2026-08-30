// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"reflect"
	"testing"

	"jul/internal/config"
)

func loadTestSources(t *testing.T) Sources {
	t.Helper()
	return Sources{
		ValueContract: loadTestValueContract(t),
		Comments:      loadTestFieldComments(t),
	}
}

// TestBuildSucceeds proves the normalized model builds cleanly against the
// real repository sources, with no invariant violations.
func TestBuildSucceeds(t *testing.T) {
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(c.Leaves) != len(config.SchemaLeaves()) {
		t.Errorf("len(Leaves) = %d, want %d (one per schema leaf)", len(c.Leaves), len(config.SchemaLeaves()))
	}
	if len(c.Resources) != len(ResourceCatalog) {
		t.Errorf("len(Resources) = %d, want %d", len(c.Resources), len(ResourceCatalog))
	}
}

// TestBuildEveryLeafAppearsExactlyOnce proves no duplicate and no missing
// path in the rendered model.
func TestBuildEveryLeafAppearsExactlyOnce(t *testing.T) {
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	seen := map[string]int{}
	for _, f := range c.Leaves {
		seen[f.Path]++
	}
	for _, p := range config.SchemaLeaves() {
		if seen[p.Path] != 1 {
			t.Errorf("leaf %q appears %d times in the model, want 1", p.Path, seen[p.Path])
		}
	}
}

// TestBuildIsDeterministic proves repeated builds are byte-for-byte
// identical in content and order.
func TestBuildIsDeterministic(t *testing.T) {
	src := loadTestSources(t)
	first, err := Build(src)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for i := 0; i < 3; i++ {
		next, err := Build(src)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if len(first.Leaves) != len(next.Leaves) {
			t.Fatalf("leaf count changed: %d then %d", len(first.Leaves), len(next.Leaves))
		}
		for j := range first.Leaves {
			if !reflect.DeepEqual(first.Leaves[j], next.Leaves[j]) {
				t.Fatalf("leaf %d changed:\n%+v\n%+v", j, first.Leaves[j], next.Leaves[j])
			}
		}
	}
}

// TestBuildValueContractJoin spot-checks that a known value-contract entry's
// constraint data lands on the right Field.
func TestBuildValueContractJoin(t *testing.T) {
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	byPath := map[string]Field{}
	for _, f := range c.Leaves {
		byPath[f.Path] = f
	}
	f, ok := byPath["compression.level"]
	if !ok {
		t.Fatal("compression.level missing from model")
	}
	if !f.HasValueContract {
		t.Fatal("compression.level should have a value-contract entry")
	}
	if f.NumericBound.Min == nil || *f.NumericBound.Min != 0 || f.NumericBound.Max == nil || *f.NumericBound.Max != 11 {
		t.Errorf("compression.level NumericBound = %+v, want min=0 max=11", f.NumericBound)
	}

	// waf.paranoia must NOT get a wrong mechanical bound (the trap case).
	pf, ok := byPath["waf.paranoia"]
	if !ok {
		t.Fatal("waf.paranoia missing from model")
	}
	if pf.NumericBound.HasBound {
		t.Errorf("waf.paranoia should not have a mechanical bound, got %+v", pf.NumericBound)
	}
}

// TestBuildCapabilityJoin proves a build-tag-gated leaf carries its
// capability requirement.
func TestBuildCapabilityJoin(t *testing.T) {
	c, err := Build(loadTestSources(t))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, f := range c.Leaves {
		if f.Path != "servers.*.tls.acme.ca" {
			continue
		}
		found := false
		for _, cap := range f.Capabilities {
			if cap == CapACME {
				found = true
			}
		}
		if !found {
			t.Errorf("servers.*.tls.acme.ca Capabilities = %v, want to include %q", f.Capabilities, CapACME)
		}
		return
	}
	t.Fatal("servers.*.tls.acme.ca missing from model")
}

// TestBuildRejectsUnresolvableValueContractPath proves the join fails loudly
// rather than silently ignoring a stale/mistyped audited path.
func TestBuildRejectsUnresolvableValueContractPath(t *testing.T) {
	src := loadTestSources(t)
	src.ValueContract.Fields = append(src.ValueContract.Fields, ValueContractField{
		GoField: "Bogus.Field",
		RawPath: "this.path.does.not.exist",
		Kind:    "integer",
	})
	if _, err := Build(src); err == nil {
		t.Fatal("Build should fail on an unresolvable value-contract path")
	}
}

// TestBuildRejectsUnresolvableCapabilityPrefix and description-override
// paths are exercised the same way to prove Build fails loudly rather than
// silently accepting a stale entry; DescriptionOverrides is a package-level
// var, so this test mutates and restores it.
func TestBuildRejectsUnresolvableDescriptionOverride(t *testing.T) {
	DescriptionOverrides["this.path.does.not.exist"] = "bogus"
	defer delete(DescriptionOverrides, "this.path.does.not.exist")
	if _, err := Build(loadTestSources(t)); err == nil {
		t.Fatal("Build should fail on an unresolvable DescriptionOverrides path")
	}
}
