// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"jul/internal/adminapi"
)

func mustSchema(t *testing.T, v any) *Schema {
	t.Helper()
	s, err := schemaFor(reflect.TypeOf(v), "")
	if err != nil {
		t.Fatalf("schemaFor(%T): %v", v, err)
	}
	return s
}

// TestScalarKindsMapToJSONSchemaTypes covers the leaf branches of the
// reflector. They are the boring half of the contract and the half a client
// generator turns directly into field types, so an integer published as a
// string is a compile error in every generated client.
func TestScalarKindsMapToJSONSchemaTypes(t *testing.T) {
	cases := []struct {
		value      any
		wantType   string
		wantFormat string
	}{
		{"", "string", ""},
		{true, "boolean", ""},
		{int(0), "integer", "int64"},
		{int8(0), "integer", "int64"},
		{int16(0), "integer", "int64"},
		{int32(0), "integer", "int64"},
		{int64(0), "integer", "int64"},
		{uint(0), "integer", "int64"},
		{uint8(0), "integer", "int64"},
		{uint16(0), "integer", "int64"},
		{uint32(0), "integer", "int64"},
		{uint64(0), "integer", "int64"},
		{float32(0), "number", ""},
		{float64(0), "number", ""},
	}
	for _, tc := range cases {
		s := mustSchema(t, tc.value)
		if s.Type != tc.wantType || s.Format != tc.wantFormat {
			t.Errorf("%T -> {type:%q format:%q}, want {type:%q format:%q}", tc.value, s.Type, s.Format, tc.wantType, tc.wantFormat)
		}
	}
}

// TestPointerIsOptionalityNotNullability. The DTOs use pointers where "omitted"
// and "present and zero" must stay distinct; that is optionality, which
// omitempty already expresses, not a nullable type.
func TestPointerIsOptionalityNotNullability(t *testing.T) {
	n := 0
	s := mustSchema(t, &n)
	if s.Type != "integer" {
		t.Fatalf("*int -> %q, want integer", s.Type)
	}
	if strings.Contains(s.Type, "null") {
		t.Fatal("a pointer field must not be published as a nullable type")
	}
}

func TestSliceAndArrayBecomeArrays(t *testing.T) {
	s := mustSchema(t, []string{})
	if s.Type != "array" || s.Items == nil || s.Items.Type != "string" {
		t.Fatalf("[]string -> %+v", s)
	}
	a := mustSchema(t, [3]bool{})
	if a.Type != "array" || a.Items == nil || a.Items.Type != "boolean" {
		t.Fatalf("[3]bool -> %+v", a)
	}
}

// TestMapsAreRefused is the deliberate restriction: a map renders as a
// free-form JSON object, which publishes an unbounded shape — exactly what the
// closed Details struct exists to avoid.
func TestMapsAreRefused(t *testing.T) {
	_, err := schemaFor(reflect.TypeFor[map[string]string](), "")
	if err == nil {
		t.Fatal("a map-typed field was accepted; a free-form object publishes an unbounded shape")
	}
	if !strings.Contains(err.Error(), "unbounded shape") {
		t.Fatalf("the error does not explain the restriction: %v", err)
	}
}

// TestUnrenderableKindsAreRefused: a kind with no JSON representation must stop
// the generator rather than silently produce an empty schema.
func TestUnrenderableKindsAreRefused(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeFor[chan int](),
		reflect.TypeFor[func()](),
		reflect.TypeFor[complex128](),
		reflect.TypeFor[any](),
	} {
		if _, err := schemaFor(typ, ""); err == nil {
			t.Errorf("%v was rendered as a schema", typ)
		}
	}
}

type requiredProbe struct {
	Always     string  `json:"always"`
	Omitempty  string  `json:"omitempty_field,omitempty"`
	Omitzero   Nested  `json:"omitzero_field,omitzero"`
	Pointer    *string `json:"pointer_field"`
	unexported string  //nolint:unused // present to prove the reflector skips it
	Skipped    string  `json:"-"`
}

// Nested is a second struct so the inline path is exercised alongside $ref.
type Nested struct {
	Value string `json:"value"`
}

