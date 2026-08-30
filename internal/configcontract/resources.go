// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import "strings"

// IdentityClass classifies how a resource is addressed, per ADR 0019 §5/§21.
type IdentityClass string

const (
	// IdentityDurableID means the resource carries an explicit, minted,
	// persistent identifier independent of its mutable content.
	IdentityDurableID IdentityClass = "durable_id"
	// IdentityNaturalKey means a single required, validated, unique field
	// already names the resource; renaming it is delete-plus-create.
	IdentityNaturalKey IdentityClass = "natural_key"
	// IdentityCompositeNaturalKey is a natural key made of more than one field.
	IdentityCompositeNaturalKey IdentityClass = "composite_natural_key"
	// IdentityRevisionSelector means the resource is addressed by coordinates
	// meaningful only against a specific configuration revision
	// (base_version), not a durable name.
	IdentityRevisionSelector IdentityClass = "revision_selector"
	// IdentityNone means no public identity is required or provided; the
	// resource is edited as part of its enclosing collection.
	IdentityNone IdentityClass = "none"
)

// keyIsIdentity is the sentinel IdentityFields value meaning the dynamic
// wildcard segment itself (a map key, e.g. a plugin name) is the identity,
// rather than a named field inside the collection element.
const keyIsIdentity = "$key"

// Resource is one row of the generated resource catalog (ADR 0019 §5/§21): a
// configuration resource classified by identity, never a fourth artifact —
// it is rendered into config-metadata.json's "resources" section.
type Resource struct {
	// Kind is a stable lowercase identifier, e.g. "route", "upstream_pool".
	Kind string
	// CollectionPath is the canonical config.SchemaPaths() path naming the
	// collection this resource is an element of, e.g. "upstreams.*".
	CollectionPath string
	IdentityClass  IdentityClass
	// IdentityFields are the field names (relative to CollectionPath) that
	// jointly name the resource, or the keyIsIdentity sentinel when the
	// dynamic map key itself is the identity. Nil for IdentityNone.
	IdentityFields []string
	// UniquenessScope is "configuration" (unique document-wide),
	// "collection" (unique within CollectionPath's siblings) or "none".
	UniquenessScope string
	Required        bool
	// Renameable is false when changing the identity is delete-plus-create.
	Renameable bool
	// ExternalPath is the existing per-resource admin API pattern, or "" when
	// only a collection-level endpoint exists today.
	ExternalPath string
}

// CollectionElementPath returns the schema container path that
// r.CollectionPath addresses one element of, stripping the trailing dynamic
// wildcard segment CollectionPath uses by convention (ADR 0019 §21's
// illustrative "servers.*.locations.*" names one location under the
// "servers.*.locations" collection — the collection path itself, not one of
// its elements, is what appears in config.SchemaPaths()).
func (r Resource) CollectionElementPath() string {
	return strings.TrimSuffix(r.CollectionPath, ".*")
}

// ResourceCatalog is the accepted classification of every configuration
// resource ADR 0019 §5 lists with persistence owner "config owner" — the
// only resources this generator's catalog covers.
//
// Excluded, by the same rule ADR 0019 §21 states for operation identities:
// Discovery endpoint and Certificate are runtime-materialized, not
// configuration (persistence owner "runtime" in §5's table), and
// Configuration revision / Managed apply operation / History revision /
// Reload transaction are operation identities with no schema path at all.
//
// "route_id" (ADR 0019 §4) is not yet part of this schema (see the
// generator's README/PR notes) — Route is classified as a revision-scoped
// selector only until that field lands; the catalog needs no change when it
// does, only a new row.
//
// ADR 0019 §5's table lists "RBAC role / principal" as one row; this catalog
// splits it into two kinds (rbac_role, rbac_principal) because they are two
// distinct schema collections sharing one identity pattern (natural key by
// name) — tests check every config-owner §5 row has at least one catalog
// kind, not a strict one-row-one-entry bijection.
var ResourceCatalog = []Resource{
	{
		Kind:            "route",
		CollectionPath:  "servers.*.locations.*",
		IdentityClass:   IdentityRevisionSelector,
		IdentityFields:  nil,
		UniquenessScope: "none",
		Required:        false,
		Renameable:      true,
		ExternalPath:    "",
	},
	{
		Kind:            "upstream_pool",
		CollectionPath:  "upstreams.*",
		IdentityClass:   IdentityNaturalKey,
		IdentityFields:  []string{"name"},
		UniquenessScope: "configuration",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "/api/upstreams/{name}/resilience",
	},
	{
		Kind:            "upstream_backend",
		CollectionPath:  "upstreams.*.servers.*",
		IdentityClass:   IdentityNone,
		IdentityFields:  nil,
		UniquenessScope: "none",
		Required:        false,
		Renameable:      true,
		ExternalPath:    "",
	},
	{
		Kind:            "http_server",
		CollectionPath:  "servers.*",
		IdentityClass:   IdentityRevisionSelector,
		IdentityFields:  []string{"listen", "server_names"},
		UniquenessScope: "none",
		Required:        false,
		Renameable:      true,
		ExternalPath:    "",
	},
	{
		Kind:            "listener",
		CollectionPath:  "servers.*",
		IdentityClass:   IdentityCompositeNaturalKey,
		IdentityFields:  []string{"listen"},
		UniquenessScope: "configuration",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "/api/listeners/{addr}/client_address",
	},
	{
		Kind:            "stream",
		CollectionPath:  "stream.*",
		IdentityClass:   IdentityCompositeNaturalKey,
		IdentityFields:  []string{"protocol", "listen"},
		UniquenessScope: "configuration",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "",
	},
	{
		Kind:            "plugin",
		CollectionPath:  "plugins.*",
		IdentityClass:   IdentityNaturalKey,
		IdentityFields:  []string{keyIsIdentity},
		UniquenessScope: "configuration",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "",
	},
	{
		Kind:            "rbac_role",
		CollectionPath:  "admin.rbac.roles.*",
		IdentityClass:   IdentityNaturalKey,
		IdentityFields:  []string{"name"},
		UniquenessScope: "collection",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "",
	},
	{
		Kind:            "rbac_principal",
		CollectionPath:  "admin.rbac.principals.*",
		IdentityClass:   IdentityNaturalKey,
		IdentityFields:  []string{"name"},
		UniquenessScope: "collection",
		Required:        true,
		Renameable:      false,
		ExternalPath:    "",
	},
}
