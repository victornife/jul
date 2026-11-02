// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandleWAFEvaluateEndpoint(t *testing.T) {
	// 1. Unwired EvaluateWAF returns 503.
	sUnwired := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	sUnwired.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unwired status = %d, want 503", rr.Code)
	}

	// 2. Method not allowed returns 405.
	rrGet := httptest.NewRecorder()
	sUnwired.handleWAFEvaluate(rrGet, httptest.NewRequest(http.MethodGet, "/api/security/waf/evaluate", nil))
	if rrGet.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", rrGet.Code)
	}

	// 3. Wired EvaluateWAF handles success, unavailable error (503), and bad request (400).
	sWired := newTestServer(t, config.AdminConfig{}, Deps{
		EvaluateWAF: func(_ context.Context, body []byte) (*WAFEvaluationSummaryView, error) {
			switch strings.TrimSpace(string(body)) {
			case "unavailable":
				return nil, ErrWAFEvaluationUnavailable
			case "bad":
				return nil, errors.New("invalid pack")
			default:
				return &WAFEvaluationSummaryView{State: "current", TotalCases: 10, PassedCases: 10}, nil
			}
		},
	})
	rrOK := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrOK, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("{}")))
	if rrOK.Code != http.StatusOK {
		t.Fatalf("OK status = %d, want 200", rrOK.Code)
	}

	rrUnavail := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrUnavail, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("unavailable")))
	if rrUnavail.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable status = %d, want 503", rrUnavail.Code)
	}

	rrBad := httptest.NewRecorder()
	sWired.routes().ServeHTTP(rrBad, httptest.NewRequest(http.MethodPost, "/api/security/waf/evaluate", strings.NewReader("bad")))
	if rrBad.Code != http.StatusBadRequest {
		t.Fatalf("bad status = %d, want 400", rrBad.Code)
	}
}
