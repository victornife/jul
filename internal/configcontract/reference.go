// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"fmt"
	"sort"
	"strings"
)

// RenderReferenceMarkdown renders docs/generated/config-reference.md: an
// exhaustive factual reference over the same normalized Contract the JSON
// Schema and metadata artifacts render from, ordered by configuration
// hierarchy (path order) rather than map iteration.
func RenderReferenceMarkdown(c Contract) ([]byte, error) {
	fields := make([]Field, len(c.Leaves))
	copy(fields, c.Leaves)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })

	var b strings.Builder
	b.WriteString("<!--\n")
	b.WriteString(generatedBanner + "\n")
	b.WriteString("-->\n\n")
	b.WriteString("# Configuration reference\n\n")
	b.WriteString("Every public configurable TOML leaf. The normalized contract in\n")
	b.WriteString("`internal/configcontract` is the source of truth; this page,\n")
	b.WriteString("[`config.schema.json`](config.schema.json) and\n")
	b.WriteString("[`config-metadata.json`](config-metadata.json) are deterministic renderings of\n")
	b.WriteString("it. Conceptual explanations, operating guidance and examples stay in\n")
	b.WriteString("[configuration.md](../configuration.md).\n\n")
	b.WriteString("> Schema validity is necessary and not sufficient; Jul's runtime\n")
	b.WriteString("> configuration validation (`jul check`) remains authoritative. A document may\n")
	b.WriteString("> satisfy the generated JSON Schema and still fail `jul check`. A configuration\n")
	b.WriteString("> may pass `jul check` while `jul lint` reports an error-severity finding —\n")
	b.WriteString("> lint policy is never converted into structural invalidity.\n\n")

	fmt.Fprintf(&b, "Coverage: %d configurable leaves.\n\n", len(fields))

	for _, f := range fields {
		fmt.Fprintf(&b, "## `%s` {#%s}\n\n", f.Path, f.Anchor)
		if f.Description != "" {
			fmt.Fprintf(&b, "%s.\n\n", strings.TrimSuffix(f.Description, "."))
		}
		b.WriteString("| | |\n| --- | --- |\n")
		fmt.Fprintf(&b, "| Type | %s |\n", typeCell(f))
		if f.Optional {
			b.WriteString("| Optional | yes |\n")
		}
		fmt.Fprintf(&b, "| Lifecycle | `%s` |\n", f.Class)
		fmt.Fprintf(&b, "| Subsystem | `%s` |\n", f.Subsystem)
		if f.Reason != "" {
			fmt.Fprintf(&b, "| Why | %s |\n", escapeCell(f.Reason))
		}
		if len(f.Capabilities) > 0 {
			fmt.Fprintf(&b, "| Requires | %s |\n", capabilityCell(f.Capabilities))
		}
		if flags := flagCell(f); flags != "" {
			fmt.Fprintf(&b, "| Flags | %s |\n", flags)
		}
		if f.HasValueContract {
			if len(f.Allowed) > 0 {
				fmt.Fprintf(&b, "| Allowed values | %s |\n", allowedCell(f.Allowed))
			}
			if len(f.IntegerEnum) > 0 {
				fmt.Fprintf(&b, "| Allowed values | %s |\n", integerEnumCell(f.IntegerEnum))
			}
			if f.Constraint != "" {
				fmt.Fprintf(&b, "| Constraint | %s |\n", escapeCell(f.Constraint))
			}
			if f.ZeroSemantics != "" {
				fmt.Fprintf(&b, "| Zero/empty semantics | %s |\n", escapeCell(f.ZeroSemantics))
			}
			if f.ActiveWhen != "" {
				fmt.Fprintf(&b, "| Active when | %s |\n", escapeCell(f.ActiveWhen))
			}
		}
		b.WriteString("\n")
	}

	return []byte(b.String()), nil
}

func typeCell(f Field) string {
	if f.Kind == "list" {
		return fmt.Sprintf("list of `%s`", f.Scalar)
	}
	return fmt.Sprintf("`%s`", f.Scalar)
}

func capabilityCell(caps []Capability) string {
	names := make([]string, len(caps))
	for i, c := range caps {
		names[i] = "`" + string(c) + "`"
	}
	return strings.Join(names, ", ")
}

func flagCell(f Field) string {
	var flags []string
	if f.StartupConsumed {
		flags = append(flags, "startup-consumed")
	}
	if f.AddressKeyed {
		flags = append(flags, "per-address")
	}
	if f.CollectionKeyed {
		flags = append(flags, "per-collection-element")
	}
	if f.Conditional {
		flags = append(flags, "conditional")
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
		flags = append(flags, "secret")
	}
	return strings.Join(flags, ", ")
}

func allowedCell(allowed []string) string {
	quoted := make([]string, len(allowed))
	for i, a := range allowed {
		quoted[i] = "`" + a + "`"
	}
	return strings.Join(quoted, ", ")
}

func integerEnumCell(values []int64) string {
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf("`%d`", v)
	}
	return strings.Join(parts, ", ")
}

// escapeCell makes a sentence safe inside a Markdown table cell.
func escapeCell(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\\|"), "\n", " ")
}
