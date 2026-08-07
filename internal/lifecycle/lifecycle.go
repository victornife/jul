// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package lifecycle is the single machine authority for how every public
// configuration field behaves across a reload.
//
// The Go registry in registry.go classifies each leaf of the TOML schema
// (config.SchemaLeaves) exactly once. The runtime — reload gating, admin
// preflight, planned restart, diff projections and the configuration preview
// API — reads classification only from this package; it never parses
// docs/config-lifecycle.yaml, generated Markdown or generated JSON. Those
// artifacts are deterministic renderings of this registry produced by
// `make lifecycle-generate` and verified by `make generated-check`.
//
// The world is closed: TestRegistryCoversEverySchemaLeaf fails when a schema
// leaf has no disposition, and Lookup reports absence instead of assuming a
// new field is safe to hot reload.
package lifecycle

import (
	"fmt"
	"strings"
)

// Class categorizes how a configuration field may be changed.
type Class int

const (
	// HotReloadClass means the field takes effect on the next successful
	// configuration reload without restarting the process.
	HotReloadClass Class = iota
	// RestartRequiredClass means the field is consumed while the process starts
	// and cannot change without restarting it.
	RestartRequiredClass
	// NewListenerOnlyClass means the value is frozen when a socket binds: a new
	// listen address picks it up immediately, while an address that survives the
	// reload keeps the value it bound with until the process restarts.
	NewListenerOnlyClass
	// IgnoredDeprecatedClass means the field stays parseable for v1
	// compatibility but no runtime consumer reads it. Changing it never creates
	// a pending restart and preview must not claim it was applied.
	IgnoredDeprecatedClass
	// ValidationRejectedReservedClass means the field names a reserved seam that
	// configuration validation rejects today, so no running process can ever
	// have consumed it.
	ValidationRejectedReservedClass
)

// classNames is the closed set of class identifiers rendered into generated
// artifacts and API responses.
var classNames = map[Class]string{
	HotReloadClass:                  "hot_reload",
	RestartRequiredClass:            "restart_required",
	NewListenerOnlyClass:            "new_listener_only",
	IgnoredDeprecatedClass:          "ignored_deprecated",
	ValidationRejectedReservedClass: "validation_rejected_reserved",
}

// AllClasses returns every class in declaration order.
func AllClasses() []Class {
	return []Class{
		HotReloadClass,
		RestartRequiredClass,
		NewListenerOnlyClass,
		IgnoredDeprecatedClass,
		ValidationRejectedReservedClass,
	}
}

func (c Class) String() string {
	if s, ok := classNames[c]; ok {
		return s
	}
	return fmt.Sprintf("lifecycle.Class(%d)", int(c))
}

// Description explains the class in operator-facing terms. Generated references
// render it verbatim, so it must stay free of configured values.
func (c Class) Description() string {
	switch c {
	case HotReloadClass:
		return "Takes effect on the next successful configuration reload; no process restart is needed."
	case RestartRequiredClass:
		return "Consumed while the process starts. A change is persisted and reported, but the running process keeps the startup value until it restarts."
	case NewListenerOnlyClass:
		return "Frozen when a socket binds. A newly added listen address adopts the value immediately; an address kept across the reload keeps the value it bound with until the process restarts."
	case IgnoredDeprecatedClass:
		return "Parsed for compatibility but read by no runtime consumer. Changing it has no runtime effect and never creates a pending restart."
	case ValidationRejectedReservedClass:
		return "A reserved seam that configuration validation rejects today, so no running process can have consumed it."
	default:
		return ""
	}
}

// Subsystem is a bounded identifier grouping paths that share an operational
// owner. The set is closed (see SubsystemDescription) so metadata, metrics
// labels and generated documentation stay low-cardinality and reviewable.
type Subsystem string

