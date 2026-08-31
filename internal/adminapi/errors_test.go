// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// allCodes is the catalogue as the constants declare it. The test below holds
// it and the map to exact-set equality, so a code added to one and forgotten in
// the other fails rather than silently existing in half the contract.
var allCodes = []Code{
	CodeInvalidRequest, CodeValidationFailed, CodeOperationFailed,
	CodeUnauthenticated, CodeForbidden, CodeInsecureTransport, CodeNotFound,
	CodeConfigAuthorityRO, CodeStaleBaseVersion, CodeDriftDetected,
	CodePendingRestartConf, CodeRestartRequired, CodeAdminReachabilityConf,
	CodeIdempotencyKeyReused, CodeIdempotencyKeyInUse, CodePayloadTooLarge,
	CodeUnsupportedMediaType, CodeRateLimited, CodeInternalError,
	CodeNotImplemented, CodeStorageUnavailable, CodeOperationTimeout,
}

// TestCatalogueIsClosedAndComplete pins the bounded set. ADR 0019 §26 rule 4
// makes adding a code an additive API change that must reach OpenAPI, the
// compatibility document and the contract tests together; this is where that
// starts failing if it does not.
func TestCatalogueIsClosedAndComplete(t *testing.T) {
	if got, want := len(allCodes), 22; got != want {
		t.Fatalf("the declared catalogue has %d codes, ADR 0019 §26 fixes it at %d.\n"+
			"Adding one is an additive API change: update §26, docs/admin-api.md, docs/compatibility.md and this count together.", got, want)
	}
	if len(catalog) != len(allCodes) {
		t.Fatalf("catalog map has %d entries, the constants declare %d", len(catalog), len(allCodes))
	}
	for _, c := range allCodes {
		if _, ok := Spec(c); !ok {
			t.Errorf("code %q is declared but absent from the catalog map", c)
		}
	}
	declared := make(map[Code]bool, len(allCodes))
	for _, c := range allCodes {
		if declared[c] {
			t.Errorf("code %q is declared twice", c)
		}
		declared[c] = true
	}
	for c := range catalog {
		if !declared[c] {
			t.Errorf("catalog map carries %q, which no constant declares", c)
		}
	}
}

// TestOneCodeOneStatus is ADR 0019 §26 rule 2. It is the rule that forced
// payload_too_large and unsupported_media_type into existence: an earlier draft
// mapped oversized and unsupported bodies onto invalid_request, which is fixed
// at 400, so 413 and 415 had no code that could represent them.
func TestOneCodeOneStatus(t *testing.T) {
	want := map[Code]int{
		CodeInvalidRequest:        http.StatusBadRequest,
		CodeValidationFailed:      http.StatusBadRequest,
		CodeOperationFailed:       http.StatusBadRequest,
		CodeUnauthenticated:       http.StatusUnauthorized,
		CodeForbidden:             http.StatusForbidden,
		CodeInsecureTransport:     http.StatusForbidden,
		CodeNotFound:              http.StatusNotFound,
		CodeConfigAuthorityRO:     http.StatusConflict,
		CodeStaleBaseVersion:      http.StatusConflict,
		CodeDriftDetected:         http.StatusConflict,
		CodePendingRestartConf:    http.StatusConflict,
		CodeRestartRequired:       http.StatusConflict,
		CodeAdminReachabilityConf: http.StatusConflict,
		CodeIdempotencyKeyReused:  http.StatusConflict,
		CodeIdempotencyKeyInUse:   http.StatusConflict,
		CodePayloadTooLarge:       http.StatusRequestEntityTooLarge,
		CodeUnsupportedMediaType:  http.StatusUnsupportedMediaType,
		CodeRateLimited:           http.StatusTooManyRequests,
		CodeInternalError:         http.StatusInternalServerError,
		CodeNotImplemented:        http.StatusNotImplemented,
		CodeStorageUnavailable:    http.StatusServiceUnavailable,
		CodeOperationTimeout:      http.StatusGatewayTimeout,
	}
	if len(want) != len(allCodes) {
		t.Fatalf("this test enumerates %d codes, the catalogue has %d", len(want), len(allCodes))
	}
	for c, status := range want {
		if got := c.Status(); got != status {
			t.Errorf("code %q maps to %d, want %d", c, got, status)
		}
	}
}

// TestAuthorityDenialIsConflictNotForbidden pins the distinction ADR 0019 §9
// makes deliberately: a file-owned mutation denial is a property of the
// server's configuration, identical for every principal including a wildcard
// admin, so returning 403 would send an operator to look at RBAC for a problem
// RBAC cannot explain.
func TestAuthorityDenialIsConflictNotForbidden(t *testing.T) {
	if CodeConfigAuthorityRO.Status() != http.StatusConflict {
		t.Fatalf("config_authority_read_only must be 409, got %d", CodeConfigAuthorityRO.Status())
	}
	if CodeConfigAuthorityRO.Status() == CodeForbidden.Status() {
		t.Fatal("config_authority_read_only must not share a status with forbidden")
	}
}

