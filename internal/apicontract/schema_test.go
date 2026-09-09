// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package apicontract

import (
	"encoding/json"
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

func TestScalarKindsMapToJSONSchemaTypes(t *testing.T) {
	cases := []struct {
		value      any
		wantType   string
		wantFormat string
	}{
		{"", "string", ""}, {true, "boolean", ""}, {int(0), "integer", "int64"},
		{int8(0), "integer", "int64"}, {int16(0), "integer", "int64"}, {int32(0), "integer", "int64"},
		{int64(0), "integer", "int64"}, {uint(0), "integer", "int64"}, {uint8(0), "integer", "int64"},
		{uint16(0), "integer", "int64"}, {uint32(0), "integer", "int64"}, {uint64(0), "integer", "int64"},
		{float32(0), "number", ""}, {float64(0), "number", ""},
	}
	for _, tc := range cases {
		s := mustSchema(t, tc.value)
		if s.Type != tc.wantType || s.Format != tc.wantFormat {
			t.Errorf("%T -> {type:%q format:%q}, want {type:%q format:%q}", tc.value, s.Type, s.Format, tc.wantType, tc.wantFormat)
		}
	}
}

func TestPointerIsOptionalityNotNullability(t *testing.T) {
	n := 0
	s := mustSchema(t, &n)
	if s.Type != "integer" {
		t.Fatalf("*int -> %q, want integer", s.Type)
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

func TestTypedStringMapsBecomeControlledAdditionalProperties(t *testing.T) {
	s := mustSchema(t, map[string]string{})
	if s.Type != "object" || s.AdditionalPropertiesSchema == nil || s.AdditionalPropertiesSchema.Type != "string" {
		t.Fatalf("map schema = %+v", s)
	}
	if s.AdditionalProperties != nil {
		t.Fatalf("dynamic map used boolean additionalProperties: %+v", s.AdditionalProperties)
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal map schema: %v", err)
	}
	if !strings.Contains(string(encoded), `"additionalProperties":{"type":"string"}`) {
		t.Fatalf("controlled map schema not rendered: %s", encoded)
	}
}

func TestMapsRejectNonStringKeysAndUnrenderableValues(t *testing.T) {
	if _, err := schemaFor(reflect.TypeFor[map[int]string](), ""); err == nil || !strings.Contains(err.Error(), "non-string keys") {
		t.Fatalf("map[int]string error = %v", err)
	}
	if _, err := schemaFor(reflect.TypeFor[map[string]chan int](), ""); err == nil {
		t.Fatal("map[string]chan was accepted")
	}
}

func TestUnrenderableKindsAreRefused(t *testing.T) {
	for _, typ := range []reflect.Type{reflect.TypeFor[chan int](), reflect.TypeFor[func()](), reflect.TypeFor[complex128](), reflect.TypeFor[any]()} {
		if _, err := schemaFor(typ, ""); err == nil {
			t.Errorf("%v was rendered", typ)
		}
	}
}

type requiredProbe struct {
	Always     string  `json:"always"`
	Omitempty  string  `json:"omitempty_field,omitempty"`
	Omitzero   Nested  `json:"omitzero_field,omitzero"`
	Pointer    *string `json:"pointer_field"`
	unexported string  //nolint:unused // deliberate: proves reflection skips unexported fields
	Skipped    string  `json:"-"`
}

type Nested struct {
	Value string `json:"value"`
}

func TestRequiredIsDerivedFromTheGoTags(t *testing.T) {
	s := mustSchema(t, requiredProbe{})
	if !slices.Equal(s.Required, []string{"always"}) {
		t.Fatalf("required = %v", s.Required)
	}
	if s.AdditionalProperties == nil || *s.AdditionalProperties {
		t.Fatal("typed object must reject unknown fields")
	}
}

type untaggedProbe struct{ Field string }

func TestAnUntaggedFieldIsRefused(t *testing.T) {
	_, err := schemaFor(reflect.TypeFor[untaggedProbe](), "")
	if err == nil || !strings.Contains(err.Error(), "json tag") {
		t.Fatalf("error = %v", err)
	}
}

type badFieldProbe struct {
	Bad map[string]chan int `json:"bad"`
}

func TestAFieldErrorNamesTheField(t *testing.T) {
	_, err := schemaFor(reflect.TypeFor[badFieldProbe](), "")
	if err == nil || !strings.Contains(err.Error(), "Bad") {
		t.Fatalf("error = %v", err)
	}
}

type sliceOfBadProbe struct {
	Items []chan int `json:"items"`
}

func TestASliceElementErrorPropagates(t *testing.T) {
	if _, err := schemaFor(reflect.TypeFor[sliceOfBadProbe](), ""); err == nil {
		t.Fatal("slice of bad type accepted")
	}
}

func TestRegisteredTypesBecomeRefsExceptTheSelfReference(t *testing.T) {
	body, err := schemaFor(reflect.TypeFor[adminapi.Body](), "ErrorBody")
	if err != nil {
		t.Fatal(err)
	}
	details := body.Properties["details"]
	if details == nil || details.Ref != "#/components/schemas/ErrorDetails" {
		t.Fatalf("details = %+v", details)
	}
	got, err := refOrInline(reflect.TypeFor[adminapi.Details](), "ErrorDetails")
	if err != nil || got.Ref != "" || got.Type != "object" {
		t.Fatalf("self = %+v err=%v", got, err)
	}
}
