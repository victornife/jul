// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/observability"
	"jul/internal/rbac"
	"jul/internal/server"
	"jul/internal/versionid"
	"jul/internal/waf"
)

func TestAPIV1WAFEventsPermissionsAndFilters(t *testing.T) {
	store := waf.NewInvestigationStore(16, 15*time.Minute)
	ev1 := waf.TransactionEvent{
		EventID:              "wev_0123456789abcdef0123456789abcdef",
		Time:                 time.Now().UTC().Add(-time.Minute),
		Outcome:              waf.OutcomeInterruptedRequest,
		Mode:                 "block",
		WouldInterrupt:       true,
		InterruptedAction:    "deny",
		InterruptedRuleID:    942100,
		ClientStatus:         403,
		OriginReached:        false,
		PhasesEvaluated:      []string{"request_headers", "request_body"},
		Method:               "POST",
		Host:                 "app.example.test",
		Path:                 "/api/login",
		Route:                waf.RouteAttribution{RouteID: "rt-login", Listen: ":8443", ServerNames: []string{"app.example.test"}, MatchType: "prefix", PathPattern: "/api", Scope: "override"},
		Generation:           1,
		PolicyDigest:         "ev1:ctx:oldpolicy",
		CRSVersion:           "4.25.0",
		Paranoia:             1,
		SupportHeaderEmitted: true,
		RequestIDHint:        "req-123",
		TraceIDHint:          "trace-456",
		MatchedRules: []waf.MatchedRuleSummary{
			{
				RuleID:         942100,
				Phase:          2,
				Severity:       "CRITICAL",
				SourceClass:    "embedded",
				Disruptive:     true,
				Logged:         true,
				MatchedTargets: []string{"ARGS:username"},
			},
		},
		TotalMatchedRules: 1,
	}
	store.Record(ev1)

	cfg := &config.Config{
		WAF: config.WAFConfig{Enabled: true, Mode: "block", CRSEnabled: true},
		Servers: []config.ServerConfig{
			{
				Listen: ":8443",
				Locations: []config.LocationConfig{
					{Match: config.MatchConfig{Type: "prefix", Path: "/api"}, Return: 200},
				},
			},
		},
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:  func() (*config.Config, error) { return cfg, nil },
		WAFCompiled: true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:      true,
				Generation:    2,
				GlobalEnabled: true,
				Global: &WAFPolicySummary{
					Mode:         "block",
					PolicyDigest: "ev1:ctx:newpolicy",
				},
			}
		},
		WAFInvestigation: func(filter waf.InvestigationFilter) waf.InvestigationSnapshot {
			return store.Snapshot(time.Now().UTC(), filter)
		},
	})

	// 1. Query with matching filters (operator with observability:read + config:read).
	reqOK := httptest.NewRequest(http.MethodGet, "/api/v1/waf/events?event_id=wev_0123456789abcdef0123456789abcdef&request_id=req-123&trace_id=trace-456&route_id=rt-login&path=/api&outcome=interrupted_request&rule_id=942100&limit=10", nil)
	reqOK = reqOK.WithContext(rbac.WithIdentity(reqOK.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.ObservabilityRead, rbac.ConfigRead},
	}))
	recOK := httptest.NewRecorder()
	s.handleV1WAFEvents(recOK, reqOK)
	if recOK.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recOK.Code, recOK.Body.String())
	}
	var got adminapi.WAFEventsResponse
	if err := json.Unmarshal(recOK.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode WAFEventsResponse: %v", err)
	}
	if got.CollectionState != "active" || len(got.Events) != 1 {
		t.Fatalf("unexpected WAFEventsResponse: %+v", got)
	}
	if !got.Events[0].StalePolicy {
		t.Fatal("expected StalePolicy = true when event generation (1) != serving generation (2)")
	}
	if got.Events[0].Route.PathPattern != "/api" {
		t.Fatalf("expected config:read caller to see Route.PathPattern, got %q", got.Events[0].Route.PathPattern)
	}

	// 2. Auditor (observability:read WITHOUT config:read) gets events with PathPattern stripped.
	reqAud := httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil)
	reqAud = reqAud.WithContext(rbac.WithIdentity(reqAud.Context(), rbac.Identity{
		Principal:   "aud",
		Role:        rbac.RoleAuditor,
		Permissions: []rbac.Permission{rbac.ObservabilityRead},
	}))
	recAud := httptest.NewRecorder()
	s.handleV1WAFEvents(recAud, reqAud)
	if recAud.Code != http.StatusOK {
		t.Fatalf("auditor status = %d", recAud.Code)
	}
	var gotAud adminapi.WAFEventsResponse
	_ = json.Unmarshal(recAud.Body.Bytes(), &gotAud)
	if len(gotAud.Events) != 1 || gotAud.Events[0].Route.PathPattern != "" || len(gotAud.Events[0].Route.ServerNames) != 0 {
		t.Fatalf("expected route path_pattern/server_names stripped for non-config:read caller, got %+v", gotAud.Events[0].Route)
	}

	// 3. Invalid filter parameters return 400 invalid_request.
	for _, badQuery := range []string{
		"outcome=unknown_outcome",
		"rule_id=notanumber",
		"limit=9999",
	} {
		reqBad := httptest.NewRequest(http.MethodGet, "/api/v1/waf/events?"+badQuery, nil)
		recBad := httptest.NewRecorder()
		s.handleV1WAFEvents(recBad, reqBad)
		if recBad.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want 400", badQuery, recBad.Code)
		}
	}
}

