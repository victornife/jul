// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/rbac"
)

// getV1 issues an authenticated GET against a v1 route.
func getV1(t *testing.T, s *Server, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	s.routes().ServeHTTP(rr, req)
	return rr
}

func decodeInto[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, rr.Body.String())
	}
	return out
}

// TestV1StatusReportsControlPlaneState covers the projection GET
// /api/v1/status exists for: what is serving, what is on disk, who owns the
// configuration, and where the last transaction got to.
func TestV1StatusReportsControlPlaneState(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "tok-32-chars-padded-------------"}, Deps{
		Ready: func() bool { return true },
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{
				Mode:            "managed",
				Source:          "explicit",
				ConfigState:     "managed_drift",
				Drift:           true,
				DriftDetectedAt: time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC),
				BaselineVersion: "1c0d5e9a77b34f21",
				DiskVersion:     "9f2c1ab7d4e05863",
				DiskRawDigest:   "aabbccddeeff0011",
			}
		},
		PendingRestart: func() *PendingRestartStatus {
			return &PendingRestartStatus{
				State: "managed_staged", StagedVersion: "9f2c1ab7d4e05863",
				Subsystems: []string{"cache", "listener"}, DiscardAvailable: true,
			}
		},
		LastManagedApply: func() *ManagedApplyOutcome {
			return &ManagedApplyOutcome{
				ID: "rl_9f2c1ab74e60_41", Mode: "hot", OK: true, Outcome: "applied_live",
				CompletedAt: time.Date(2026, 8, 31, 11, 0, 0, 0, time.UTC),
			}
		},
	})

	rr := getV1(t, s, "/api/v1/status", "tok-32-chars-padded-------------")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[adminapi.StatusResponse](t, rr)

	if got.APIVersion != "v1" {
		t.Errorf("api_version = %q", got.APIVersion)
	}
	if !got.Ready {
		t.Error("ready = false")
	}
	if got.ConfigAuthority != "managed" || got.ConfigAuthoritySource != "explicit" {
		t.Errorf("authority = %q/%q", got.ConfigAuthority, got.ConfigAuthoritySource)
	}
	if got.ConfigState != "managed_drift" {
		t.Errorf("config_state = %q", got.ConfigState)
	}
	if !got.Drift.Detected || got.Drift.BaselineVersion != "1c0d5e9a77b34f21" {
		t.Errorf("drift = %+v", got.Drift)
	}
	if got.Drift.DetectedAt != "2026-08-31T12:00:00Z" {
		t.Errorf("detected_at = %q; §24a requires RFC 3339 with a Z offset", got.Drift.DetectedAt)
	}
	if got.PersistedVersion != "9f2c1ab7d4e05863" {
		t.Errorf("persisted_version = %q", got.PersistedVersion)
	}
	if !got.PendingRestart.Pending || got.PendingRestart.State != "managed_staged" {
		t.Errorf("pending_restart = %+v", got.PendingRestart)
	}
	if got.LastApply == nil || got.LastApply.ApplyID != "rl_9f2c1ab74e60_41" {
		t.Fatalf("last_apply = %+v", got.LastApply)
	}
	if got.LastApply.Outcome != "applied_live" {
		t.Errorf("outcome = %q", got.LastApply.Outcome)
	}
	if got.BootID == "" {
		t.Error("boot_id is empty; a client cannot detect a lost replay window without it")
	}
	if got.LedgerRetention.MinTerminalRecords != 512 || got.LedgerRetention.MinAgeSeconds != 3600 {
		t.Errorf("ledger_retention = %+v", got.LedgerRetention)
	}
	if got.LedgerRetention.Policy != "evict_after_both" {
		t.Errorf("policy = %q; the bounds are minimum guarantees, not caps", got.LedgerRetention.Policy)
	}
}