// Entry is the complete disposition of one configuration path.
type Entry struct {
	// Path is the canonical TOML path with "*" for every dynamic collection
	// key, e.g. "servers.*.tls.client_auth.ca_file".
	Path string
	// Class is how the field may be changed at runtime.
	Class Class
	// Subsystem groups the path with its operational owner.
	Subsystem Subsystem
	// Reason states, in one sentence, why the path has this class.
	Reason string
	// StartupConsumed is true when the effective value is captured in the
	// startup fingerprint and compared on every reload.
	StartupConsumed bool
	// AddressKeyed is true when the fingerprint value is a map keyed by listen
	// address, so adding or removing a listener does not produce a false
	// restart-required verdict for addresses that were not touched.
	AddressKeyed bool
	// Deprecated marks a field that is superseded by another path.
	Deprecated bool
	// Ignored marks a field that no runtime consumer reads.
	Ignored bool
	// Reserved marks a field that configuration validation rejects today.
	Reserved bool
	// Conditional marks a field whose effective disposition depends on the live
	// listener set. Classify resolves it against a concrete transition.
	Conditional bool
	// Secret marks a field whose value must never leave the process: the
	// extractor emits a digest instead of the value.
	Secret bool
}

// registryIndex is the exact-path index of Registry, built once at init.
var registryIndex map[string]int

func init() {
	registryIndex = make(map[string]int, len(Registry))
	for i := range Registry {
		registryIndex[Registry[i].Path] = i
	}
}

// Lookup returns the entry that governs path. The second result reports whether
// a disposition exists: an unregistered path is never assumed to be hot
// reloadable, because a newly added startup-consumed field would then be
// written to disk while the process keeps serving the old value.
//
// path may be canonical ("servers.*.tls.cert") or concrete
// ("servers.0.tls.cert"); a concrete path matches the canonical entry whose
// wildcard segments align with it.
func Lookup(path string) (Entry, bool) {
	if i, ok := registryIndex[path]; ok {
		return Registry[i], true
	}
	for i := range Registry {
		if MatchWildcard(Registry[i].Path, path) {
			return Registry[i], true
		}
	}
	return Entry{}, false
}

// ErrUnclassified reports a path with no lifecycle disposition. It is returned
// instead of a permissive default so an unclassified field fails closed.
type ErrUnclassified struct{ Path string }

func (e *ErrUnclassified) Error() string {
	return fmt.Sprintf("lifecycle: configuration path %q has no disposition; add it to internal/lifecycle/registry.go and run `make lifecycle-generate`", e.Path)
}

// ClassOf returns the class governing path, or an *ErrUnclassified when the
// path has no disposition.
func ClassOf(path string) (Class, error) {
	e, ok := Lookup(path)
	if !ok {
		return 0, &ErrUnclassified{Path: path}
	}
	return e.Class, nil
}

// MatchWildcard reports whether concrete is an instance of registryPath, where
// "*" in registryPath matches exactly one path segment.
func MatchWildcard(registryPath, concrete string) bool {
	rs := strings.Split(registryPath, ".")
	cs := strings.Split(concrete, ".")
	if len(rs) != len(cs) {
		return false
	}
	for i := range rs {
		if rs[i] == "*" {
			continue
		}
		if rs[i] != cs[i] {
			return false
		}
	}
	return true
}

// StartupFields returns every entry whose effective value is captured in the
// startup fingerprint, in registry order.
func StartupFields() []Entry {
	out := make([]Entry, 0, 64)
	for _, e := range Registry {
		if e.StartupConsumed {
			out = append(out, e)
		}
	}
	return out
}

// ClassCounts returns how many entries carry each class.
func ClassCounts() map[Class]int {
	out := make(map[Class]int, len(classNames))
	for _, c := range AllClasses() {
		out[c] = 0
	}
	for _, e := range Registry {
		out[e.Class]++
	}
	return out
}

// normalizeStreamProtocol returns the canonical protocol name for a stream
// listener, treating the empty string as the "tcp" default.
func normalizeStreamProtocol(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	if p == "" {
		return "tcp"
	}
	return p
}
