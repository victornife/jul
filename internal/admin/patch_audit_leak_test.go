// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPatchAuditDoesNotLeakPredicateOrHeaderValues pins #147 scope item 3
// ("audit summaries record operation class and safe identifiers, not
// complete arbitrary header/query values by default"). Unlike the generic
// secret-pattern redaction recordAudit applies to known credential shapes
// (TestAuditRedactsSensitiveDetail), an ordinary header/query predicate value
// is not a recognized secret pattern — so the guarantee has to come from
// applyPatch never putting the value into the summary in the first place,
// which this test verifies end to end through the real HTTP apply pipeline.
func TestPatchAuditDoesNotLeakPredicateOrHeaderValues(t *testing.T) {
	s, _ := v2WriteServer(t)

	const tenantSecret = "acme-tenant-98214-do-not-log"
	const headerSecret = "internal-build-marker-7f3c9a"
	const corsOrigin = "https://sensitive-partner.example.test"

	ops := []patchRequest{
		{
			Op: "location_set_predicates", Listen: ":8080", MatchType: "prefix", Path: "/",
			Predicates: &locationPredicates{
				Headers: &[]headerPredicate{{Name: "X-Tenant", Op: "exact", Value: strp(tenantSecret)}},
			},
		},
		{
			Op: "location_response_headers_set", Listen: ":8080", MatchType: "prefix", Path: "/",
			ResponseHeaders: &[]responseHeaderOpPatch{
				{Op: "set", Name: "X-Build-Marker", Value: strp(headerSecret)},
			},
		},
		{
			Op: "location_cors_set", Listen: ":8080", MatchType: "prefix", Path: "/",
			CORS: &corsPatch{Enabled: true, AllowedOrigins: []string{corsOrigin}},
		},
	}
	body, err := json.Marshal(patchApplyRequest{Ops: ops})
	if err != nil {
		t.Fatalf("marshal ops: %v", err)
	}

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("patch apply: status %d, body %s", rr.Code, rr.Body.String())
	}

	events := s.audit.snapshot("", "", 0)
	if len(events) == 0 {
		t.Fatal("expected at least one audit event")
	}
	for _, e := range events {
		for _, secret := range []string{tenantSecret, headerSecret, corsOrigin} {
			if strings.Contains(e.Detail, secret) {
				t.Errorf("audit detail leaked a configured value: operation=%q detail=%q contains %q", e.Operation, e.Detail, secret)
			}
		}
	}
}