func TestWAFInvestigationCanaryPrivacyAllSinks(t *testing.T) {
	if !waf.Compiled {
		t.Skip("requires -tags waf")
	}
	const (
		canaryQuery  = "CANARY_QUERY_SECRET_991122"
		canaryCookie = "CANARY_COOKIE_SECRET_883344"
		canaryHeader = "CANARY_HEADER_SECRET_775566"
		canaryBody   = "CANARY_BODY_SECRET_664422"
	)
	store := waf.NewInvestigationStore(32, 15*time.Minute)
	metrics := observability.NewMetrics()
	fw, err := waf.New(context.Background(), config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		BlockStatus: 403,
		CRSEnabled:  true,
	}, waf.Options{
		Investigation: store,
		Hooks: waf.Hooks{
			OnEvent:              metrics.ObserveWAFEvent,
			OnTransaction:        metrics.ObserveWAFTransaction,
			OnInvestigationEvent: metrics.ObserveWAFInvestigationEvent,
		},
	})
	if err != nil {
		t.Fatalf("waf.New: %v", err)
	}
	h := fw.RouteMiddleware(waf.RouteAttribution{RouteID: "rt-canary", Scope: "global"}, func() uint64 { return 1 })(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	body := "profile_bio=%3Cscript%3Ealert(1)%3C/script%3E&secret_token=" + canaryBody
	req := httptest.NewRequest(http.MethodPost, "http://example.test/submit?api_key="+canaryQuery, strings.NewReader(body))
	req.Host = "example.test"
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Content-Length", "91")
	req.Header.Set("Authorization", "Bearer "+canaryHeader)
	req.Header.Set("Cookie", "session="+canaryCookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{
		WAFCompiled: true,
		WAFInvestigation: func(filter waf.InvestigationFilter) waf.InvestigationSnapshot {
			return store.Snapshot(time.Now().UTC(), filter)
		},
	})
	recEvents := httptest.NewRecorder()
	s.handleV1WAFEvents(recEvents, httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil))

	recMetrics := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recMetrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	for _, sink := range []struct {
		name string
		text string
	}{
		{"client_response", rec.Body.String()},
		{"waf_events_api", recEvents.Body.String()},
		{"prometheus_metrics", recMetrics.Body.String()},
	} {
		for _, canary := range []string{canaryQuery, canaryCookie, canaryHeader, canaryBody} {
			if strings.Contains(sink.text, canary) {
				t.Fatalf("sink %s leaked canary %q: %s", sink.name, canary, sink.text)
			}
		}
	}
}