func TestV1StatusReportsInspectionLimitsAndPendingIntegration(t *testing.T) {
	snapshot := inspection.Snapshot{
		Limits: inspection.Limits{
			Mode:                      inspection.Enforce,
			MaxBytes:                  256 << 20,
			MaxResponseHeaderBytes:    64 << 10,
			ConfiguredEvaluationSlots: 0,
			EvaluationSlots:           4,
			EvaluationQueueTimeout:    time.Second,
			BodyTimeout:               60 * time.Second,
			BaseChargeBytes:           inspection.BaseChargeBytes,
		},
		Usage:              [4]uint64{inspection.BaseChargeBytes, 8192, 4096, 2048},
		ActiveLeases:       1,
		ActiveEvaluations:  2,
		WaitingEvaluations: 1,
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		InspectionSnapshot: func() inspection.Snapshot { return snapshot },
		InspectionPluginGuestCapacity: func() (adminapi.InspectionPluginGuestCapacity, error) {
			return adminapi.InspectionPluginGuestCapacity{
				MaxPermittedBytes: 56 << 20,
				LiveInstances:     3,
				MaxInstances:      5,
			}, nil
		},
	})
	got := decodeInto[adminapi.StatusResponse](t, getV1(t, s, "/api/v1/status", ""))
	if got.Inspection == nil {
		t.Fatal("inspection status is missing")
	}
	i := got.Inspection
	if i.Configured.MaxConcurrentEvaluations != 0 || i.Effective.MaxConcurrentEvaluations != 4 ||
		i.Configured.Mode != "enforce" || i.Effective.Mode != "enforce" ||
		i.Configured.MaxResponseHeaderBytes != 64<<10 || i.Effective.MaxResponseHeaderBytes != 64<<10 {
		t.Fatalf("configured/effective inspection limits = %+v/%+v", i.Configured, i.Effective)
	}
	if i.Usage.BaseBytes != inspection.BaseChargeBytes || i.Usage.BufferBytes != 8192 ||
		i.Usage.WAFStateBytes != 4096 || i.ActiveLeases != 1 ||
		i.ActiveEvaluations != 2 || i.WaitingEvaluations != 1 {
		t.Fatalf("inspection usage = %+v", i)
	}
	if i.RequestPathIntegration != "partial" || i.BaseChargeBytes != inspection.BaseChargeBytes ||
		!strings.Contains(i.BaseChargeScope, "excludes") {
		t.Fatalf("inspection delivery boundary = %+v", i)
	}
	if i.PluginGuestMaxBytes == nil || *i.PluginGuestMaxBytes != 56<<20 ||
		i.PluginGuestLiveInstances == nil || *i.PluginGuestLiveInstances != 3 ||
		i.PluginGuestMaxInstances == nil || *i.PluginGuestMaxInstances != 5 ||
		i.PluginGuestActualBytes != nil || i.PluginGuestActualBytesState != "not_sampled" {
		t.Fatalf("plugin guest capacity status = %+v", i)
	}
	if i.EvaluationHeadroomEstimateBytes != nil || i.EvaluationHeadroomEstimateState != "not_calibrated" {
		t.Fatalf("evaluation headroom status = %+v", i)
	}
}

func TestV1StatusDoesNotRepresentUnavailableGuestCapacityAsZero(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		InspectionSnapshot: func() inspection.Snapshot { return inspection.Snapshot{} },
	})
	rr := getV1(t, s, "/api/v1/status", "")
	got := decodeInto[adminapi.StatusResponse](t, rr)
	if got.Inspection == nil {
		t.Fatal("inspection status is missing")
	}
	if got.Inspection.PluginGuestMaxBytes != nil ||
		got.Inspection.PluginGuestLiveInstances != nil ||
		got.Inspection.PluginGuestMaxInstances != nil ||
		got.Inspection.PluginGuestActualBytesState != "unavailable" {
		t.Fatalf("unavailable plugin guest capacity = %+v", got.Inspection)
	}
	if strings.Contains(rr.Body.String(), `"plugin_guest_max_bytes"`) {
		t.Fatalf("unavailable guest capacity must be omitted, not encoded as zero: %s", rr.Body.String())
	}
}

func TestV1StatusSurfacesGuestCapacityFailure(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		InspectionSnapshot: func() inspection.Snapshot { return inspection.Snapshot{} },
		InspectionPluginGuestCapacity: func() (adminapi.InspectionPluginGuestCapacity, error) {
			return adminapi.InspectionPluginGuestCapacity{}, errors.New("injected capacity failure")
		},
	})
	rr := getV1(t, s, "/api/v1/status", "")
	if rr.Code != http.StatusInternalServerError ||
		!strings.Contains(rr.Body.String(), "could not report plugin guest capacity") {
		t.Fatalf("guest capacity failure response = %d %s", rr.Code, rr.Body.String())
	}
}

