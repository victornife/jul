// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package configcontract builds one normalized model of Jul's public
// configuration surface, joining the authorities ADR 0019 §19/§21 names:
// config.SchemaPaths/SchemaLeaves (structure), lifecycle.BuildMetadata
// (disposition), docs/config-value-contract.json (value constraints), a
// small capability registry (build-tag requirements), a small resource
// catalog (identity), and a small description table (mostly derived from
// existing Go doc comments). The three generated artifacts
// (docs/generated/config.schema.json, config-metadata.json,
// config-reference.md) are all rendered from this one model, so they cannot
// disagree because three separate code paths interpreted the sources
// differently.
//
// This package never re-walks internal/config.Config. config.SchemaPaths and
// config.SchemaLeaves remain the only schema-reflection implementation in the
// repository.
package configcontract

//go:generate go run ./configcontractgen -out ../../docs -value-contract ../../docs/config-value-contract.json -schema-go ../config/schema.go

import (
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

// Sources bundles the externally loaded inputs Build joins against the schema
// inventory. Loading is the generator's (or a test's) job; Build performs no
// file I/O so it stays trivially testable.
type Sources struct {
	ValueContract ValueContract
	Comments      map[string]string
}

// Field is the normalized, per-leaf record every renderer reads.
type Field struct {
	Path        string
	Kind        config.PathKind
	Scalar      config.ScalarKind
	GoType      string
	Optional    bool
	Dynamic     bool
	TextScalar  bool
	Description string
	Anchor      string
	// Default is the documented default, when one is recorded in
	// DefaultOverrides, converted to a properly JSON-typed value (bool,
	// number, string, or array) against this leaf's own Scalar/Kind. It is
	// annotation only: no renderer materializes it into a submitted
	// document, and it is kept distinct from ZeroSemantics.
	Default    any
	HasDefault bool
	// ConditionalDefault is the documented default's human-readable text when
	// the default depends on another field (e.g. "true (when
	// admin.enabled)"), from ConditionalDefaultOverrides. It is mutually
	// exclusive with Default/HasDefault and is NEVER rendered as a JSON
	// Schema `default` — an unconditional default would misdescribe it.
	ConditionalDefault string

	// Lifecycle (ADR 0019 §19, from lifecycle.BuildMetadata; every leaf has
	// exactly one entry, enforced by lifecycle's own closed-world tests and
	// re-asserted in Build).
	Class           string
	Subsystem       string
	Reason          string
	StartupConsumed bool
	AddressKeyed    bool
	CollectionKeyed bool
	Conditional     bool
	Deprecated      bool
	Ignored         bool
	Reserved        bool
	Secret          bool

	// Capability requirement, if any (usually zero or one entry).
	Capabilities []Capability
	// ValueCapabilities maps a specific accepted value to the capability it
	// requires, for a field whose presence is unconditional but one of whose
	// values gates a build tag (e.g. compression.encoders' "br"/"zstd").
	ValueCapabilities map[string]Capability

	// Value contract (ADR 0019 §21), present only when an audited entry
	// resolves to this path.
	HasValueContract bool
	ValueKind        string
	Constraint       string
	ZeroSemantics    string
	ActiveWhen       string
	Allowed          []string
	NumericBound     NumericBound
	IntegerEnum      []int64
}

// Contract is the one normalized model every generated artifact renders from.
type Contract struct {
	Leaves    []Field
	Resources []Resource
}

// Build joins every authority into one normalized Contract, failing loudly
// (returning an error) when a cross-authority invariant breaks, per ADR 0019
// §21/§149's completion bar. It performs no file I/O; call LoadValueContract
// and LoadFieldComments to build Sources first.
func Build(src Sources) (Contract, error) {
	leaves := config.SchemaLeaves()
	allPaths := config.SchemaPaths()

	schemaLeafSet := make(map[string]bool, len(leaves))
	for _, p := range leaves {
		schemaLeafSet[p.Path] = true
	}
	schemaPathSet := make(map[string]config.SchemaPath, len(allPaths))
	for _, p := range allPaths {
		if _, dup := schemaPathSet[p.Path]; dup {
			return Contract{}, fmt.Errorf("configcontract: duplicate canonical schema path %q", p.Path)
		}
		schemaPathSet[p.Path] = p
	}

	// Join the value contract: every canonical path an audited entry names
	// must resolve, and no two entries may claim the same path.
	valueByPath := make(map[string]ValueContractField, len(src.ValueContract.Fields))
	for _, f := range src.ValueContract.Fields {
		for _, p := range f.CanonicalPaths() {
			if _, ok := schemaPathSet[p]; !ok {
				return Contract{}, fmt.Errorf("configcontract: value-contract entry %s: canonical path %q does not resolve against config.SchemaPaths()", f.GoField, p)
			}
			if prev, dup := valueByPath[p]; dup {
				return Contract{}, fmt.Errorf("configcontract: value-contract path %q is claimed by both %s and %s", p, prev.GoField, f.GoField)
			}
			valueByPath[p] = f
		}
	}

	// Join the capability registry: every prefix must resolve.
	for _, e := range CapabilityRegistry {
		if _, ok := schemaPathSet[e.PathPrefix]; !ok {
			return Contract{}, fmt.Errorf("configcontract: capability %s: PathPrefix %q does not resolve against config.SchemaPaths()", e.Capability, e.PathPrefix)
		}
	}

	// Join the value-capability registry: every path must resolve against a
	// real schema leaf, and every capability must have a known build tag.
	for _, e := range ValueCapabilityRegistry {
		if !schemaLeafSet[e.Path] {
			return Contract{}, fmt.Errorf("configcontract: value-capability %s: path %q does not resolve against config.SchemaLeaves()", e.Capability, e.Path)
		}
		if _, ok := CapabilityBuildTag[e.Capability]; !ok {
			return Contract{}, fmt.Errorf("configcontract: value-capability %s: no build tag registered in CapabilityBuildTag", e.Capability)
		}
	}
	for _, e := range CapabilityRegistry {
		if _, ok := CapabilityBuildTag[e.Capability]; !ok {
			return Contract{}, fmt.Errorf("configcontract: capability %s: no build tag registered in CapabilityBuildTag", e.Capability)
		}
	}

	// Join description overrides: every key must resolve to a real leaf.
	for path := range DescriptionOverrides {
		if !schemaLeafSet[path] {
			return Contract{}, fmt.Errorf("configcontract: DescriptionOverrides entry %q does not resolve against config.SchemaLeaves()", path)
		}
	}

	// Join documented defaults: every key must resolve to a real leaf, in
	// EXACTLY one of DefaultOverrides (unconditional) or
	// ConditionalDefaultOverrides (conditional) — never both, since an
	// unconditional JSON Schema `default` would misdescribe a conditional
	// field.
	for path := range DefaultOverrides {
		if !schemaLeafSet[path] {
			return Contract{}, fmt.Errorf("configcontract: DefaultOverrides entry %q does not resolve against config.SchemaLeaves()", path)
		}
		if _, ok := ConditionalDefaultOverrides[path]; ok {
			return Contract{}, fmt.Errorf("configcontract: %q is in both DefaultOverrides and ConditionalDefaultOverrides", path)
		}
	}
	for path := range ConditionalDefaultOverrides {
		if !schemaLeafSet[path] {
			return Contract{}, fmt.Errorf("configcontract: ConditionalDefaultOverrides entry %q does not resolve against config.SchemaLeaves()", path)
		}
	}

	// Join the resource catalog: every CollectionElementPath must resolve as
	// a dynamic collection, and every identity field must resolve against it.
	seenKind := make(map[string]bool, len(ResourceCatalog))
	for _, r := range ResourceCatalog {
		if seenKind[r.Kind] {
			return Contract{}, fmt.Errorf("configcontract: duplicate resource kind %q", r.Kind)
		}
		seenKind[r.Kind] = true

		elem := r.CollectionElementPath()
		p, ok := schemaPathSet[elem]
		if !ok {
			return Contract{}, fmt.Errorf("configcontract: resource %s: CollectionElementPath %q does not resolve against config.SchemaPaths()", r.Kind, elem)
		}
		if p.Structure != config.StructArrayTable && p.Structure != config.StructMapTable {
			return Contract{}, fmt.Errorf("configcontract: resource %s: %q is not a dynamic collection (Structure=%q)", r.Kind, elem, p.Structure)
		}
		for _, field := range r.IdentityFields {
			if field == keyIsIdentity {
				continue
			}
			full := r.CollectionPath + "." + field
			if _, ok := schemaPathSet[full]; !ok {
				return Contract{}, fmt.Errorf("configcontract: resource %s: identity field %q (%s) does not resolve against config.SchemaPaths()", r.Kind, field, full)
			}
		}
	}

	// Build one Field per schema leaf, joining lifecycle (mandatory: every
	// leaf must have exactly one entry) and the optional sources above.
	fields := make([]Field, 0, len(leaves))
	for _, p := range leaves {
		entry, ok := lifecycle.Lookup(p.Path)
		if !ok {
			return Contract{}, fmt.Errorf("configcontract: schema leaf %q has no lifecycle disposition", p.Path)
		}
		desc, ok := DescribeLeaf(src.Comments, p)
		if !ok {
			return Contract{}, fmt.Errorf("configcontract: schema leaf %q has no description (add a Go doc comment or a DescriptionOverrides entry)", p.Path)
		}

		f := Field{
			Path:              p.Path,
			Kind:              p.Kind,
			Scalar:            p.Scalar,
			GoType:            p.GoType,
			Optional:          p.Optional,
			Dynamic:           p.Dynamic,
			TextScalar:        p.TextScalar,
			Description:       desc,
			Anchor:            anchorFor(p.Path),
			Class:             entry.Class.String(),
			Subsystem:         string(entry.Subsystem),
			Reason:            entry.Reason,
			StartupConsumed:   entry.StartupConsumed,
			AddressKeyed:      entry.AddressKeyed,
			CollectionKeyed:   entry.CollectionKeyed,
			Conditional:       entry.Conditional,
			Deprecated:        entry.Deprecated,
			Ignored:           entry.Ignored,
			Reserved:          entry.Reserved,
			Secret:            entry.Secret,
			Capabilities:      CapabilitiesFor(p.Path),
			ValueCapabilities: ValueCapabilitiesFor(p.Path),
		}
		if raw, ok := DefaultFor(p.Path); ok {
			typed, err := convertDefaultValue(p.Kind, p.Scalar, raw)
			if err != nil {
				return Contract{}, fmt.Errorf("configcontract: DefaultOverrides entry %q: %w", p.Path, err)
			}
			f.Default = typed
			f.HasDefault = true
		}
		if text, ok := ConditionalDefaultFor(p.Path); ok {
			f.ConditionalDefault = text
		}
		if vc, ok := valueByPath[p.Path]; ok {
			f.HasValueContract = true
			f.ValueKind = vc.Kind
			f.Constraint = vc.Constraint
			f.ZeroSemantics = vc.ZeroSemantics
			f.ActiveWhen = vc.ActiveWhen
			f.Allowed = vc.Allowed
			switch vc.Kind {
			case "integer", "ratio", "http_status":
				f.NumericBound = ParseNumericBound(vc.Constraint)
			case "integer_enum":
				f.IntegerEnum = ParseIntegerEnum(vc.Constraint)
			}
		}
		fields = append(fields, f)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })

	resources := make([]Resource, len(ResourceCatalog))
	copy(resources, ResourceCatalog)

	return Contract{Leaves: fields, Resources: resources}, nil
}

// anchorFor derives a stable, deterministic Markdown anchor from a canonical
// path.
func anchorFor(path string) string {
	r := strings.NewReplacer(".", "-", "*", "x")
	return r.Replace(path)
}