// TestDetailKeysMatchTheDetailsStruct proves the closed Details struct and the
// per-code key lists describe the same thing. A key documented for a code but
// absent from the struct would be undeliverable; a struct field no code may
// carry would be an unpublished field.
func TestDetailKeysMatchTheDetailsStruct(t *testing.T) {
	structKeys := make(map[string]bool)
	rt := reflect.TypeOf(Details{})
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		if name == "" || name == "-" {
			t.Fatalf("Details.%s has no json name; every detail field is part of the published contract", rt.Field(i).Name)
		}
		structKeys[name] = true
	}

	documented := make(map[string]bool)
	for _, c := range allCodes {
		spec, _ := Spec(c)
		for _, k := range spec.DetailKeys {
			if !structKeys[k] {
				t.Errorf("code %q documents detail key %q, which Details cannot carry", c, k)
			}
			if documented[k] {
				continue
			}
			documented[k] = true
		}
	}
	for k := range structKeys {
		if !documented[k] {
			t.Errorf("Details carries %q, which no code documents; either document it or delete the field", k)
		}
	}
}

// TestInsecureTransportDetailsCarryNoConfigurationValue is the case ADR 0019
// §26 rule 3 names as the one that tests the rule. An earlier draft returned
// the listen address, which is a configuration value, and returned it *before
// authentication*.
func TestInsecureTransportDetailsCarryNoConfigurationValue(t *testing.T) {
	spec, ok := Spec(CodeInsecureTransport)
	if !ok {
		t.Fatal("insecure_transport is missing from the catalogue")
	}
	if !reflect.DeepEqual(spec.DetailKeys, []string{"required"}) {
		t.Fatalf("insecure_transport details are %v; it carries only `required`, a constant of the contract rather than a fact about this server", spec.DetailKeys)
	}
}

// TestNoDetailKeyNamesAConfigurationValue is the blunt structural half of
// §26 rule 3. Field paths are safe; field values are not.
func TestNoDetailKeyNamesAConfigurationValue(t *testing.T) {
	forbidden := []string{"listen", "token", "secret", "password", "candidate", "body", "content", "bytes", "path_value", "cert", "key"}
	rt := reflect.TypeOf(Details{})
	for i := range rt.NumField() {
		name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
		for _, f := range forbidden {
			if name == f {
				t.Errorf("Details carries %q: `details` never carries a value read from a configuration field (ADR 0019 §26 rule 3)", name)
			}
		}
	}
}

// TestEnvelopeShape pins the wire shape itself, including that an error with no
// details omits the object entirely rather than emitting an empty one.
func TestEnvelopeShape(t *testing.T) {
	limit := int64(1 << 20)
	env := Envelope{Error: Body{
		Code:      CodePayloadTooLarge,
		Message:   "The request body exceeded the admin cap.",
		Details:   Details{LimitBytes: &limit},
		RequestID: "01J9ZZZZZZZZZZZZZZZZZZZZZZ",
	}}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	const want = `{"error":{"code":"payload_too_large","message":"The request body exceeded the admin cap.","details":{"limit_bytes":1048576},"request_id":"01J9ZZZZZZZZZZZZZZZZZZZZZZ"}}`
	if string(b) != want {
		t.Fatalf("envelope shape changed.\n got: %s\nwant: %s", b, want)
	}

	bare, err := json.Marshal(Envelope{Error: Body{Code: CodeInternalError, Message: "x", RequestID: "y"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(bare), "details") {
		t.Fatalf("an error with no details must omit the object, got %s", bare)
	}
}

// TestErrorStatusFailsVisiblyOnAnUnknownCode: an Error carrying a code outside
// the catalogue is a programming error. Reporting 500 makes it visible; a zero
// status would make http.ResponseWriter emit 200 with an error body.
func TestErrorStatusFailsVisiblyOnAnUnknownCode(t *testing.T) {
	e := &Error{Code: Code("not_a_real_code"), Message: "x"}
	if got := e.Status(); got != http.StatusInternalServerError {
		t.Fatalf("unknown code produced status %d, want 500", got)
	}
}

func TestCodesIsSortedAndComplete(t *testing.T) {
	got := Codes()
	if len(got) != len(allCodes) {
		t.Fatalf("Codes() returned %d, want %d", len(got), len(allCodes))
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return got[i] < got[j] }) {
		t.Fatal("Codes() must be sorted so generated artifacts are deterministic")
	}
}

// TestRequestIDIsSortableOpaqueAndUnique covers the three properties the
// correlation id is used for: it orders by mint time in a log, it discloses
// nothing, and two of them do not collide.
func TestRequestIDIsSortableOpaqueAndUnique(t *testing.T) {
	base := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	earlier := newRequestIDAt(base)
	later := newRequestIDAt(base.Add(time.Second))
	if len(earlier) != 26 {
		t.Fatalf("request id is %d characters, want 26", len(earlier))
	}
	if earlier >= later {
		t.Fatalf("request ids must sort by mint time: %q >= %q", earlier, later)
	}
	for _, c := range earlier {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("request id %q contains %q, which is outside the Crockford alphabet", earlier, c)
		}
	}
	seen := make(map[string]bool, 1000)
	for range 1000 {
		id := NewRequestID()
		if seen[id] {
			t.Fatalf("duplicate request id %q", id)
		}
		seen[id] = true
	}
}
