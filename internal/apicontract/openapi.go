// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package apicontract renders the external admin API contract as an OpenAPI
// 3.1 document (ADR 0019 §29).
//
// The document is derived, never authored: paths come from the admin route
// catalog, so a route cannot be in one and absent from the other; schemas come
// from the Go DTOs in internal/adminapi, so a field cannot exist in the server
// and not in the contract; the error components come from that package's
// bounded code catalogue; and resource paths are checked against the generated
// resource catalog from internal/configcontract, so /api/v1/routes/{route_id}
// exists because the catalog says routes have a durable id with that external
// path, not because someone wrote it down twice.
//
// Rendering is deterministic: sorted throughout, with no timestamp, no absolute
// path, no host and no map-iteration order, so the committed artifact is
// byte-identical across clean checkouts and `--check` can run in CI against a
// read-only tree.
package apicontract

import (
	"jul/internal/admin"
	"jul/internal/adminapi"
)

//go:generate go run ./apicontractgen -out ../../docs

// Version is the API namespace this document describes. It is not the release
// version of Jul: an additive change ships in the same namespace, and only a
// breaking change (ADR 0019 §25) creates /api/v2.
const Version = "v1"

// Document is the OpenAPI 3.1 document. Only the subset of the specification
// this contract uses is modelled; an unmodelled keyword is one the generator
// cannot emit, which is the point — the document says what the server does and
// nothing else.
type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Paths      map[string]*PathItem  `json:"paths"`
	Components Components            `json:"components"`
	Security   []map[string][]string `json:"security,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty"`
}

// Info is the document header. It deliberately carries no `servers` block: a
// server URL would be either a local address or a fabricated host, and ADR 0019
// §29 forbids both. A client is configured with its own endpoint.
type Info struct {
	Title       string   `json:"title"`
	Version     string   `json:"version"`
	Description string   `json:"description"`
	License     *License `json:"license,omitempty"`
}

type License struct {
	Name       string `json:"name"`
	Identifier string `json:"identifier,omitempty"`
}

type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// PathItem holds the operations for one path, keyed by lowercase HTTP method.
type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
	Head   *Operation `json:"head,omitempty"`
}

// Operation is one method on one path.
type Operation struct {
	OperationID string                `json:"operationId"`
	Summary     string                `json:"summary"`
	Description string                `json:"description,omitempty"`
	Tags        []string              `json:"tags,omitempty"`
	Deprecated  bool                  `json:"deprecated,omitempty"`
	Parameters  []Parameter           `json:"parameters,omitempty"`
	RequestBody *RequestBody          `json:"requestBody,omitempty"`
	Responses   map[string]*Response  `json:"responses"`
	Security    []map[string][]string `json:"security,omitempty"`
	// Extensions carry the classification metadata a generated client cannot
	// derive: the route's stability, the permissions that admit a caller, and
	// the sunset date of a deprecated operation.
	Stability   string   `json:"x-jul-stability"`
	Permissions []string `json:"x-jul-permissions,omitempty"`
	Sunset      string   `json:"x-jul-sunset,omitempty"`
}

type Parameter struct {
	Name        string  `json:"name"`
	In          string  `json:"in"`
	Description string  `json:"description,omitempty"`
	Required    bool    `json:"required,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

type RequestBody struct {
	Description string               `json:"description,omitempty"`
	Required    bool                 `json:"required,omitempty"`
	Content     map[string]MediaType `json:"content"`
}

type Response struct {
	Description string               `json:"description"`
	Headers     map[string]Header    `json:"headers,omitempty"`
	Content     map[string]MediaType `json:"content,omitempty"`
}

type Header struct {
	Description string  `json:"description,omitempty"`
	Schema      *Schema `json:"schema,omitempty"`
}

type MediaType struct {
	Schema *Schema `json:"schema,omitempty"`
}

// Components holds the reusable pieces. The error envelope and the code
// catalogue are components referenced by every operation rather than being
// restated per operation (ADR 0019 §29).
type Components struct {
	Schemas         map[string]*Schema         `json:"schemas"`
	Responses       map[string]*Response       `json:"responses,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

// SecurityScheme describes the bearer credential. It carries no example: an
// example resembling a credential in a published document is a credential
// someone will paste into a configuration file (ADR 0019 §29).
type SecurityScheme struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description,omitempty"`
}

// Schema is the JSON Schema 2020-12 subset OpenAPI 3.1 uses.
type Schema struct {
	Ref                  string             `json:"$ref,omitempty"`
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Description          string             `json:"description,omitempty"`
	Enum                 []string           `json:"enum,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	Required             []string           `json:"required,omitempty"`
	AdditionalProperties *bool              `json:"additionalProperties,omitempty"`
}

// externalRoutes is the one derivation of the external surface. Both the
// generator and the contract tests call it, so neither re-derives the pairing
// of pattern, method, permission and operation metadata.
func externalRoutes() []admin.ExternalRoute { return admin.ExternalRoutes() }

// errorCodes is the one derivation of the error catalogue.
func errorCodes() []adminapi.Code { return adminapi.Codes() }