// TestV1StatusDegradedIsPresentAndEmpty. ADR 0019 §33.2 requires the array to
// be present and empty on a clean success so a script can test it
// unconditionally rather than checking whether the key exists.
func TestV1StatusDegradedIsPresentAndEmpty(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LastManagedApply: func() *ManagedApplyOutcome {
			return &ManagedApplyOutcome{ID: "rl_aaaaaaaaaaaa_1", Outcome: "applied_live", OK: true}
		},
	})
	rr := getV1(t, s, "/api/v1/status", "")
	if !strings.Contains(rr.Body.String(), `"degraded":[]`) {
		t.Fatalf("degraded must be present and empty on a clean success: %s", rr.Body.String())
	}
}

// TestV1StatusSurfacesDegradationsWithoutChangingTheOutcome is §33.2's
// cross-product rule: "did the change take effect" and "is anything unhealthy"
// are independent questions, and a provenance failure must not rewrite the
// terminal outcome.
func TestV1StatusSurfacesDegradationsWithoutChangingTheOutcome(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		LastManagedApply: func() *ManagedApplyOutcome {
			return &ManagedApplyOutcome{
				ID: "rl_aaaaaaaaaaaa_2", Outcome: "not_applied", OK: false,
				HistoryError:      "history snapshot could not be written",
				FinalizationError: "finalization failed",
			}
		},
	})
	got := decodeInto[adminapi.StatusResponse](t, getV1(t, s, "/api/v1/status", ""))
	if got.LastApply.Outcome != "not_applied" {
		t.Fatalf("outcome = %q; a degradation never upgrades or downgrades it", got.LastApply.Outcome)
	}
	kinds := map[string]bool{}
	for _, d := range got.LastApply.Degraded {
		kinds[d.Kind] = true
	}
	if !kinds["history_error"] || !kinds["finalization_error"] {
		t.Fatalf("degraded = %+v", got.LastApply.Degraded)
	}
}

// TestV1CapabilitiesDescribesTheBuild covers §30: a client must not have to
// infer capability from an error.
func TestV1CapabilitiesDescribesTheBuild(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := getV1(t, s, "/api/v1/capabilities", "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	got := decodeInto[adminapi.CapabilitiesResponse](t, rr)

	if got.APIVersion != "v1" {
		t.Errorf("api_version = %q", got.APIVersion)
	}
	if got.ConfigSchemaVersion == 0 {
		t.Error("config_schema_version is unset")
	}
	if got.BootID == "" {
		t.Error("boot_id is empty")
	}

	// Every external route, and nothing else, is described.
	published := map[string]adminapi.EndpointAvailability{}
	for _, e := range got.Endpoints {
		published[e.Path] = e
	}
	for _, r := range ExternalRoutes() {
		e, ok := published[r.Pattern]
		if !ok {
			t.Errorf("external route %s is not published in capabilities", r.Pattern)
			continue
		}
		if !e.Available {
			t.Errorf("%s reports unavailable in a build that serves it", r.Pattern)
		}
		if e.Stability != r.Stability.String() {
			t.Errorf("%s stability = %q, catalog says %q", r.Pattern, e.Stability, r.Stability)
		}
	}
	for pattern := range InternalRouteReasons() {
		if _, leaked := published[pattern]; leaked {
			t.Errorf("internal route %s is published in capabilities", pattern)
		}
	}
	if _, ok := published["/api/v1/status"]; !ok {
		t.Error("capabilities does not describe /api/v1/status")
	}
}

// TestV1CapabilitiesAgreesWithTheJulCLI pins ADR 0019 §30's reason for
// publishing the build flags at all: `jul capabilities` and the API must agree,
// which is only guaranteed while both read one source.
func TestV1CapabilitiesAgreesWithTheJulCLI(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	got := decodeInto[adminapi.CapabilitiesResponse](t, getV1(t, s, "/api/v1/capabilities", ""))

	named := got.Build.Named()
	if len(named) != 13 {
		t.Fatalf("the build reports %d flags, the contract publishes 13", len(named))
	}
	// The API's flags are the running binary's, which is what `jul
	// capabilities` prints from the same package.
	for _, f := range named {
		if f.Name == "" {
			t.Error("a build flag has no name")
		}
	}
}

// TestV1FailuresUseTheErrorEnvelope is the property that makes the v1 surface
// usable by a machine: one shape for every failure, with the request id echoed
// in the header, on a route that shares its authentication middleware with the
// internal routes.
func TestV1FailuresUseTheErrorEnvelope(t *testing.T) {
	tok := "correct-token-32-chars-padded---"
	s := newTestServer(t, config.AdminConfig{Token: tok}, Deps{})

	t.Run("unauthenticated", func(t *testing.T) {
		rr := getV1(t, s, "/api/v1/status", "")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rr.Code)
		}
		env := decodeEnvelope(t, rr)
		if env.Error.Code != adminapi.CodeUnauthenticated {
			t.Fatalf("code = %q", env.Error.Code)
		}
		if env.Error.RequestID == "" || rr.Header().Get("X-Request-ID") != env.Error.RequestID {
			t.Fatalf("request id missing or not echoed: header=%q body=%q",
				rr.Header().Get("X-Request-ID"), env.Error.RequestID)
		}
		if strings.Contains(rr.Body.String(), tok) {
			t.Fatal("the refusal echoed the credential")
		}
	})

	t.Run("wrong method", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/status", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		s.routes().ServeHTTP(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 invalid_request", rr.Code)
		}
		if env := decodeEnvelope(t, rr); env.Error.Code != adminapi.CodeInvalidRequest {
			t.Fatalf("code = %q", env.Error.Code)
		}
	})
}

