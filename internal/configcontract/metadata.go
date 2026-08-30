// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"bytes"
	"encoding/json"
	"strings"

	"jul/internal/config"
)

// ContractVersion is the schema version of the generated configuration
// contract artifacts (config.schema.json, config-metadata.json,
// config-reference.md). Bump it when their rendered shape changes so
// downstream tooling (including the future #150 capabilities endpoint) can
// detect it without parsing the generated files.
const ContractVersion = 1

// RegenerateCommand is printed by every check-mode failure.
const RegenerateCommand = "make config-contract-generate"

const generatedBanner = "GENERATED FILE — DO NOT EDIT.\n" +
	"Source of truth: internal/configcontract (config.SchemaPaths + lifecycle.BuildMetadata +\n" +
	"docs/config-value-contract.json + the capability/resource/description tables in this package).\n" +
	"Regenerate with: " + RegenerateCommand + "\n" +
	"CI runs `make generated-check`, which fails when this file is stale."

// ArtifactPaths are the repository-relative paths (under docs/) of the
// generated artifacts, in generation order.
var ArtifactPaths = []string{
	"generated/config.schema.json",
	"generated/config-metadata.json",
	"generated/config-reference.md",
}

// RenderArtifacts renders every generated artifact keyed by its
// repository-relative path under docs/.
func RenderArtifacts(c Contract) (map[string][]byte, error) {
	schemaBytes, err := RenderSchema(c)
	if err != nil {
		return nil, err
	}
	metadataBytes, err := RenderMetadataJSON(c)
	if err != nil {
		return nil, err
	}
	referenceBytes, err := RenderReferenceMarkdown(c)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		ArtifactPaths[0]: schemaBytes,
		ArtifactPaths[1]: metadataBytes,
		ArtifactPaths[2]: referenceBytes,
	}, nil
}

// MetadataEntry is the machine projection of one Field. It never carries a
// configured value, a resolved secret, a local path or a timestamp — every
// source is static Go/doc/audited metadata, never a read configuration
// document.
type MetadataEntry struct {
	Kind            string       `json:"kind"`
	Scalar          string       `json:"scalar"`
	GoType          string       `json:"go_type"`
	Optional        bool         `json:"optional"`
	Dynamic         bool         `json:"dynamic"`
	Subsystem       string       `json:"subsystem"`
	LifecycleClass  string       `json:"lifecycle_class"`
	LifecycleReason string       `json:"lifecycle_reason"`
	StartupConsumed bool         `json:"startup_consumed,omitempty"`
	AddressKeyed    bool         `json:"address_keyed,omitempty"`
	CollectionKeyed bool         `json:"collection_keyed,omitempty"`
	Conditional     bool         `json:"conditional,omitempty"`
	Deprecated      bool         `json:"deprecated,omitempty"`
	Ignored         bool         `json:"ignored,omitempty"`
	Reserved        bool         `json:"reserved,omitempty"`
	Secret          bool         `json:"secret,omitempty"`
	Capabilities    []Capability `json:"capabilities,omitempty"`
	// ValueCapabilities maps a specific accepted value to the capability it
	// requires (e.g. compression.encoders' "br"->"brotli"); most fields have
	// none, since presence rather than a particular value gates a build tag.
	ValueCapabilities map[string]Capability `json:"value_capabilities,omitempty"`
	ValueKind         string                `json:"value_kind,omitempty"`
	Constraint        string                `json:"constraint,omitempty"`
	ZeroSemantics     string                `json:"zero_semantics,omitempty"`
	ActiveWhen        string                `json:"active_when,omitempty"`
	Allowed           []string              `json:"allowed,omitempty"`
	IntegerEnum       []int64               `json:"integer_enum,omitempty"`
	// Default is the documented default, present only when Config lists one
	// in DefaultOverrides; distinct from ZeroSemantics.
	Default     string `json:"default,omitempty"`
	Description string `json:"description"`
	Anchor      string `json:"anchor"`
}

// ResourceEntry is the machine projection of one catalog Resource.
type ResourceEntry struct {
	Kind            string   `json:"kind"`
	CollectionPath  string   `json:"collection_path"`
	IdentityClass   string   `json:"identity_class"`
	IdentityFields  []string `json:"identity_fields,omitempty"`
	UniquenessScope string   `json:"uniqueness_scope"`
	Required        bool     `json:"required"`
	Renameable      bool     `json:"renameable"`
	ExternalPath    string   `json:"external_path,omitempty"`
}

