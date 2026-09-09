// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// Package apicontract renders the external admin API contract as an OpenAPI
// 3.1 document (ADR 0019 §29).
package apicontract

import (
	"encoding/json"

	"jul/internal/admin"
	"jul/internal/adminapi"
)

//go:generate go run ./apicontractgen -out ../../docs

const Version = "v1"

type Document struct {
	OpenAPI    string                `json:"openapi"`
	Info       Info                  `json:"info"`
	Paths      map[string]*PathItem  `json:"paths"`
	Components Components            `json:"components"`
	Security   []map[string][]string `json:"security,omitempty"`
	Tags       []Tag                 `json:"tags,omitempty"`
}

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

type PathItem struct {
	Get    *Operation `json:"get,omitempty"`
	Post   *Operation `json:"post,omitempty"`
	Put    *Operation `json:"put,omitempty"`
	Patch  *Operation `json:"patch,omitempty"`
	Delete *Operation `json:"delete,omitempty"`
	Head   *Operation `json:"head,omitempty"`
}

type Operation struct {
	OperationID  string                `json:"operationId"`
	Summary      string                `json:"summary"`
	Description  string                `json:"description,omitempty"`
	Tags         []string              `json:"tags,omitempty"`
	Deprecated   bool                  `json:"deprecated,omitempty"`
	Parameters   []Parameter           `json:"parameters,omitempty"`
	RequestBody  *RequestBody          `json:"requestBody,omitempty"`
	Responses    map[string]*Response  `json:"responses"`
	Security     []map[string][]string `json:"security,omitempty"`
	Stability    string                `json:"x-jul-stability"`
	Permissions  []string              `json:"x-jul-permissions,omitempty"`
	Sunset       string                `json:"x-jul-sunset,omitempty"`
	MaxBodyBytes int64                 `json:"x-jul-max-body-bytes,omitempty"`
	ErrorCodes   []string              `json:"x-jul-error-codes,omitempty"`
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

type Components struct {
	Schemas         map[string]*Schema         `json:"schemas"`
	Responses       map[string]*Response       `json:"responses,omitempty"`
	SecuritySchemes map[string]*SecurityScheme `json:"securitySchemes,omitempty"`
}

type SecurityScheme struct {
	Type        string `json:"type"`
	Scheme      string `json:"scheme,omitempty"`
	Description string `json:"description,omitempty"`
}

type Schema struct {
	Ref                        string             `json:"$ref,omitempty"`
	Type                       string             `json:"type,omitempty"`
	Format                     string             `json:"format,omitempty"`
	Description                string             `json:"description,omitempty"`
	Enum                       []string           `json:"enum,omitempty"`
	Default                    any                `json:"default,omitempty"`
	Items                      *Schema            `json:"items,omitempty"`
	Properties                 map[string]*Schema `json:"properties,omitempty"`
	Required                   []string           `json:"required,omitempty"`
	AdditionalProperties       *bool              `json:"-"`
	AdditionalPropertiesSchema *Schema            `json:"-"`
}

// MarshalJSON renders JSON Schema's additionalProperties union without making
// the rest of the Go model untyped. Typed objects use the boolean form (`false`)
// while deliberate dynamic maps use a schema describing their value type.
func (s Schema) MarshalJSON() ([]byte, error) {
	type schemaAlias Schema
	var additional any
	switch {
	case s.AdditionalPropertiesSchema != nil:
		additional = s.AdditionalPropertiesSchema
	case s.AdditionalProperties != nil:
		additional = *s.AdditionalProperties
	}
	return json.Marshal(struct {
		schemaAlias
		AdditionalProperties any `json:"additionalProperties,omitempty"`
	}{
		schemaAlias:          schemaAlias(s),
		AdditionalProperties: additional,
	})
}

func externalRoutes() []admin.ExternalRoute { return admin.ExternalRoutes() }
func errorCodes() []adminapi.Code           { return adminapi.Codes() }
