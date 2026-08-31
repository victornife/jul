// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"jul/internal/admin"
	"jul/internal/adminapi"
	"jul/internal/configcontract"
)

// bearerSchemeName is the security scheme every authenticated operation
// references.
const bearerSchemeName = "adminToken"

// Build renders the external contract. It returns an error rather than an
// approximation whenever the catalog and the DTOs disagree, so a mistake stops
// the generator instead of reaching the published document.
func Build() (*Document, error) {
	doc := &Document{
		OpenAPI: "3.1.0",
		Info: Info{
			Title:   "Jul admin API",
			Version: Version,
			Description: strings.Join([]string{
				"The supported, versioned external administration API.",
				"",
				"Only the operations described here are part of the compatibility contract. The admin listener serves many " +
					"other routes; they exist for the Console and may change shape in any release, and being served is not the " +
					"same as being supported. See docs/compatibility.md.",
				"",
				"Every authenticated route requires a transport that is either TLS-terminated or bound to loopback. A request " +
					"that arrives in cleartext on a non-loopback listener is refused with 403 insecure_transport before " +
					"authentication, on reads as well as writes, and the refusal is not configurable.",
				"",
				"This document is generated from the Go route catalog and the Go request and response types. It is not edited " +
					"by hand and an edit to it will be overwritten; change the source and regenerate.",
			}, "\n"),
			License: &License{Name: "GNU Affero General Public License v3.0 or later", Identifier: "AGPL-3.0-or-later"},
		},
		Paths: map[string]*PathItem{},
		Components: Components{
			Schemas:   map[string]*Schema{},
			Responses: map[string]*Response{},
			SecuritySchemes: map[string]*SecurityScheme{
				bearerSchemeName: {
					Type:   "http",
					Scheme: "bearer",
					Description: "An admin bearer token. Tokens are issued out of band through the configuration file; there is no " +
						"token-issuance API. No example is given here, and none should be copied from documentation into a " +
						"deployment: an example that resembles a credential is a credential someone will paste into a configuration file.",
				},
			},
		},
		Tags: []Tag{
			{Name: "health", Description: "Unauthenticated liveness and readiness probes."},
			{Name: "observability", Description: "Metrics exposition."},
		},
	}

	if err := addSchemas(doc); err != nil {
		return nil, err
	}
	addErrorResponses(doc)
	if err := addPaths(doc, externalRoutes()); err != nil {
		return nil, err
	}
	if err := checkResourcePaths(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// addSchemas reflects every registered DTO into a component.
func addSchemas(doc *Document) error {
	types := adminapi.SchemaTypes()
	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s, err := schemaFor(types[name], name)
		if err != nil {
			return fmt.Errorf("schema %s: %w", name, err)
		}
		doc.Components.Schemas[name] = s
	}

	// The error code catalogue is a component of its own so a generated client
	// gets an enum it can switch on exhaustively, rather than a bare string.
	codes := errorCodes()
	enum := make([]string, 0, len(codes))
	var meanings []string
	for _, c := range codes {
		spec, ok := adminapi.Spec(c)
		if !ok {
			return fmt.Errorf("code %q has no catalogue entry", c)
		}
		enum = append(enum, string(c))
		meanings = append(meanings, fmt.Sprintf("- `%s` (%d): %s", c, spec.Status, spec.Meaning))
	}
	doc.Components.Schemas["ErrorCode"] = &Schema{
		Type: "string",
		Enum: enum,
		Description: "The bounded external error-code catalogue. `code` is the machine contract; the accompanying `message` " +
			"is for humans and may change in any release. Each code maps to exactly one HTTP status.\n\n" +
			strings.Join(meanings, "\n"),
	}
	// The envelope's own code field refers to that enum rather than repeating
	// it, which is what keeps one definition of the catalogue in the document.
	if body, ok := doc.Components.Schemas["ErrorBody"]; ok {
		body.Properties["code"] = &Schema{Ref: "#/components/schemas/ErrorCode"}
	}

	for name, ns := range adminapi.NonJSONSchemas {
		doc.Components.Schemas[name] = &Schema{Type: "string", Description: ns.Description}
	}
	return nil
}

// addErrorResponses creates one reusable response per catalogue code. An
// operation references the ones it can return, so the document enumerates the
// conditions a client must handle rather than describing a generic failure.
func addErrorResponses(doc *Document) {
	for _, c := range errorCodes() {
		spec, _ := adminapi.Spec(c)
		doc.Components.Responses[errorResponseName(c)] = &Response{
			Description: fmt.Sprintf("`%s` — %s", c, spec.Meaning),
			Headers: map[string]Header{
				"X-Request-ID": {
					Description: "The server-minted correlation identifier, identical to the envelope's request_id. A " +
						"client-supplied value is never reflected.",
					Schema: &Schema{Type: "string"},
				},
			},
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{Ref: "#/components/schemas/ErrorEnvelope"}},
			},
		}
	}
}

