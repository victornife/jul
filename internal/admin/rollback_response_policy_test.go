// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"jul/internal/config"
)

// TestRollbackPreservesResponsePolicyOrderAndCORS pins ADR 0018 §8/§9's
// ordering contract through a full apply → apply-again → rollback round trip
// (#147 scope item 3: "history/rollback must preserve exact ordering and
// policy"). history.snapshot stores the exact serialized bytes captured at
// apply time, so nothing about response_headers/cors/predicates needs its own
// rollback logic — this test is the regression pin that guarantees it, not a
// new mechanism.
func TestRollbackPreservesResponsePolicyOrderAndCORS(t *testing.T) {
	s, cfgPath := v2WriteServer(t)

	seedCfg, err := config.Parse(readFile(t, cfgPath))
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	loc := &seedCfg.Servers[0].Locations[0]
	loc.Match.Methods = []string{"GET", "POST"}
	loc.Match.Headers = []config.HeaderMatch{{Name: "X-Tenant", Op: "present"}}
	// Order matters: set then two adds is the canonical deterministic
	// multi-value form (ADR 0018 §8), so a byte-for-byte round trip is the
	// only proof that matters here.
	loc.ResponseHeaders = []config.ResponseHeaderOp{
		{Op: "set", Name: "X-Frame-Options", Value: strp("DENY")},
		{Op: "add", Name: "X-Extra", Value: strp("one")},
		{Op: "add", Name: "X-Extra", Value: strp("two")},
	}
	loc.CORS = &config.CORSConfig{
		Enabled:        true,
		AllowedOrigins: []string{"https://app.example.test"},
		ExposedHeaders: []string{"X-Request-Id"},
	}
	firstRaw, err := config.Marshal(seedCfg)
	if err != nil {
		t.Fatalf("marshal state 1: %v", err)
	}
	if rr := rollbackApply(t, s, firstRaw); rr.Code != http.StatusOK {
		t.Fatalf("apply 1: status %d, body %s", rr.Code, rr.Body.String())
	}

	// State 2 reorders the response-header operations and changes the CORS
	// policy, moving the live file away from state 1 and leaving a rollback
	// snapshot of state 1 behind it.
	secondCfg, err := config.Parse(firstRaw)
	if err != nil {
		t.Fatalf("parse for state 2: %v", err)
	}
	loc2 := &secondCfg.Servers[0].Locations[0]
	loc2.ResponseHeaders = []config.ResponseHeaderOp{
		{Op: "add", Name: "X-Extra", Value: strp("two")},
		{Op: "add", Name: "X-Extra", Value: strp("one")},
		{Op: "set", Name: "X-Frame-Options", Value: strp("DENY")},
	}
	loc2.CORS.AllowedOrigins = []string{"https://other.example.test"}
	secondRaw, err := config.Marshal(secondCfg)
	if err != nil {
		t.Fatalf("marshal state 2: %v", err)
	}
	if rr := rollbackApply(t, s, secondRaw); rr.Code != http.StatusOK {
		t.Fatalf("apply 2: status %d, body %s", rr.Code, rr.Body.String())
	}

	// The newest snapshot in history is the pre-state-2 config, i.e. state 1.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/history", nil))
	var entries []historyEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("decode history: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected a history snapshot")
	}
	id := entries[0].ID

	body := `{"id":"` + id + `"}`
	rr2 := httptest.NewRecorder()
	s.routes().ServeHTTP(rr2, httptest.NewRequest(http.MethodPost, "/api/config/rollback", bytes.NewReader([]byte(body))))
	if rr2.Code != http.StatusOK {
		t.Fatalf("rollback: status %d, body %s", rr2.Code, rr2.Body.String())
	}

	disk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if !bytes.Equal(disk, firstRaw) {
		t.Fatalf("rollback did not restore state 1 byte-for-byte:\nwant:\n%s\ngot:\n%s", firstRaw, disk)
	}

	restored, err := config.Parse(disk)
	if err != nil {
		t.Fatalf("parse restored: %v", err)
	}
	rloc := restored.Servers[0].Locations[0]
	if len(rloc.ResponseHeaders) != 3 ||
		rloc.ResponseHeaders[0].Op != "set" ||
		rloc.ResponseHeaders[1].Value == nil || *rloc.ResponseHeaders[1].Value != "one" ||
		rloc.ResponseHeaders[2].Value == nil || *rloc.ResponseHeaders[2].Value != "two" {
		t.Fatalf("response_headers order not restored: %+v", rloc.ResponseHeaders)
	}
	if rloc.CORS == nil || len(rloc.CORS.AllowedOrigins) != 1 || rloc.CORS.AllowedOrigins[0] != "https://app.example.test" {
		t.Fatalf("cors not restored: %+v", rloc.CORS)
	}
	if len(rloc.Match.Methods) != 2 || len(rloc.Match.Headers) != 1 {
		t.Fatalf("predicates not restored: %+v", rloc.Match)
	}
}

// rollbackApply POSTs raw TOML to /api/config/apply, the same path an
// operator's raw editor or a typed-patch candidate ultimately writes through.
func rollbackApply(t *testing.T, s *Server, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(raw)))
	return rr
}

// readFile reads path or fails the test, for seeding a config to mutate in
// memory before re-marshaling.
func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
