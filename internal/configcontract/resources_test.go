// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"strings"
	"testing"

	"jul/internal/config"
)

// TestResourceCatalogCollectionPathsResolve proves every CollectionPath
// addresses one element of a real dynamic schema collection: stripping its
// trailing wildcard segment names a container classified array_table or
// map_table, so the catalog cannot drift from the schema.
func TestResourceCatalogCollectionPathsResolve(t *testing.T) {
	byPath := map[string]config.SchemaPath{}
	for _, p := range config.SchemaPaths() {
		byPath[p.Path] = p
	}
	for _, r := range ResourceCatalog {
		elem := r.CollectionElementPath()
		p, ok := byPath[elem]
		if !ok {
			t.Errorf("resource %s: CollectionElementPath %q does not resolve against config.SchemaPaths()", r.Kind, elem)
			continue
		}
		if p.Structure != config.StructArrayTable && p.Structure != config.StructMapTable {
			t.Errorf("resource %s: %q has Structure %q, want array_table or map_table", r.Kind, elem, p.Structure)
		}
	}
}

// TestResourceCatalogIdentityFieldsResolve proves every named identity field
// resolves against its resource's schema path, and that the "$key" sentinel
// (the dynamic map key itself is the identity) is used only where
// CollectionPath is in fact a dynamic collection.
func TestResourceCatalogIdentityFieldsResolve(t *testing.T) {
	schema := map[string]bool{}
	for _, p := range config.SchemaPaths() {
		schema[p.Path] = true
	}
	for _, r := range ResourceCatalog {
		for _, field := range r.IdentityFields {
			if field == keyIsIdentity {
				if !strings.HasSuffix(r.CollectionPath, ".*") {
					t.Errorf("resource %s: %q sentinel requires a dynamic CollectionPath, got %q", r.Kind, keyIsIdentity, r.CollectionPath)
				}
				continue
			}
			full := r.CollectionPath + "." + field
			if !schema[full] {
				t.Errorf("resource %s: identity field %q (%s) does not resolve against config.SchemaPaths()", r.Kind, field, full)
			}
		}
	}
}

// TestResourceCatalogNoDuplicateKinds proves each row is unique.
func TestResourceCatalogNoDuplicateKinds(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range ResourceCatalog {
		if seen[r.Kind] {
			t.Errorf("duplicate resource kind %q", r.Kind)
		}
		seen[r.Kind] = true
	}
}

// TestResourceCatalogCoversEveryConfigOwnedADR5Resource proves every
// configuration resource ADR 0019 §5's table classifies with persistence
// owner "config owner" is represented by at least one catalog kind. "RBAC
// role / principal" is one table row satisfied by two kinds (rbac_role,
// rbac_principal), documented in resources.go.
func TestResourceCatalogCoversEveryConfigOwnedADR5Resource(t *testing.T) {
	want := map[string][]string{
		"route":                  {"route"},
		"upstream_pool":          {"upstream_pool"},
		"upstream_backend":       {"upstream_backend"},
		"http_server":            {"http_server"},
		"listener":               {"listener"},
		"stream":                 {"stream"},
		"plugin":                 {"plugin"},
		"rbac_role_or_principal": {"rbac_role", "rbac_principal"},
	}
	have := map[string]bool{}
	for _, r := range ResourceCatalog {
		have[r.Kind] = true
	}
	for row, kinds := range want {
		found := false
		for _, k := range kinds {
			if have[k] {
				found = true
			}
		}
		if !found {
			t.Errorf("ADR 0019 §5 row %q has no representative catalog kind among %v", row, kinds)
		}
	}
}

// TestResourceCatalogNoOperationIdentity proves no operation identity
// (configuration revision, managed apply, history revision, reload
// transaction) or runtime-only resource (discovery endpoint, certificate)
// leaked into the configuration resource catalog.
func TestResourceCatalogNoOperationIdentity(t *testing.T) {
	forbidden := []string{
		"configuration_revision", "config_revision", "base_version",
		"managed_apply", "apply_operation", "apply_id",
		"history_revision", "history",
		"reload_transaction", "reload",
		"discovery_endpoint", "discovery",
		"certificate", "cert",
	}
	for _, r := range ResourceCatalog {
		for _, f := range forbidden {
			if r.Kind == f {
				t.Errorf("resource catalog contains forbidden operation/runtime kind %q", r.Kind)
			}
		}
	}
}

// TestResourceCatalogExternalPathsMatchADR0019Section24 pins every
// ExternalPath to the accepted external `/api/v1` contract (ADR 0019 §24),
// never a legacy internal `/api/...` route and never an invented one for a
// resource the accepted table does not address individually.
func TestResourceCatalogExternalPathsMatchADR0019Section24(t *testing.T) {
	want := map[string]string{
		"route":            "/api/v1/routes/{route_id}",
		"upstream_pool":    "/api/v1/upstreams/{name}",
		"upstream_backend": "",
		"http_server":      "",
		"listener":         "/api/v1/listeners/{addr}/client_address",
		"stream":           "",
		"plugin":           "",
		"rbac_role":        "",
		"rbac_principal":   "",
	}
	have := map[string]string{}
	for _, r := range ResourceCatalog {
		have[r.Kind] = r.ExternalPath
	}
	for kind, path := range want {
		got, ok := have[kind]
		if !ok {
			t.Errorf("resource kind %q is missing from the catalog", kind)
			continue
		}
		if got != path {
			t.Errorf("resource %q ExternalPath = %q, want %q", kind, got, path)
		}
		if path != "" && !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("resource %q ExternalPath %q does not use the accepted /api/v1 contract", kind, path)
		}
		if strings.HasPrefix(path, "/api/") && !strings.HasPrefix(path, "/api/v1/") {
			t.Errorf("resource %q ExternalPath %q encodes a legacy /api/... route instead of /api/v1/...", kind, path)
		}
	}
}

// TestResourceCatalogRouteIsDurableID proves the route resource is the exact
// shape ADR 0019 §21 describes now that route_id exists: one row, durable_id,
// optional, not renameable (changing the ID is delete+create).
func TestResourceCatalogRouteIsDurableID(t *testing.T) {
	var route *Resource
	for i := range ResourceCatalog {
		if ResourceCatalog[i].Kind == "route" {
			route = &ResourceCatalog[i]
		}
	}
	if route == nil {
		t.Fatal("route resource missing from catalog")
	}
	if route.IdentityClass != IdentityDurableID {
		t.Errorf("route IdentityClass = %q, want %q", route.IdentityClass, IdentityDurableID)
	}
	if len(route.IdentityFields) != 1 || route.IdentityFields[0] != "route_id" {
		t.Errorf("route IdentityFields = %v, want [route_id]", route.IdentityFields)
	}
	if route.Required {
		t.Error("route should not be Required: a route_id is optional")
	}
	if route.Renameable {
		t.Error("route should not be Renameable: changing route_id is delete+create")
	}
}