func TestSecurityPostureCatalogAllAreasAndEpistemicStates(t *testing.T) {
	extEnf := "cloudflare-access"
	cfg := &config.Config{
		Admin: config.AdminConfig{
			Enabled: true,
			Listen:  "0.0.0.0:9090",
			Token:   "literal-admin-token-in-config",
			RBAC:    config.AdminRBACConfig{Enabled: true},
		},
		Upstreams: []config.UpstreamConfig{
			{
				Name:       "insecure-up",
				BackendTLS: &config.BackendTLSConfig{InsecureSkipVerify: true},
			},
		},
		WAF: config.WAFConfig{
			Enabled:     true,
			Mode:        "detect",
			BlockStatus: 403,
			CRSEnabled:  true,
			InlineRules: "SecRuleRemoveById 942100",
			Exceptions: []config.WAFException{
				{
					ID:       "due-exc",
					RuleID:   941100,
					Scope:    "narrow_target",
					Target:   "ARGS:bio",
					ReviewAt: "2024-01-01",
				},
			},
		},
		Cache: config.CacheConfig{
			Enabled: true,
		},
		Servers: []config.ServerConfig{
			{
				Listen: ":8080",
				ClientAddress: &config.ClientAddressConfig{
					TrustedProxies: []string{"0.0.0.0/0"},
				},
				Locations: []config.LocationConfig{
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/public"},
						Return: 200,
						Sharing: &config.SharingConfig{
							Mode: "public",
						},
					},
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/ext"},
						Return: 200,
						Sharing: &config.SharingConfig{
							Mode:             "restricted",
							ExternalEnforcer: &extEnf,
						},
					},
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/broken-restricted"},
						Return: 200,
						Sharing: &config.SharingConfig{
							Mode: "restricted",
						},
						WAF: &config.WAFConfig{Enabled: false},
					},
					{
						Match:     config.MatchConfig{Type: "prefix", Path: "/withdrawn"},
						Return:    200,
						Withdrawn: true,
					},
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/cached-auth"},
						Return: 200,
						Cache:  true,
						Auth:   &config.AuthConfig{Allow: []string{"10.0.0.0/8"}},
					},
				},
			},
		},
	}

	var observedPosture map[string]map[string]int
	evidenceStates := make(map[string]string)

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:  func() (*config.Config, error) { return cfg, nil },
		WAFCompiled: true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:      true,
				Generation:    4,
				GlobalEnabled: false, // differs from cfg.WAF.Enabled -> maintenance finding
				Evaluation: &WAFEvaluationSummaryView{
					State: "stale_asset_changed",
				},
			}
		},
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "managed", Drift: true}
		},
		ObserveSecurityPosture: func(m map[string]map[string]int) {
			observedPosture = m
		},
		ObserveSecurityEvidenceState: func(producer, st string) {
			evidenceStates[producer] = st
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/security/posture", nil)
	req = req.WithContext(rbac.WithIdentity(req.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigRead},
	}))
	rec := httptest.NewRecorder()
	s.handleV1SecurityPosture(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminapi.SecurityPostureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode SecurityPostureResponse: %v", err)
	}
	if len(resp.Areas) != 7 {
		t.Fatalf("expected 7 posture areas, got %d", len(resp.Areas))
	}
	if resp.OverallStatus != "error" || resp.ErrorCount == 0 || resp.WarningCount == 0 {
		t.Fatalf("unexpected overall posture summary: %+v", resp)
	}

	byCode := make(map[string]adminapi.SecurityPostureFinding)
	for _, f := range resp.Findings {
		byCode[f.Code] = f
	}
	for _, wantCode := range []string{
		"admin_off_loopback_no_tls",
		"admin_legacy_token_active_with_rbac",
		"trust_backend_tls_insecure_skip_verify",
		"trust_wide_open_trusted_proxies",
		"waf_routes_unprotected",
		"waf_custom_seclang_unanalysed",
		"waf_evaluation_stale",
		"waf_exception_review_due",
		"cache_authenticated_route_review",
		"certificates_literal_secret_in_config",
		"sharing_public_intentional",
		"sharing_external_enforcer_unverified",
		"sharing_restricted_without_enforcement",
		"sharing_route_withdrawn",
		"maintenance_external_drift_detected",
		"maintenance_waf_serving_differs_from_persisted",
	} {
		if _, ok := byCode[wantCode]; !ok {
			t.Errorf("missing expected posture finding code %q", wantCode)
		}
	}
	if byCode["sharing_public_intentional"].Severity != "info" || byCode["sharing_public_intentional"].EpistemicState != "configured" {
		t.Fatalf("deliberately public route must be info/configured, got %+v", byCode["sharing_public_intentional"])
	}
	if byCode["sharing_external_enforcer_unverified"].EpistemicState != "external_unknown" {
		t.Fatalf("external enforcer must be external_unknown, got %+v", byCode["sharing_external_enforcer_unverified"])
	}
	if byCode["waf_custom_seclang_unanalysed"].EpistemicState != "unanalysed_custom" {
		t.Fatalf("custom SecLang must be unanalysed_custom, got %+v", byCode["waf_custom_seclang_unanalysed"])
	}
	if strings.Contains(rec.Body.String(), "literal-admin-token-in-config") {
		t.Fatal("posture response leaked literal admin token")
	}
	if observedPosture == nil || evidenceStates["waf_effective"] != "stale" {
		t.Fatalf("expected posture and evidence metrics to be updated, got posture=%+v evidence=%+v", observedPosture, evidenceStates)
	}

	// Verify status:read-only caller on the full 7-area cfg hits every redactPostureSummaryForStatusRead branch
	// and handleRoutes strips LocationWAFState details for non-config:read callers (SR-02).
	reqAudFull := httptest.NewRequest(http.MethodGet, "/api/v1/security/posture", nil)
	reqAudFull = reqAudFull.WithContext(rbac.WithIdentity(reqAudFull.Context(), rbac.Identity{
		Principal:   "aud",
		Role:        rbac.RoleAuditor,
		Permissions: []rbac.Permission{rbac.StatusRead},
	}))
	recAudFull := httptest.NewRecorder()
	s.handleV1SecurityPosture(recAudFull, reqAudFull)
	if recAudFull.Code != http.StatusOK {
		t.Fatalf("auditor full posture status = %d", recAudFull.Code)
	}
	if strings.Contains(recAudFull.Body.String(), "cloudflare-access") || strings.Contains(recAudFull.Body.String(), "due-exc") {
		t.Fatalf("status:read posture summary leaked config details: %s", recAudFull.Body.String())
	}

	reqAudRoutes := httptest.NewRequest(http.MethodGet, "/api/routes", nil)
	reqAudRoutes = reqAudRoutes.WithContext(rbac.WithIdentity(reqAudRoutes.Context(), rbac.Identity{
		Principal:   "aud",
		Role:        rbac.RoleAuditor,
		Permissions: []rbac.Permission{rbac.StatusRead},
	}))
	recAudRoutes := httptest.NewRecorder()
	s.handleRoutes(recAudRoutes, reqAudRoutes)
	if recAudRoutes.Code != http.StatusOK {
		t.Fatalf("auditor handleRoutes status = %d", recAudRoutes.Code)
	}

	// Verify status:read-only caller on /api/v1/security/posture has scope redacted to area name,
	// plus additional posture branches (unauthenticated admin, cookie bypass, native gRPC, body limit, uncompiled WAF).
	cfgExtra := *cfg
	cfgExtra.Admin = config.AdminConfig{Enabled: true, Listen: "0.0.0.0:9090"}
	cfgExtra.Servers = []config.ServerConfig{
		{
			Listen:            ":8080",
			ClientMaxBodySize: config.Size(2 << 20),
			Locations: []config.LocationConfig{
				{
					Match:       config.MatchConfig{Type: "prefix", Path: "/cached-bypass"},
					Return:      200,
					Cache:       true,
					Auth:        &config.AuthConfig{Allow: []string{"10.0.0.0/8"}},
					CachePolicy: &config.LocationCachePolicy{BypassAnyCookie: true},
				},
				{
					Match:     config.MatchConfig{Type: "prefix", Path: "/grpc"},
					GRPC:      true,
					ProxyPass: "127.0.0.1:9000",
				},
			},
		},
	}
	sExtra := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig:  func() (*config.Config, error) { return &cfgExtra, nil },
		WAFCompiled: false,
	})
	reqAudPosture := httptest.NewRequest(http.MethodGet, "/api/v1/security/posture", nil)
	reqAudPosture = reqAudPosture.WithContext(rbac.WithIdentity(reqAudPosture.Context(), rbac.Identity{
		Principal:   "aud",
		Role:        rbac.RoleAuditor,
		Permissions: []rbac.Permission{rbac.StatusRead},
	}))
	recAudPosture := httptest.NewRecorder()
	sExtra.handleV1SecurityPosture(recAudPosture, reqAudPosture)
	if recAudPosture.Code != http.StatusOK {
		t.Fatalf("auditor posture status = %d", recAudPosture.Code)
	}
	if !strings.Contains(recAudPosture.Body.String(), "waf_engine_not_compiled") ||
		!strings.Contains(recAudPosture.Body.String(), "admin_off_loopback_unauthenticated") ||
		!strings.Contains(recAudPosture.Body.String(), "cache_authenticated_route_cookie_bypass_configured") ||
		!strings.Contains(recAudPosture.Body.String(), "waf_native_grpc_incompatible") ||
		!strings.Contains(recAudPosture.Body.String(), "waf_body_limit_below_route_max") {
		t.Fatalf("missing expected findings in extra posture response: %s", recAudPosture.Body.String())
	}

	// Also verify handleV1WAFEvents when WAFCompiled=false and when WAFInvestigation=nil.
	recUnavail := httptest.NewRecorder()
	sExtra.handleV1WAFEvents(recUnavail, httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil))
	if !strings.Contains(recUnavail.Body.String(), `"collection_state":"unavailable"`) {
		t.Fatalf("expected unavailable collection_state, got %s", recUnavail.Body.String())
	}
	sDisabled := newTestServer(t, config.AdminConfig{}, Deps{WAFCompiled: true})
	recDisabled := httptest.NewRecorder()
	sDisabled.handleV1WAFEvents(recDisabled, httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil))
	if !strings.Contains(recDisabled.Body.String(), `"collection_state":"disabled"`) {
		t.Fatalf("expected disabled collection_state, got %s", recDisabled.Body.String())
	}
}