// TestV1ForbiddenCarriesTheRequiredPermissionOnly. §28's 403 must not become an
// existence oracle, and §26's details must not carry the principal or the role
// — those are Console affordances, not published contract.
func TestV1ForbiddenCarriesTheRequiredPermissionOnly(t *testing.T) {
	viewerless := "no-status-read-token-32-chars---"
	pol, err := rbac.Build(true, "admin",
		map[string][]string{"nostatus": {"history:read"}},
		[]rbac.PrincipalDef{
			{Name: "limited", Role: "nostatus", Token: viewerless},
			// A policy must retain at least one admin-capable principal.
			{Name: "admin-user", Role: rbac.RoleAdmin, Token: "admin-token-32-chars-padded-----"},
		}, "", time.Now())
	if err != nil {
		t.Fatalf("build policy: %v", err)
	}
	s := newTestServer(t, config.AdminConfig{RBAC: config.AdminRBACConfig{Enabled: true}}, Deps{})
	s.UpdatePolicy(pol)

	rr := getV1(t, s, "/api/v1/status", viewerless)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rr.Code, rr.Body.String())
	}
	env := decodeEnvelope(t, rr)
	if env.Error.Code != adminapi.CodeForbidden {
		t.Fatalf("code = %q", env.Error.Code)
	}
	if env.Error.Details.RequiredPermission != "status:read" {
		t.Fatalf("required_permission = %q", env.Error.Details.RequiredPermission)
	}
	body := rr.Body.String()
	for _, leak := range []string{"limited", "nostatus", viewerless} {
		if strings.Contains(body, leak) {
			t.Errorf("the refusal disclosed %q: %s", leak, body)
		}
	}
}

// TestInternalRoutesKeepTheirExistingErrorShapes is the other half of the
// envelope seam: one authentication implementation, two renderings. A change
// that gave the Console the envelope would be a silent breaking change.
func TestInternalRoutesKeepTheirExistingErrorShapes(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{Token: "tok-32-chars-padded-------------"}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
	if strings.Contains(rr.Body.String(), `"request_id"`) {
		t.Fatalf("an internal route rendered the external envelope: %s", rr.Body.String())
	}
	if rr.Header().Get("X-Request-ID") != "" {
		t.Fatal("an internal route minted an external request id")
	}
}

// TestV1StatusIsSideEffectFree: a read must not mutate, and must not be cached
// by an intermediary that would then answer a later request with stale
// control-plane state.
func TestV1StatusIsSideEffectFree(t *testing.T) {
	var applies int
	s := newTestServer(t, config.AdminConfig{}, Deps{
		ApplyConfigRaw: func(ApplyRequestContext, []byte, string) (ConfigApplyResult, error) {
			applies++
			return ConfigApplyResult{}, nil
		},
	})
	rr := getV1(t, s, "/api/v1/status", "")
	if applies != 0 {
		t.Fatalf("a read triggered %d applies", applies)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