// MetadataCounts summarizes schema coverage, derived at render time rather
// than hardcoded.
type MetadataCounts struct {
	SchemaPaths  int `json:"schema_paths"`
	SchemaLeaves int `json:"schema_leaves"`
	Resources    int `json:"resources"`
}

// ContractMetadata is the complete machine-readable projection of Contract.
// config-metadata.json is a direct rendering of it, keyed by canonical path
// so lookup never depends on array position.
type ContractMetadata struct {
	Generated         string         `json:"_generated"`
	Version           int            `json:"version"`
	GeneratedBy       string         `json:"generated_by"`
	RegenerateCommand string         `json:"regenerate_command"`
	Counts            MetadataCounts `json:"counts"`
	// CapabilityBuildTags maps every logical capability name used in
	// "capabilities"/"value_capabilities" to the actual Go build tag that
	// compiles it in (Makefile's FULL_TAGS) — most already match, but
	// stream_proxy/stream and wasm_plugins/wasmplugins differ.
	CapabilityBuildTags map[string]string        `json:"capability_build_tags"`
	Fields              map[string]MetadataEntry `json:"fields"`
	Resources           []ResourceEntry          `json:"resources"`
}

// BuildContractMetadata projects c into the shape config-metadata.json
// renders.
func BuildContractMetadata(c Contract) ContractMetadata {
	fields := make(map[string]MetadataEntry, len(c.Leaves))
	for _, f := range c.Leaves {
		fields[f.Path] = MetadataEntry{
			Kind:              string(f.Kind),
			Scalar:            string(f.Scalar),
			GoType:            f.GoType,
			Optional:          f.Optional,
			Dynamic:           f.Dynamic,
			Subsystem:         f.Subsystem,
			LifecycleClass:    f.Class,
			LifecycleReason:   f.Reason,
			StartupConsumed:   f.StartupConsumed,
			AddressKeyed:      f.AddressKeyed,
			CollectionKeyed:   f.CollectionKeyed,
			Conditional:       f.Conditional,
			Deprecated:        f.Deprecated,
			Ignored:           f.Ignored,
			Reserved:          f.Reserved,
			Secret:            f.Secret,
			Capabilities:      f.Capabilities,
			ValueCapabilities: f.ValueCapabilities,
			ValueKind:         f.ValueKind,
			Constraint:        f.Constraint,
			ZeroSemantics:     f.ZeroSemantics,
			ActiveWhen:        f.ActiveWhen,
			Allowed:           f.Allowed,
			IntegerEnum:       f.IntegerEnum,
			Default:           f.Default,
			Description:       f.Description,
			Anchor:            f.Anchor,
		}
	}

	resources := make([]ResourceEntry, 0, len(c.Resources))
	for _, r := range c.Resources {
		resources = append(resources, ResourceEntry{
			Kind:            r.Kind,
			CollectionPath:  r.CollectionPath,
			IdentityClass:   string(r.IdentityClass),
			IdentityFields:  r.IdentityFields,
			UniquenessScope: r.UniquenessScope,
			Required:        r.Required,
			Renameable:      r.Renameable,
			ExternalPath:    r.ExternalPath,
		})
	}

	return ContractMetadata{
		Generated:         strings.ReplaceAll(generatedBanner, "\n", " "),
		Version:           ContractVersion,
		GeneratedBy:       "internal/configcontract",
		RegenerateCommand: RegenerateCommand,
		Counts: MetadataCounts{
			SchemaPaths:  len(config.SchemaPaths()),
			SchemaLeaves: len(config.SchemaLeaves()),
			Resources:    len(resources),
		},
		CapabilityBuildTags: buildCapabilityBuildTags(),
		Fields:              fields,
		Resources:           resources,
	}
}

// buildCapabilityBuildTags projects CapabilityBuildTag with string keys for
// JSON (Capability is already a string type, but the map's declared key type
// must be converted explicitly for encoding/json to emit plain string keys).
func buildCapabilityBuildTags() map[string]string {
	out := make(map[string]string, len(CapabilityBuildTag))
	for cap, tag := range CapabilityBuildTag {
		out[string(cap)] = tag
	}
	return out
}

// RenderMetadataJSON renders docs/generated/config-metadata.json.
func RenderMetadataJSON(c Contract) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(BuildContractMetadata(c)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