func TestAPIV1WAFEventsRouteOverrideStaleAttribution(t *testing.T) {
	// Finding 4: Verify stale_policy compares against the policy serving that
	// event's route and scope across global inheritance, overrides alongside
	// global, and override-only configurations.
	store := waf.NewInvestigationStore(16, 15*time.Minute)
	now := time.Now().UTC()

	store.Record(waf.TransactionEvent{
		EventID:      "wev_global_current_0000000000000001",
		Time:         now,
		Outcome:      waf.OutcomeInterruptedRequest,
		Mode:         "block",
		ClientStatus: 403,
		Method:       "GET",
		Host:         "app.example.test",
		Path:         "/public",
		Route:        waf.RouteAttribution{RouteID: "rt-global", Listen: ":8443", ServerNames: []string{"app.example.test"}, MatchType: "prefix", PathPattern: "/public", Scope: "global"},
		Generation:   5,
		PolicyDigest: "ev1:ctx:global-digest-v5",
	})
	store.Record(waf.TransactionEvent{
		EventID:      "wev_override_current_00000000000002",
		Time:         now,
		Outcome:      waf.OutcomeInterruptedRequest,
		Mode:         "block",
		ClientStatus: 403,
		Method:       "POST",
		Host:         "app.example.test",
		Path:         "/api/pay",
		Route:        waf.RouteAttribution{RouteID: "rt-api-pay", Listen: ":8443", ServerNames: []string{"app.example.test"}, MatchType: "prefix", PathPattern: "/api", Scope: "override"},
		Generation:   5,
		PolicyDigest: "ev1:ctx:override-digest-v5",
	})
	store.Record(waf.TransactionEvent{
		EventID:      "wev_override_stale_0000000000000003",
		Time:         now,
		Outcome:      waf.OutcomeInterruptedRequest,
		Mode:         "block",
		ClientStatus: 403,
		Method:       "POST",
		Host:         "app.example.test",
		Path:         "/api/pay",
		Route:        waf.RouteAttribution{RouteID: "rt-api-pay", Listen: ":8443", ServerNames: []string{"app.example.test"}, MatchType: "prefix", PathPattern: "/api", Scope: "override"},
		Generation:   5,
		PolicyDigest: "ev1:ctx:override-digest-old",
	})

	// Case A: Global + route override serving simultaneously with different digests.
	sMixed := newTestServer(t, config.AdminConfig{}, Deps{
		WAFCompiled: true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:      true,
				Generation:    5,
				GlobalEnabled: true,
				Global:        &WAFPolicySummary{Mode: "block", PolicyDigest: "ev1:ctx:global-digest-v5"},
				Overrides: []WAFRouteOverride{
					{
						RouteID:     "rt-api-pay",
						Listen:      ":8443",
						ServerNames: []string{"app.example.test"},
						MatchType:   "prefix",
						Path:        "/api",
						Enabled:     true,
						Policy:      &WAFPolicySummary{Mode: "block", PolicyDigest: "ev1:ctx:override-digest-v5"},
					},
				},
			}
		},
		WAFInvestigation: func(filter waf.InvestigationFilter) waf.InvestigationSnapshot {
			return store.Snapshot(now, filter)
		},
	})

	recMixed := httptest.NewRecorder()
	sMixed.handleV1WAFEvents(recMixed, httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil))
	var gotMixed adminapi.WAFEventsResponse
	if err := json.Unmarshal(recMixed.Body.Bytes(), &gotMixed); err != nil {
		t.Fatalf("decode mixed WAFEventsResponse: %v", err)
	}
	byID := make(map[string]adminapi.WAFEventRecord)
	for _, ev := range gotMixed.Events {
		byID[ev.EventID] = ev
	}
	if byID["wev_global_current_0000000000000001"].StalePolicy {
		t.Fatal("current global event must not be marked stale")
	}
	if byID["wev_override_current_00000000000002"].StalePolicy {
		t.Fatal("current route-override event with override digest != global digest must NOT be marked stale")
	}
	if !byID["wev_override_stale_0000000000000003"].StalePolicy {
		t.Fatal("route-override event with outdated override digest must be marked stale")
	}

	// Case B: Override-only configuration (Global == nil).
	sOverrideOnly := newTestServer(t, config.AdminConfig{}, Deps{
		WAFCompiled: true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:      true,
				Generation:    5,
				GlobalEnabled: false,
				Global:        nil,
				Overrides: []WAFRouteOverride{
					{
						RouteID:     "rt-api-pay",
						Listen:      ":8443",
						ServerNames: []string{"app.example.test"},
						MatchType:   "prefix",
						Path:        "/api",
						Enabled:     true,
						Policy:      &WAFPolicySummary{Mode: "block", PolicyDigest: "ev1:ctx:override-digest-v5"},
					},
				},
			}
		},
		WAFInvestigation: func(filter waf.InvestigationFilter) waf.InvestigationSnapshot {
			return store.Snapshot(now, filter)
		},
	})
	recOvOnly := httptest.NewRecorder()
	sOverrideOnly.handleV1WAFEvents(recOvOnly, httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil))
	var gotOvOnly adminapi.WAFEventsResponse
	if err := json.Unmarshal(recOvOnly.Body.Bytes(), &gotOvOnly); err != nil {
		t.Fatalf("decode override-only WAFEventsResponse: %v", err)
	}
	byIDOvOnly := make(map[string]adminapi.WAFEventRecord)
	for _, ev := range gotOvOnly.Events {
		byIDOvOnly[ev.EventID] = ev
	}
	if byIDOvOnly["wev_override_current_00000000000002"].StalePolicy {
		t.Fatal("override-only config: current override event must not be marked stale")
	}
	if !byIDOvOnly["wev_global_current_0000000000000001"].StalePolicy {
		t.Fatal("override-only config: event from removed global policy must be marked stale")
	}
}