// errorResponseName turns a snake_case code into the CamelCase component name
// its response is registered under.
func errorResponseName(c adminapi.Code) string {
	parts := strings.Split(string(c), "_")
	var b strings.Builder
	for _, p := range parts {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(p[1:])
	}
	return b.String()
}

// addPaths renders every externally classified route. Nothing else reaches the
// document: an internal route has no ExternalOperation, so it cannot be
// rendered even by mistake.
//
// The routes are a parameter rather than being read from the catalog here so a
// test can drive the per-method branches without mutating the global catalog.
func addPaths(doc *Document, routes []admin.ExternalRoute) error {
	for _, r := range routes {
		op, err := buildOperation(r, doc)
		if err != nil {
			return err
		}
		item := doc.Paths[r.Pattern]
		if item == nil {
			item = &PathItem{}
			doc.Paths[r.Pattern] = item
		}
		switch r.Method {
		case http.MethodGet:
			item.Get = op
		case http.MethodPost:
			item.Post = op
		case http.MethodPut:
			item.Put = op
		case http.MethodPatch:
			item.Patch = op
		case http.MethodDelete:
			item.Delete = op
		case http.MethodHead:
			item.Head = op
		default:
			return fmt.Errorf("route %s declares method %s, which the external contract does not model", r.Pattern, r.Method)
		}
	}
	return nil
}

func buildOperation(r admin.ExternalRoute, doc *Document) (*Operation, error) {
	op := &Operation{
		OperationID: r.Operation.ID,
		Summary:     r.Operation.Summary,
		Tags:        tagsFor(r.Pattern),
		Deprecated:  r.Stability == admin.StabilityDeprecated,
		Stability:   r.Stability.String(),
		Permissions: r.Permissions,
		Sunset:      r.Sunset,
		Responses:   map[string]*Response{},
	}
	if r.Sunset != "" {
		op.Description = "Deprecated. This operation keeps working until " + r.Sunset +
			" and responds with Deprecation and Sunset headers. Migrate before that date."
	}
	if r.Public {
		// An explicit empty security requirement is how OpenAPI says "this
		// operation takes no credential", overriding a document-level default.
		op.Security = []map[string][]string{{}}
	} else {
		op.Security = []map[string][]string{{bearerSchemeName: {}}}
	}

	// Path parameters are derived from the pattern rather than declared, so a
	// renamed segment cannot leave a stale parameter behind.
	for _, name := range pathParameters(r.Pattern) {
		op.Parameters = append(op.Parameters, Parameter{
			Name:     name,
			In:       "path",
			Required: true,
			Schema:   &Schema{Type: "string"},
		})
	}

	if err := addSuccessResponse(op, r, doc); err != nil {
		return nil, err
	}

	codes := append([]adminapi.Code{}, adminapi.UniversalErrorCodes(r.Public)...)
	for _, extra := range r.Operation.Errors {
		codes = append(codes, adminapi.Code(extra))
	}
	seen := map[adminapi.Code]bool{}
	for _, c := range codes {
		if seen[c] {
			continue
		}
		seen[c] = true
		spec, ok := adminapi.Spec(c)
		if !ok {
			return nil, fmt.Errorf("operation %s lists error code %q, which is not in the catalogue", r.Operation.ID, c)
		}
		op.Responses[strconv.Itoa(spec.Status)] = &Response{
			Description: fmt.Sprintf("`%s` — %s", c, spec.Meaning),
			Content: map[string]MediaType{
				"application/json": {Schema: &Schema{Ref: "#/components/schemas/ErrorEnvelope"}},
			},
		}
	}
	return op, nil
}

