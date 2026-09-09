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

const bearerSchemeName = "adminToken"

func Build() (*Document, error) {
	doc := &Document{
		OpenAPI: "3.1.0",
		Info: Info{
			Title:   "Jul admin API",
			Version: Version,
			Description: strings.Join([]string{
				"The supported, versioned external administration API.",
				"",
				"Only the operations described here are part of the compatibility contract. The admin listener serves many other routes; they exist for the Console and may change shape in any release.",
				"",
				"Every authenticated route requires a transport that is either TLS-terminated or bound to loopback. Cleartext non-loopback requests are refused before authentication.",
				"",
				"Request bodies, query/header parameters, response types and errors are generated from the authoritative Go route catalog and Go wire types. This document is never hand-edited.",
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
					Description: "An admin bearer token issued out of band through configuration. There is no token-issuance endpoint and no credential example is published.",
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
	if err := checkCatalogPathsAreServed(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func addSchemas(doc *Document) error {
	types := adminapi.SchemaTypes()
	for name, typ := range admin.ExternalSchemaTypes() {
		if _, exists := types[name]; exists {
			return fmt.Errorf("schema %s is registered by both adminapi and admin", name)
		}
		types[name] = typ
	}
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
		Type: "string", Enum: enum,
		Description: "The bounded external error-code catalogue. `code` is the machine contract; `message` is human text.\n\n" + strings.Join(meanings, "\n"),
	}
	if body, ok := doc.Components.Schemas["ErrorBody"]; ok {
		body.Properties["code"] = &Schema{Ref: "#/components/schemas/ErrorCode"}
	}
	for name, ns := range adminapi.NonJSONSchemas {
		doc.Components.Schemas[name] = &Schema{Type: "string", Description: ns.Description}
	}
	doc.Components.Schemas["RawTOML"] = &Schema{
		Type: "string",
		Description: "Exact candidate TOML bytes. The server hashes and processes the received bytes; clients retrying under Idempotency-Key must resend byte-identical content.",
	}
	return nil
}

func addErrorResponses(doc *Document) {
	for _, c := range errorCodes() {
		spec, _ := adminapi.Spec(c)
		doc.Components.Responses[errorResponseName(c)] = &Response{
			Description: fmt.Sprintf("`%s` — %s", c, spec.Meaning),
			Headers: map[string]Header{
				"X-Request-ID": {Description: "Server-minted correlation identifier, identical to error.request_id.", Schema: &Schema{Type: "string"}},
			},
			Content: map[string]MediaType{"application/json": {Schema: &Schema{Ref: "#/components/schemas/ErrorEnvelope"}}},
		}
	}
}

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
		op.Description = "Deprecated. This operation keeps working until " + r.Sunset + " and responds with Deprecation and Sunset headers."
	}
	if r.Public {
		op.Security = []map[string][]string{{}}
	} else {
		op.Security = []map[string][]string{{bearerSchemeName: {}}}
	}

	for _, name := range pathParameters(r.Pattern) {
		op.Parameters = append(op.Parameters, Parameter{Name: name, In: "path", Required: true, Schema: &Schema{Type: "string"}})
	}
	for _, p := range r.Operation.Parameters {
		if p.In != "query" && p.In != "header" {
			return nil, fmt.Errorf("operation %s parameter %s has unsupported location %q", r.Operation.ID, p.Name, p.In)
		}
		if p.Type != "string" && p.Type != "integer" && p.Type != "boolean" {
			return nil, fmt.Errorf("operation %s parameter %s has unsupported type %q", r.Operation.ID, p.Name, p.Type)
		}
		op.Parameters = append(op.Parameters, Parameter{
			Name: p.Name, In: p.In, Required: p.Required, Description: p.Description,
			Schema: &Schema{Type: p.Type, Enum: p.Enum, Default: p.Default},
		})
	}
	if r.Operation.RequestBody != "" {
		if _, ok := doc.Components.Schemas[r.Operation.RequestBody]; !ok {
			return nil, fmt.Errorf("operation %s names request schema %q, which is not registered", r.Operation.ID, r.Operation.RequestBody)
		}
		contentTypes := r.Operation.RequestContentTypes
		if len(contentTypes) == 0 {
			contentTypes = []string{"application/json"}
		}
		content := make(map[string]MediaType, len(contentTypes))
		for _, contentType := range contentTypes {
			content[contentType] = MediaType{Schema: &Schema{Ref: "#/components/schemas/" + r.Operation.RequestBody}}
		}
		description := "Request body."
		if r.Operation.MaxBodyBytes > 0 {
			description = fmt.Sprintf("Request body. Maximum accepted size: %d bytes; larger bodies return payload_too_large before unbounded allocation.", r.Operation.MaxBodyBytes)
		}
		op.RequestBody = &RequestBody{Required: true, Description: description, Content: content}
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
		// Several bounded error codes intentionally share one HTTP status. The
		// OpenAPI response at that status therefore references the common closed
		// envelope; clients branch on error.code, not on prose or status alone.
		key := strconv.Itoa(spec.Status)
		op.Responses[key] = &Response{
			Description: "Failure. Inspect error.code for the bounded machine-readable condition.",
			Content: map[string]MediaType{"application/json": {Schema: &Schema{Ref: "#/components/schemas/ErrorEnvelope"}}},
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
		return fmt.Errorf("operation %s names response schema %q, which is not registered", r.Operation.ID, name)
	}
	makeResponse := func(description string) *Response {
		resp := &Response{Description: description, Content: map[string]MediaType{mediaType: {Schema: &Schema{Ref: "#/components/schemas/" + name}}}}
		if !r.Public {
			resp.Headers = map[string]Header{"X-Request-ID": {Description: "Server-minted correlation identifier for this request.", Schema: &Schema{Type: "string"}}}
		}
		return resp
	}
	op.Responses[strconv.Itoa(status)] = makeResponse("Success.")
	for _, alternate := range r.Operation.AdditionalSuccessStatuses {
		if alternate < 200 || alternate >= 300 || alternate == status {
			return fmt.Errorf("operation %s has invalid additional success status %d", r.Operation.ID, alternate)
		}
		op.Responses[strconv.Itoa(alternate)] = makeResponse("Accepted/non-terminal success. Follow the operation's terminal fields and polling contract rather than inferring completion from HTTP status alone.")
	}
	return nil
}

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
			return fmt.Errorf("path %s addresses a resource, but no entry in the generated resource catalog claims it. Add the resource to internal/configcontract.ResourceCatalog with that ExternalPath, or remove the path", pattern)
		}
	}
	return nil
}

func checkCatalogPathsAreServed(doc *Document) error {
	for _, res := range configcontract.ResourceCatalog {
		if res.ExternalPath == "" {
			continue
		}
		if _, ok := doc.Paths[res.ExternalPath]; !ok {
			return fmt.Errorf("resource %q claims external path %s, but the contract publishes no such path", res.Kind, res.ExternalPath)
		}
	}
	return nil
}

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
