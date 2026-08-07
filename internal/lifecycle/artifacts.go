// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"jul/internal/config"
)

// ArtifactVersion is the schema version of the generated lifecycle artifacts.
// Bump it when the rendered shape changes so downstream tooling can detect it.
const ArtifactVersion = 2

// RegenerateCommand is printed by every check-mode failure so the remedy never
// has to be guessed.
const RegenerateCommand = "make lifecycle-generate"

const generatedBanner = "GENERATED FILE — DO NOT EDIT.\n" +
	"Source of truth: internal/lifecycle/registry.go (the Go lifecycle registry).\n" +
	"Regenerate with: " + RegenerateCommand + "\n" +
	"CI runs `make generated-check`, which fails when this file is stale."

// ArtifactPaths are the repository-relative paths of the generated artifacts,
// in generation order.
var ArtifactPaths = []string{
	"config-lifecycle.yaml",
	"generated/config-lifecycle.md",
	"generated/config-lifecycle.json",
}

// MetadataEntry is the machine projection of a registry entry. Field order is
// the declaration order, and it never carries a configured value.
type MetadataEntry struct {
	Path            string `json:"path"`
	Class           string `json:"class"`
	Subsystem       string `json:"subsystem"`
	Reason          string `json:"reason"`
	StartupConsumed bool   `json:"startup_consumed"`
	AddressKeyed    bool   `json:"address_keyed"`
	Conditional     bool   `json:"conditional"`
	Deprecated      bool   `json:"deprecated"`
	Ignored         bool   `json:"ignored"`
	Reserved        bool   `json:"reserved"`
	Secret          bool   `json:"secret_digested"`
}

// Metadata is the complete machine-readable projection of the registry. The
// JSON artifact and the generated Markdown reference are both rendered from it,
// so the artifacts cannot disagree.
type Metadata struct {
	Generated         string          `json:"_generated"`
	Version           int             `json:"version"`
	GeneratedBy       string          `json:"generated_by"`
	RegenerateCommand string          `json:"regenerate_command"`
	Classes           []MetadataNamed `json:"classes"`
	Subsystems        []MetadataNamed `json:"subsystems"`
	Counts            MetadataCounts  `json:"counts"`
	Conditions        []MetadataNamed `json:"conditions"`
	Fields            []MetadataEntry `json:"fields"`
	Exemptions        []MetadataNamed `json:"schema_exemptions"`
}

// MetadataNamed is a bounded identifier with its operator-facing description.
type MetadataNamed struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// MetadataCounts summarizes schema coverage.
type MetadataCounts struct {
	SchemaPaths     int            `json:"schema_paths"`
	SchemaLeaves    int            `json:"schema_leaves"`
	RegistryEntries int            `json:"registry_entries"`
	StartupConsumed int            `json:"startup_consumed"`
	ByClass         map[string]int `json:"by_class"`
}

// SchemaExemptions lists public schema leaves that are deliberately not given a
// lifecycle disposition, each with a specific source-backed justification.
//
// It is empty: every leaf of config.SchemaLeaves is classified. The map exists
// so a future exemption has to be written down and reviewed here instead of
// being hidden inside a checker script.
var SchemaExemptions = map[string]string{}

// BuildMetadata builds the machine-readable projection of the registry.
func BuildMetadata() Metadata {
	fields := make([]MetadataEntry, 0, len(Registry))
	startup := 0
	for _, e := range Registry {
		if e.StartupConsumed {
			startup++
		}
		fields = append(fields, MetadataEntry{
			Path:            e.Path,
			Class:           e.Class.String(),
			Subsystem:       string(e.Subsystem),
			Reason:          e.Reason,
			StartupConsumed: e.StartupConsumed,
			AddressKeyed:    e.AddressKeyed,
			Conditional:     e.Conditional,
			Deprecated:      e.Deprecated,
			Ignored:         e.Ignored,
			Reserved:        e.Reserved,
			Secret:          e.Secret,
		})
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })

	classes := make([]MetadataNamed, 0, len(classNames))
	byClass := make(map[string]int, len(classNames))
	counts := ClassCounts()
	for _, c := range AllClasses() {
		classes = append(classes, MetadataNamed{Name: c.String(), Description: c.Description()})
		byClass[c.String()] = counts[c]
	}

	subs := make([]MetadataNamed, 0, len(subsystemDescriptions))
	for name, desc := range subsystemDescriptions {
		subs = append(subs, MetadataNamed{Name: string(name), Description: desc})
	}
	sort.Slice(subs, func(i, j int) bool { return subs[i].Name < subs[j].Name })

	exemptions := make([]MetadataNamed, 0, len(SchemaExemptions))
	for _, name := range sortedExemptionPaths() {
		exemptions = append(exemptions, MetadataNamed{Name: name, Description: SchemaExemptions[name]})
	}

	return Metadata{
		Generated:         strings.ReplaceAll(generatedBanner, "\n", " "),
		Version:           ArtifactVersion,
		GeneratedBy:       "internal/lifecycle",
		RegenerateCommand: RegenerateCommand,
		Classes:           classes,
		Subsystems:        subs,
		Counts: MetadataCounts{
			SchemaPaths:     len(config.SchemaPaths()),
			SchemaLeaves:    len(config.SchemaLeaves()),
			RegistryEntries: len(Registry),
			StartupConsumed: startup,
			ByClass:         byClass,
		},
		Conditions: []MetadataNamed{
			{Name: "retained_listener", Description: detailRetainedListener},
			{Name: "new_listener_only", Description: detailNewListenerOnly},
			{Name: "listener_added_or_removed", Description: detailAddressAdded},
		},
		Fields:     fields,
		Exemptions: exemptions,
	}
}

