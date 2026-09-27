// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/config"
)

// The Security projection overlays the serving generation's compiled policy
// (#440): even when the on-disk config says otherwise, waf_effective reports
// what is enforced, and it is omitted when no source is wired.
func TestSecurityProjectionOverlaysServingWAFPolicy(t *testing.T) {
	onDisk := &config.Config{WAF: config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true}}
	serving := &WAFEffectivePolicy{
		Compiled:           true,
		EmbeddedCRSVersion: "4.25.0",
		Generation:         9,
		GlobalEnabled:      true,
		InheritingRoutes:   1,
		Global:             &WAFPolicySummary{Mode: "detect", BlockStatus: 403, Rules: WAFRuleCounts{Total: 1, Inline: 1}},
	}
	get := func(deps Deps) SecurityProjection {
		t.Helper()
		deps.LoadConfig = func() (*config.Config, error) { return onDisk, nil }
		s := newTestServer(t, config.AdminConfig{}, deps)
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/security", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		var out SecurityProjection
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := get(Deps{}); out.WAFEffective != nil {
		t.Fatalf("unwired waf_effective = %+v", out.WAFEffective)
	}
	out := get(Deps{WAFEffective: func() *WAFEffectivePolicy { return serving }})
	if out.WAFGlobalMode != "block" {
		t.Fatalf("configuration projection changed: %q", out.WAFGlobalMode)
	}
	e := out.WAFEffective
	if e == nil || e.Generation != 9 || e.Global == nil || e.Global.Mode != "detect" || e.EmbeddedCRSVersion != "4.25.0" {
		t.Fatalf("waf_effective = %+v", e)
	}
}