func addSuccessResponse(op *Operation, r admin.ExternalRoute, doc *Document) error {
	status := r.Operation.SuccessStatus
	if status == 0 {
		status = http.StatusOK
	}
	name := r.Operation.Response
	if name == "" {
		return fmt.Errorf("operation %s names no response schema", r.Operation.ID)
	}

	mediaType := "application/json"
	if ns, ok := adminapi.NonJSONSchemas[name]; ok {
		mediaType = ns.MediaType
	} else if _, ok := doc.Components.Schemas[name]; !ok {
		return fmt.Errorf("operation %s names response schema %q, which is not registered in internal/adminapi", r.Operation.ID, name)
	}

	resp := &Response{
		Description: "Success.",
		Content: map[string]MediaType{
			mediaType: {Schema: &Schema{Ref: "#/components/schemas/" + name}},
		},
	}
	if !r.Public {
		resp.Headers = map[string]Header{
			"X-Request-ID": {
				Description: "The server-minted correlation identifier for this request.",
				Schema:      &Schema{Type: "string"},
			},
		}
	}
	op.Responses[strconv.Itoa(status)] = resp
	return nil
}

// pathParameters extracts {name} segments from a route pattern, in order.
func pathParameters(pattern string) []string {
	var out []string
	rest := pattern
	for {
		open := strings.Index(rest, "{")
		if open < 0 {
			return out
		}
		end := strings.Index(rest[open:], "}")
		if end < 0 {
			return out
		}
		out = append(out, rest[open+1:open+end])
		rest = rest[open+end+1:]
	}
}

func tagsFor(pattern string) []string {
	switch {
	case pattern == "/healthz" || pattern == "/readyz":
		return []string{"health"}
	case pattern == "/metrics":
		return []string{"observability"}
	case strings.HasPrefix(pattern, "/api/v1/config"):
		return []string{"configuration"}
	case strings.HasPrefix(pattern, "/api/v1/routes"):
		return []string{"routes"}
	case strings.HasPrefix(pattern, "/api/v1/upstreams"):
		return []string{"upstreams"}
	case strings.HasPrefix(pattern, "/api/v1/listeners"):
		return []string{"listeners"}
	case strings.HasPrefix(pattern, "/api/v1/streams"):
		return []string{"streams"}
	default:
		return []string{"status"}
	}
}

// checkResourcePaths holds the document to the generated resource catalog
// (ADR 0019 §21, §29). A per-resource external path exists in the document
// because the catalog says the resource has a durable identity addressable at
// that path — not because someone wrote the path down in two places.
//
// It applies to per-resource collection addressing — /api/v1/<collection>/{id} —
// and not to operation identities such as /api/v1/config/applies/{apply_id} or
// the history id, which name operations rather than configuration resources and
// deliberately have no entry in the resource catalog (ADR 0019 §21).
//
// The check is one-directional on purpose: the catalog may name a path this
// build has not published yet, and that is the ordinary state while the
// external surface is being filled in. What must never happen is the reverse —
// a per-resource path in the published contract that no resource claims.
func checkResourcePaths(doc *Document) error {
	claimed := make(map[string]string)
	for _, res := range configcontract.ResourceCatalog {
		if res.ExternalPath != "" {
			claimed[res.ExternalPath] = res.Kind
		}
	}
	for pattern := range doc.Paths {
		if !isResourceAddressPath(pattern) {
			continue
		}
		if _, ok := claimed[pattern]; !ok {
			return fmt.Errorf("path %s addresses a resource, but no entry in the generated resource catalog claims it.\n"+
				"Add the resource to internal/configcontract.ResourceCatalog with that ExternalPath, or remove the path: "+
				"a per-resource URI that no resource claims is a second, unchecked identity model", pattern)
		}
	}
	return nil
}

// isResourceAddressPath reports whether a pattern addresses one element of a v1
// collection: exactly /api/v1/<collection>/{param}.
func isResourceAddressPath(pattern string) bool {
	rest, ok := strings.CutPrefix(pattern, "/api/v1/")
	if !ok {
		return false
	}
	segments := strings.Split(rest, "/")
	if len(segments) != 2 {
		return false
	}
	return strings.HasPrefix(segments[1], "{") && strings.HasSuffix(segments[1], "}")
}