func sortedExemptionPaths() []string {
	out := make([]string, 0, len(SchemaExemptions))
	for k := range SchemaExemptions {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// RenderJSON renders the compact machine-readable lifecycle metadata.
func RenderJSON() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(BuildMetadata()); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// RenderYAML renders docs/config-lifecycle.yaml.
func RenderYAML() ([]byte, error) {
	m := BuildMetadata()
	var b strings.Builder
	for _, line := range strings.Split(generatedBanner, "\n") {
		b.WriteString("# " + line + "\n")
	}
	b.WriteString("#\n")
	b.WriteString("# Every public TOML leaf appears exactly once. Editing this file has no effect\n")
	b.WriteString("# on runtime behavior; change internal/lifecycle/registry.go instead.\n")
	fmt.Fprintf(&b, "version: %d\n", m.Version)
	b.WriteString("generated_by: " + yamlString(m.GeneratedBy) + "\n")
	b.WriteString("regenerate_command: " + yamlString(m.RegenerateCommand) + "\n")

	b.WriteString("\nclasses:\n")
	for _, c := range m.Classes {
		b.WriteString("  " + c.Name + ": " + yamlString(c.Description) + "\n")
	}

	b.WriteString("\nsubsystems:\n")
	for _, s := range m.Subsystems {
		b.WriteString("  " + s.Name + ": " + yamlString(s.Description) + "\n")
	}

	b.WriteString("\ncounts:\n")
	fmt.Fprintf(&b, "  schema_paths: %d\n", m.Counts.SchemaPaths)
	fmt.Fprintf(&b, "  schema_leaves: %d\n", m.Counts.SchemaLeaves)
	fmt.Fprintf(&b, "  registry_entries: %d\n", m.Counts.RegistryEntries)
	fmt.Fprintf(&b, "  startup_consumed: %d\n", m.Counts.StartupConsumed)
	b.WriteString("  by_class:\n")
	for _, c := range AllClasses() {
		fmt.Fprintf(&b, "    %s: %d\n", c.String(), m.Counts.ByClass[c.String()])
	}

	b.WriteString("\nconditions:\n")
	for _, c := range m.Conditions {
		b.WriteString("  " + c.Name + ": " + yamlString(c.Description) + "\n")
	}

	b.WriteString("\nschema_exemptions:\n")
	if len(m.Exemptions) == 0 {
		b.WriteString("  {}\n")
	}
	for _, e := range m.Exemptions {
		b.WriteString("  " + yamlKey(e.Name) + ": " + yamlString(e.Description) + "\n")
	}

	b.WriteString("\nfields:\n")
	for _, f := range m.Fields {
		b.WriteString("  - path: " + yamlString(f.Path) + "\n")
		b.WriteString("    class: " + f.Class + "\n")
		b.WriteString("    subsystem: " + f.Subsystem + "\n")
		b.WriteString("    reason: " + yamlString(f.Reason) + "\n")
		writeYAMLFlag(&b, "startup_consumed", f.StartupConsumed)
		writeYAMLFlag(&b, "address_keyed", f.AddressKeyed)
		writeYAMLFlag(&b, "conditional", f.Conditional)
		writeYAMLFlag(&b, "deprecated", f.Deprecated)
		writeYAMLFlag(&b, "ignored", f.Ignored)
		writeYAMLFlag(&b, "reserved", f.Reserved)
		writeYAMLFlag(&b, "secret_digested", f.Secret)
	}
	return []byte(b.String()), nil
}

func writeYAMLFlag(b *strings.Builder, name string, v bool) {
	if v {
		b.WriteString("    " + name + ": true\n")
	}
}

// yamlKey quotes a mapping key that contains characters with YAML meaning.
func yamlKey(s string) string {
	if strings.ContainsAny(s, "*:#{}[]&!|>'\"%@` ") {
		return yamlString(s)
	}
	return s
}

// yamlString renders a double-quoted YAML scalar. JSON string escaping is a
// strict subset of YAML double-quoted escaping, so encoding/json produces a
// valid and deterministic result.
func yamlString(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(b)
}

// RenderMarkdown renders the human-readable generated lifecycle reference.
func RenderMarkdown() ([]byte, error) {
	m := BuildMetadata()
	var b strings.Builder
	b.WriteString("<!--\n")
	b.WriteString(generatedBanner + "\n")
	b.WriteString("-->\n\n")
	b.WriteString("# Configuration lifecycle reference\n\n")
	b.WriteString("Every public TOML configuration leaf and the disposition that governs it.\n")
	b.WriteString("The Go registry in `internal/lifecycle/registry.go` is the machine authority;\n")
	b.WriteString("this page, `docs/config-lifecycle.yaml` and `docs/generated/config-lifecycle.json`\n")
	b.WriteString("are deterministic renderings of it. Conceptual reload behavior is described in\n")
	b.WriteString("[reload-semantics.md](../reload-semantics.md).\n\n")

	b.WriteString("## Coverage\n\n")
	b.WriteString("| Measure | Count |\n| --- | --- |\n")
	fmt.Fprintf(&b, "| Schema paths (containers included) | %d |\n", m.Counts.SchemaPaths)
	fmt.Fprintf(&b, "| Schema leaves (configurable values) | %d |\n", m.Counts.SchemaLeaves)
	fmt.Fprintf(&b, "| Registry entries | %d |\n", m.Counts.RegistryEntries)
	fmt.Fprintf(&b, "| Startup-consumed entries | %d |\n", m.Counts.StartupConsumed)
	for _, c := range AllClasses() {
		fmt.Fprintf(&b, "| Class `%s` | %d |\n", c.String(), m.Counts.ByClass[c.String()])
	}

	b.WriteString("\n## Classes\n\n")
	b.WriteString("| Class | Meaning |\n| --- | --- |\n")
	for _, c := range m.Classes {
		fmt.Fprintf(&b, "| `%s` | %s |\n", c.Name, escapeCell(c.Description))
	}

	b.WriteString("\n## Conditional results\n\n")
	b.WriteString("A conditional entry is resolved by `lifecycle.Classify` against the live listener\n")
	b.WriteString("set, so preview and apply reach the same verdict.\n\n")
	b.WriteString("| Condition | Meaning |\n| --- | --- |\n")
	for _, c := range m.Conditions {
		fmt.Fprintf(&b, "| `%s` | %s |\n", c.Name, escapeCell(c.Description))
	}

	b.WriteString("\n## Subsystems\n\n")
	b.WriteString("| Subsystem | Scope |\n| --- | --- |\n")
	for _, s := range m.Subsystems {
		fmt.Fprintf(&b, "| `%s` | %s |\n", s.Name, escapeCell(s.Description))
	}

	if len(m.Exemptions) > 0 {
		b.WriteString("\n## Non-runtime exemptions\n\n")
		b.WriteString("| Path | Justification |\n| --- | --- |\n")
		for _, e := range m.Exemptions {
			fmt.Fprintf(&b, "| `%s` | %s |\n", e.Name, escapeCell(e.Description))
		}
	}

	b.WriteString("\n## Fields\n\n")
	b.WriteString("`startup` marks a path captured in the startup fingerprint. `cond.` marks a path\n")
	b.WriteString("whose disposition depends on the live listener set. `digest` marks a path whose\n")
	b.WriteString("value is compared as a digest so no secret material leaves the process.\n\n")
	b.WriteString("| Path | Class | Subsystem | Flags | Why |\n| --- | --- | --- | --- | --- |\n")
	for _, f := range m.Fields {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | %s |\n",
			f.Path, f.Class, f.Subsystem, flagList(f), escapeCell(f.Reason))
	}
	return []byte(b.String()), nil
}

func flagList(f MetadataEntry) string {
	var flags []string
	if f.StartupConsumed {
		flags = append(flags, "startup")
	}
	if f.AddressKeyed {
		flags = append(flags, "per-address")
	}
	if f.Conditional {
		flags = append(flags, "cond.")
	}
	if f.Deprecated {
		flags = append(flags, "deprecated")
	}
	if f.Ignored {
		flags = append(flags, "ignored")
	}
	if f.Reserved {
		flags = append(flags, "reserved")
	}
	if f.Secret {
		flags = append(flags, "digest")
	}
	if len(flags) == 0 {
		return "—"
	}
	return strings.Join(flags, ", ")
}

// escapeCell makes a sentence safe inside a Markdown table cell.
func escapeCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}

// RenderArtifacts renders every generated artifact keyed by its
// repository-relative path under docs/.
func RenderArtifacts() (map[string][]byte, error) {
	yamlBytes, err := RenderYAML()
	if err != nil {
		return nil, err
	}
	mdBytes, err := RenderMarkdown()
	if err != nil {
		return nil, err
	}
	jsonBytes, err := RenderJSON()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		ArtifactPaths[0]: yamlBytes,
		ArtifactPaths[1]: mdBytes,
		ArtifactPaths[2]: jsonBytes,
	}, nil
}