// TestRequiredIsDerivedFromTheGoTags. "Required" in the published contract has
// to mean "the server always emits this", and the only honest source for that
// is the Go tag — which is the whole reason the schema is reflected rather than
// hand-maintained.
func TestRequiredIsDerivedFromTheGoTags(t *testing.T) {
	s := mustSchema(t, requiredProbe{})

	if !slices.Equal(s.Required, []string{"always"}) {
		t.Fatalf("required = %v, want [always]: omitempty, omitzero and pointer fields are all optional", s.Required)
	}
	for _, name := range []string{"always", "omitempty_field", "omitzero_field", "pointer_field"} {
		if s.Properties[name] == nil {
			t.Errorf("property %q is missing", name)
		}
	}
	if _, ok := s.Properties["-"]; ok {
		t.Error(`a field tagged json:"-" reached the contract`)
	}
	if _, ok := s.Properties["Skipped"]; ok {
		t.Error(`a field tagged json:"-" reached the contract under its Go name`)
	}
	if _, ok := s.Properties["unexported"]; ok {
		t.Error("an unexported field reached the contract")
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		t.Error("additionalProperties must be false: unknown request fields are rejected (ADR 0019 §24a)")
	}
	if nested := s.Properties["omitzero_field"]; nested == nil || nested.Type != "object" {
		t.Errorf("an unregistered nested struct must be inlined, got %+v", nested)
	}
}

type untaggedProbe struct {
	Field string
}

// TestAnUntaggedFieldIsRefused. Letting encoding/json default the wire key to
// the Go field name would make renaming an unexported-looking identifier a
// breaking API change nobody reviewed.
func TestAnUntaggedFieldIsRefused(t *testing.T) {
	_, err := schemaFor(reflect.TypeFor[untaggedProbe](), "")
	if err == nil {
		t.Fatal("a field with no json tag was accepted")
	}
	if !strings.Contains(err.Error(), "json tag") {
		t.Fatalf("the error does not name the cause: %v", err)
	}
}

type badFieldProbe struct {
	Bad map[string]string `json:"bad"`
}

// TestAFieldErrorNamesTheField: the generator refuses, and says which field, so
// the fix does not require bisecting the DTO.
func TestAFieldErrorNamesTheField(t *testing.T) {
	_, err := schemaFor(reflect.TypeFor[badFieldProbe](), "")
	if err == nil {
		t.Fatal("a struct containing a map was accepted")
	}
	if !strings.Contains(err.Error(), "Bad") {
		t.Fatalf("the error does not name the offending field: %v", err)
	}
}

type sliceOfBadProbe struct {
	Items []chan int `json:"items"`
}

func TestASliceElementErrorPropagates(t *testing.T) {
	if _, err := schemaFor(reflect.TypeFor[sliceOfBadProbe](), ""); err == nil {
		t.Fatal("a slice of an unrenderable type was accepted")
	}
	if _, err := schemaFor(reflect.TypeFor[[]chan int](), ""); err == nil {
		t.Fatal("a bare slice of an unrenderable type was accepted")
	}
}

// TestRegisteredTypesBecomeRefsExceptTheSelfReference is what keeps the
// document to one definition per shape while stopping a self-referential type
// from recursing forever.
func TestRegisteredTypesBecomeRefsExceptTheSelfReference(t *testing.T) {
	// Rendering ErrorBody: the nested Details is registered, so it is a $ref.
	body, err := schemaFor(reflect.TypeFor[adminapi.Body](), "ErrorBody")
	if err != nil {
		t.Fatalf("schemaFor: %v", err)
	}
	details := body.Properties["details"]
	if details == nil || details.Ref != "#/components/schemas/ErrorDetails" {
		t.Fatalf("details = %+v, want a $ref to ErrorDetails", details)
	}

	// Rendering ErrorDetails itself: the component currently being rendered is
	// inlined rather than referencing itself.
	got, err := refOrInline(reflect.TypeFor[adminapi.Details](), "ErrorDetails")
	if err != nil {
		t.Fatalf("refOrInline: %v", err)
	}
	if got.Ref != "" {
		t.Fatalf("the component being rendered referenced itself: %q", got.Ref)
	}
	if got.Type != "object" {
		t.Fatalf("self reference inlined as %q", got.Type)
	}

	// A pointer to a registered type still resolves to the component.
	ptr, err := refOrInline(reflect.TypeFor[*adminapi.Finding](), "")
	if err != nil {
		t.Fatalf("refOrInline: %v", err)
	}
	if ptr.Ref != "#/components/schemas/ValidationFinding" {
		t.Fatalf("*Finding -> %+v, want a $ref to ValidationFinding", ptr)
	}
}