func writeTestCertFile(t *testing.T, dir, name string, notBefore, notAfter time.Time, dnsNames []string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test.example"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSecurityPostureServingVsPersistedAndCertificateChecks(t *testing.T) {
	// Finding 3 & Area 5 certificates:
	// 1. Persisted config on disk has InsecureSkipVerify=false (fixed on disk, not yet reloaded),
	//    while the live serving snapshot still has InsecureSkipVerify=true and an expired/mismatched TLS cert.
	//    Posture MUST report the serving generation's findings AND maintenance_serving_differs_from_persisted.
	dir := t.TempDir()
	now := time.Now().UTC()
	expiredCert := writeTestCertFile(t, dir, "expired.pem", now.Add(-48*time.Hour), now.Add(-time.Hour), []string{"other.example.test"})
	expiringCert := writeTestCertFile(t, dir, "expiring.pem", now.Add(-time.Hour), now.Add(5*24*time.Hour), []string{"app.example.test"})

	servingCfg := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{
				Name:       "serving-insecure-up",
				BackendTLS: &config.BackendTLSConfig{InsecureSkipVerify: true},
			},
		},
		Servers: []config.ServerConfig{
			{
				Listen:      ":8443",
				ServerNames: []string{"app.example.test"},
				TLS:         &config.TLSConfig{Enabled: true, Cert: expiredCert},
				Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			},
			{
				Listen:      ":9443",
				ServerNames: []string{"app.example.test"},
				TLS:         &config.TLSConfig{Enabled: true, Cert: expiringCert},
				Locations:   []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			},
		},
	}
	persistedCfg := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{
				Name:       "serving-insecure-up",
				BackendTLS: &config.BackendTLSConfig{InsecureSkipVerify: false},
			},
		},
		Servers: []config.ServerConfig{
			{
				Listen:    ":8080",
				Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}},
			},
		},
	}

	s := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return persistedCfg, nil },
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				Generation:      3,
				RawConfig:       servingCfg,
				EffectiveConfig: servingCfg,
			}
		},
		WAFCompiled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/security/posture", nil)
	req = req.WithContext(rbac.WithIdentity(req.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigRead},
	}))
	rec := httptest.NewRecorder()
	s.handleV1SecurityPosture(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp adminapi.SecurityPostureResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode SecurityPostureResponse: %v", err)
	}
	codes := make(map[string]bool)
	for _, f := range resp.Findings {
		codes[f.Code] = true
	}
	for _, want := range []string{
		"trust_backend_tls_insecure_skip_verify",
		"maintenance_serving_differs_from_persisted",
		"certificates_tls_cert_expired",
		"certificates_tls_cert_expiring_soon",
		"certificates_tls_san_mismatch",
	} {
		if !codes[want] {
			t.Fatalf("missing expected posture finding %q in %+v", want, resp.Findings)
		}
	}

	// Finding 3 (re-audit): Healthy ${env:...} / ${file:...} secret expansion where
	// persistedCfg == snap.RawConfig while snap.EffectiveConfig has expanded secret
	// values must NOT emit a false maintenance_serving_differs_from_persisted warning!
	rawWithSecretPlaceholder := &config.Config{
		Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "${env:ADMIN_SECRET}"},
		Servers: []config.ServerConfig{
			{Listen: ":8080", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	effWithExpandedSecret := &config.Config{
		Admin: config.AdminConfig{Enabled: true, Listen: "127.0.0.1:9090", Token: "resolved-secret-value-123456"},
		Servers: []config.ServerConfig{
			{Listen: ":8080", Locations: []config.LocationConfig{{Match: config.MatchConfig{Type: "prefix", Path: "/"}, Return: 200}}},
		},
	}
	sExpanded := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) { return rawWithSecretPlaceholder, nil },
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				Generation:      4,
				RawConfig:       rawWithSecretPlaceholder,
				EffectiveConfig: effWithExpandedSecret,
			}
		},
		WAFCompiled: true,
	})
	recExp := httptest.NewRecorder()
	sExpanded.handleV1SecurityPosture(recExp, req)
	if strings.Contains(recExp.Body.String(), "maintenance_serving_differs_from_persisted") {
		t.Fatalf("healthy secret expansion falsely emitted maintenance_serving_differs_from_persisted: %s", recExp.Body.String())
	}

	// Exercise EvaluateSecurityPostureOffline and remaining certificate/digest branches.
	notYetValidCert := writeTestCertFile(t, dir, "future.pem", now.Add(24*time.Hour), now.Add(48*time.Hour), []string{"app.example.test"})
	badPEMPath := filepath.Join(dir, "bad.pem")
	if err := os.WriteFile(badPEMPath, []byte("not-a-pem-block"), 0o600); err != nil {
		t.Fatal(err)
	}
	badDERPath := filepath.Join(dir, "bad-der.pem")
	if err := os.WriteFile(badDERPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("corrupt")}), 0o600); err != nil {
		t.Fatal(err)
	}
	offlineCfg := &config.Config{
		Upstreams: []config.UpstreamConfig{
			{
				Name: "disc-insecure",
				Discovery: &config.DiscoveryConfig{
					Kubernetes: &config.KubernetesDiscovery{InsecureSkipTLSVerify: true},
				},
			},
		},
		Servers: []config.ServerConfig{
			{Listen: ":7443", ServerNames: []string{"*.example.test", "app.example.test"}, TLS: &config.TLSConfig{Enabled: true, Cert: notYetValidCert}},
			{Listen: ":7444", TLS: &config.TLSConfig{Enabled: true, Cert: badPEMPath}},
			{Listen: ":7445", TLS: &config.TLSConfig{Enabled: true, Cert: badDERPath}},
			{Listen: ":7446", TLS: &config.TLSConfig{Enabled: true, Cert: filepath.Join(dir, "missing.pem")}},
		},
	}
	off1 := EvaluateSecurityPostureOffline(offlineCfg, nil, true)
	off2 := EvaluateSecurityPostureOffline(nil, offlineCfg, false)
	if len(off1.Findings) == 0 || len(off2.Findings) == 0 {
		t.Fatalf("expected offline posture findings, got %d / %d", len(off1.Findings), len(off2.Findings))
	}

	// Exercise evaluation states ("failed", "unrun") and WAF serving != persisted.
	evalState := "failed"
	sEvalAndMaint := newTestServer(t, config.AdminConfig{}, Deps{
		LoadConfig: func() (*config.Config, error) {
			return &config.Config{WAF: config.WAFConfig{Enabled: false}}, nil
		},
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				Generation:      7,
				RawConfig:       &config.Config{WAF: config.WAFConfig{Enabled: true}},
				EffectiveConfig: &config.Config{WAF: config.WAFConfig{Enabled: true}},
			}
		},
		WAFCompiled: true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:      true,
				GlobalEnabled: true,
				Global:        &WAFPolicySummary{Mode: "block"},
				Evaluation:    &WAFEvaluationSummaryView{State: evalState, FailedCases: 2},
			}
		},
		ObserveSecurityEvidenceState: func(string, string) {},
		ObserveSecurityPosture:       func(map[string]map[string]int) {},
	})
	recEvalFailed := httptest.NewRecorder()
	sEvalAndMaint.handleV1SecurityPosture(recEvalFailed, req)
	if !strings.Contains(recEvalFailed.Body.String(), "waf_evaluation_failed") ||
		!strings.Contains(recEvalFailed.Body.String(), "maintenance_waf_serving_differs_from_persisted") {
		t.Fatalf("missing expected findings in eval/maint posture: %s", recEvalFailed.Body.String())
	}
	evalState = "unrun"
	recEvalUnrun := httptest.NewRecorder()
	sEvalAndMaint.handleV1SecurityPosture(recEvalUnrun, req)
	if !strings.Contains(recEvalUnrun.Body.String(), "waf_evaluation_unrun") {
		t.Fatalf("missing waf_evaluation_unrun in: %s", recEvalUnrun.Body.String())
	}

	// Exercise servingPolicyDigestForEvent branches (nil, 5-tuple match, disabled override, single-vhost fallback, missing).
	if got := servingPolicyDigestForEvent(nil, waf.TransactionEvent{}); got != "" {
		t.Fatalf("nil eff digest = %q, want empty", got)
	}
	effDigestTest := &WAFEffectivePolicy{
		Overrides: []WAFRouteOverride{
			{Listen: ":8443", ServerNames: []string{"a.test", "b.test"}, MatchType: "", Path: "/api", MatchOrdinal: 1, Enabled: true, Policy: &WAFPolicySummary{PolicyDigest: "d-tuple"}},
			{RouteID: "rt-dis", Listen: ":8443", Path: "/dis", Enabled: false},
			{Listen: ":9443", ServerNames: []string{"only.test"}, Path: "/single", MatchOrdinal: 0, Enabled: true, Policy: &WAFPolicySummary{PolicyDigest: "d-single"}},
		},
	}
	if got := servingPolicyDigestForEvent(effDigestTest, waf.TransactionEvent{
		Route: waf.RouteAttribution{Scope: "override", Listen: ":8443", ServerNames: []string{"b.test", "a.test"}, PathPattern: "/api", MatchOrdinal: 1},
	}); got != "d-tuple" {
		t.Fatalf("tuple match digest = %q, want d-tuple", got)
	}
	if got := servingPolicyDigestForEvent(effDigestTest, waf.TransactionEvent{
		Route: waf.RouteAttribution{Scope: "override", RouteID: "rt-dis"},
	}); got != "__disabled__" {
		t.Fatalf("disabled route_id digest = %q, want __disabled__", got)
	}
	if got := servingPolicyDigestForEvent(effDigestTest, waf.TransactionEvent{
		Route: waf.RouteAttribution{Scope: "override", Listen: ":9443", PathPattern: "/single", MatchOrdinal: 0},
	}); got != "d-single" {
		t.Fatalf("single-vhost fallback digest = %q, want d-single", got)
	}
	if got := servingPolicyDigestForEvent(effDigestTest, waf.TransactionEvent{
		Route: waf.RouteAttribution{Scope: "override", Listen: ":9999", PathPattern: "/none"},
	}); got != "__missing_override__" {
		t.Fatalf("missing override digest = %q, want __missing_override__", got)
	}

	// Item 11: Verify all four canonical sharing modes (preview, private_network,
	// public, restricted) in Area 6 ("intent"):
	// - preview on loopback (127.0.0.1:8080) -> no warning
	// - preview on non-loopback (0.0.0.0:8080) -> intent_preview_not_loopback (warning, configured)
	// - private_network on private bind (10.0.0.10:8080) -> no warning
	// - private_network on wildcard bind (0.0.0.0:8081) -> intent_private_bind_too_wide (warning, configured)
	// - private_network with external_enforcer -> sharing_external_enforcer_unverified (info, external_unknown)
	vpnEnf := "corp-wireguard"
	intentCfg := &config.Config{
		Servers: []config.ServerConfig{
			{
				Listen: "127.0.0.1:8080",
				Locations: []config.LocationConfig{
					{
						Match:   config.MatchConfig{Type: "prefix", Path: "/preview-ok"},
						Return:  200,
						Sharing: &config.SharingConfig{Mode: config.SharingModePreview},
					},
				},
			},
			{
				Listen: "10.0.0.10:8080",
				Locations: []config.LocationConfig{
					{
						Match:  config.MatchConfig{Type: "prefix", Path: "/priv-ok"},
						Return: 200,
						Sharing: &config.SharingConfig{
							Mode:             config.SharingModePrivateNetwork,
							ExternalEnforcer: &vpnEnf,
						},
					},
				},
			},
			{
				Listen: "0.0.0.0:8080",
				Locations: []config.LocationConfig{
					{
						Match:   config.MatchConfig{Type: "prefix", Path: "/preview-bad"},
						Return:  200,
						Sharing: &config.SharingConfig{Mode: config.SharingModePreview},
					},
					{
						Match:   config.MatchConfig{Type: "prefix", Path: "/priv-bad"},
						Return:  200,
						Sharing: &config.SharingConfig{Mode: config.SharingModePrivateNetwork},
					},
				},
			},
		},
	}
	intentResp := EvaluateSecurityPostureOffline(intentCfg, intentCfg, true)
	intentByCode := make(map[string]adminapi.SecurityPostureFinding)
	for _, f := range intentResp.Findings {
		intentByCode[f.Code] = f
	}
	for _, wantCode := range []string{
		config.FindingIntentPreviewNotLoopback,
		config.FindingIntentPrivateBindTooWide,
		"sharing_external_enforcer_unverified",
	} {
		f, ok := intentByCode[wantCode]
		if !ok {
			t.Fatalf("missing expected intent finding %q in %+v", wantCode, intentResp.Findings)
		}
		if f.Area != "intent" {
			t.Fatalf("finding %q area = %q, want intent", wantCode, f.Area)
		}
	}
	if intentByCode[config.FindingIntentPreviewNotLoopback].Severity != "warning" ||
		intentByCode[config.FindingIntentPreviewNotLoopback].EpistemicState != "configured" {
		t.Fatalf("unexpected preview finding metadata: %+v", intentByCode[config.FindingIntentPreviewNotLoopback])
	}
	if intentByCode[config.FindingIntentPrivateBindTooWide].Severity != "warning" ||
		intentByCode[config.FindingIntentPrivateBindTooWide].EpistemicState != "configured" {
		t.Fatalf("unexpected private_network finding metadata: %+v", intentByCode[config.FindingIntentPrivateBindTooWide])
	}
	var intentArea *adminapi.SecurityPostureAreaSummary
	for i := range intentResp.Areas {
		if intentResp.Areas[i].Area == "intent" {
			intentArea = &intentResp.Areas[i]
			break
		}
	}
	if intentArea == nil || intentArea.Status != "warning" || intentArea.WarningCount < 2 {
		t.Fatalf("expected intent area summary status=warning warning_count>=2, got %+v", intentArea)
	}

	// P1: Verify that header/query predicates using ${env:...} and ${file:...}
	// secret values are projected as non-disclosing keyed ev1: digests on both
	// GET /api/security and GET /api/v1/waf/events, never exposing the resolved
	// secret to config:read or observability:read callers while preserving distinct
	// predicate identities across sibling routes.
	const envSecretCanary = "canary-env-predicate-secret-7777"
	const fileSecretCanary = "canary-file-predicate-secret-8888"
	t.Setenv("JUL_TEST_PRED_ENV_SECRET", envSecretCanary)
	secretFilePath := filepath.Join(dir, "pred-secret.txt")
	if err := os.WriteFile(secretFilePath, []byte(fileSecretCanary), 0o600); err != nil {
		t.Fatal(err)
	}
	rawPredTOML := `[waf]
enabled = true
mode = "block"
crs_enabled = true

[[servers]]
listen = ":8443"
server_names = ["api.example.test"]

  [[servers.locations]]
  match = { type = "prefix", path = "/api", headers = [{ name = "X-App-Key", op = "exact", value = "${env:JUL_TEST_PRED_ENV_SECRET}" }] }
  return = 200

  [[servers.locations]]
  match = { type = "prefix", path = "/api", headers = [{ name = "X-App-Key", op = "exact", value = "${file:` + filepath.ToSlash(secretFilePath) + `}" }] }
  return = 200
`
	rawPredCfg, err := config.Parse([]byte(rawPredTOML))
	if err != nil {
		t.Fatalf("Parse rawPredTOML: %v", err)
	}
	candPred, err := config.NewCandidate(rawPredCfg)
	if err != nil {
		t.Fatalf("NewCandidate rawPredTOML: %v", err)
	}
	vCtx := versionid.Process()
	invPred := waf.NewInvestigationStore(16, time.Minute)
	invPred.Record(waf.TransactionEvent{
		Outcome:      waf.OutcomeInterruptedRequest,
		Mode:         "block",
		ClientStatus: 403,
		Method:       "GET",
		Host:         "api.example.test",
		Path:         "/api/items",
		Generation:   1,
		Route: waf.RouteAttribution{
			Listen:              ":8443",
			ServerNames:         []string{"api.example.test"},
			MatchType:           "prefix",
			PathPattern:         "/api",
			MatchOrdinal:        0,
			Scope:               "global",
			CanonicalPredicates: vCtx.RoutePredicates(candPred.Raw.Servers[0].Locations[0].Match),
		},
	})
	sPred := newTestServer(t, config.AdminConfig{}, Deps{
		EffectiveVersions: vCtx,
		ReadConfigRaw:     func() ([]byte, error) { return []byte(rawPredTOML), nil },
		LoadConfig:        func() (*config.Config, error) { return candPred.Raw, nil },
		WAFCompiled:       true,
		WAFEffective: func() *WAFEffectivePolicy {
			return &WAFEffectivePolicy{
				Compiled:           true,
				Generation:         1,
				GlobalEnabled:      true,
				InheritingRoutes:   2,
				BlockRoutes:        2,
				ServingBaseVersion: server.CanonicalVersion(candPred.Raw),
				ServingRoutes: []WAFEditableRouteProjection{
					{
						Listen:              ":8443",
						ServerNames:         []string{"api.example.test"},
						MatchType:           "prefix",
						Path:                "/api",
						MatchOrdinal:        0,
						SelectorVersion:     "global-v1",
						CanonicalPredicates: vCtx.RoutePredicates(candPred.Raw.Servers[0].Locations[0].Match),
						Scope:               "global",
						Enabled:             true,
					},
					{
						Listen:              ":8443",
						ServerNames:         []string{"api.example.test"},
						MatchType:           "prefix",
						Path:                "/api",
						MatchOrdinal:        1,
						SelectorVersion:     "global-v1",
						CanonicalPredicates: vCtx.RoutePredicates(candPred.Raw.Servers[0].Locations[1].Match),
						Scope:               "global",
						Enabled:             true,
					},
				},
			}
		},
		WAFInvestigation: func(f waf.InvestigationFilter) waf.InvestigationSnapshot {
			return invPred.Snapshot(time.Now().UTC(), f)
		},
	})

	reqSec := httptest.NewRequest(http.MethodGet, "/api/security", nil)
	reqSec = reqSec.WithContext(rbac.WithIdentity(reqSec.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigRead, rbac.ObservabilityRead},
	}))
	recSec := httptest.NewRecorder()
	sPred.handleSecurity(recSec, reqSec)
	secBody := recSec.Body.String()
	if strings.Contains(secBody, envSecretCanary) || strings.Contains(secBody, fileSecretCanary) {
		t.Fatalf("GET /api/security leaked resolved header predicate secret: %s", secBody)
	}
	var secProj SecurityProjection
	if err := json.Unmarshal(recSec.Body.Bytes(), &secProj); err != nil {
		t.Fatalf("decode SecurityProjection: %v", err)
	}
	if len(secProj.WAFRoutes) != 2 ||
		!strings.HasPrefix(secProj.WAFRoutes[0].CanonicalPredicates, "ev1:") ||
		!strings.HasPrefix(secProj.WAFRoutes[1].CanonicalPredicates, "ev1:") ||
		secProj.WAFRoutes[0].CanonicalPredicates == secProj.WAFRoutes[1].CanonicalPredicates {
		t.Fatalf("expected two distinct keyed ev1: canonical_predicates, got %+v", secProj.WAFRoutes)
	}
	if secProj.BaseVersion != secProj.WAFEffective.ServingBaseVersion {
		t.Fatalf("BaseVersion %q != ServingBaseVersion %q", secProj.BaseVersion, secProj.WAFEffective.ServingBaseVersion)
	}

	reqEv := httptest.NewRequest(http.MethodGet, "/api/v1/waf/events", nil)
	reqEv = reqEv.WithContext(rbac.WithIdentity(reqEv.Context(), rbac.Identity{
		Principal:   "op",
		Role:        rbac.RoleOperator,
		Permissions: []rbac.Permission{rbac.StatusRead, rbac.ConfigRead, rbac.ObservabilityRead},
	}))
	recEv := httptest.NewRecorder()
	sPred.handleV1WAFEvents(recEv, reqEv)
	evBody := recEv.Body.String()
	if strings.Contains(evBody, envSecretCanary) || strings.Contains(evBody, fileSecretCanary) {
		t.Fatalf("GET /api/v1/waf/events leaked resolved header predicate secret: %s", evBody)
	}
	var evResp adminapi.WAFEventsResponse
	if err := json.Unmarshal(recEv.Body.Bytes(), &evResp); err != nil || len(evResp.Events) != 1 {
		t.Fatalf("decode WAFEventsResponse: %v (%+v)", err, evResp)
	}
	if evResp.Events[0].Route.CanonicalPredicates != secProj.WAFRoutes[0].CanonicalPredicates {
		t.Fatalf("event canonical_predicates %q != waf_routes[0] %q",
			evResp.Events[0].Route.CanonicalPredicates, secProj.WAFRoutes[0].CanonicalPredicates)
	}
}
