// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/rbac"
)

type inspectionBoundaryFake struct {
	inspectionFake
	inspect  func(*cache.Inspection)
	sequence func() uint64
}

func (f *inspectionBoundaryFake) InspectScope(ctx context.Context, sel cache.NormalizedSelector) (cache.Inspection, error) {
	d, err := f.inspectionFake.InspectScope(ctx, sel)
	if f.inspect != nil {
		f.inspect(&d)
	}
	return d, err
}
func (f *inspectionBoundaryFake) InspectionSequence() uint64 {
	if f.sequence != nil {
		return f.sequence()
	}
	return 0
}

func TestInspectionCaptureAndDeliveryBoundaries(t *testing.T) {
	for _, scenario := range []string{"capture-reload", "delivery-reload", "expired-capture", "snapshot-bytes", "single-row-bytes", "invalid-timestamp", "changed-sequence"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
			reload := func() { next := *cfg; next.Cache.MaxEntrySize++; cfg = &next }
			f := &inspectionBoundaryFake{}
			want := http.StatusBadRequest
			switch scenario {
			case "capture-reload":
				f.inspect = func(*cache.Inspection) { reload() }
			case "delivery-reload":
				f.sequence = func() uint64 { reload(); return 1 }
			case "expired-capture":
				f.inspect = func(d *cache.Inspection) {
					d.Decisions = []cache.CacheDecision{{ExpiresAt: time.Now().Add(-time.Second)}}
				}
			case "invalid-timestamp":
				want = http.StatusServiceUnavailable
				f.inspect = func(d *cache.Inspection) { d.CapturedAt = time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC) }
			case "snapshot-bytes":
				want = http.StatusServiceUnavailable
				f.inspect = func(d *cache.Inspection) { d.Resources[0].Path = strings.Repeat("x", maxInspectionSnapshotBytes) }
			case "single-row-bytes":
				want = http.StatusServiceUnavailable
				f.inspect = func(d *cache.Inspection) { d.Resources[0].Path = strings.Repeat("x", maxInspectionPageBytes) }
			case "changed-sequence":
				want = http.StatusOK
				f.sequence = func() uint64 { return 1 }
			}
			const token = "inspection-boundary-token-32-chars-aa"
			s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: f, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
			rr := postV1JSON(t, s, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "host", Host: "example.com"}, PageSize: 1})
			if rr.Code != want {
				t.Fatalf("status=%d: %s", rr.Code, rr.Body.String())
			}
			if scenario == "changed-sequence" {
				if v := decodeInto[adminapi.CacheInspectResponse](t, rr); v.ConcurrentChange != "observed_change_since_capture" {
					t.Fatal(v.ConcurrentChange)
				}
			} else {
				s.inspectionMu.Lock()
				retained := len(s.inspectionSnapshots)
				s.inspectionMu.Unlock()
				if retained != 0 {
					t.Fatalf("refused response retained %d snapshots", retained)
				}
			}
		})
	}
}

func TestInspectionMalformedInputAndExactKeyScope(t *testing.T) {
	cfg := &config.Config{Cache: config.CacheConfig{Enabled: true}, Servers: []config.ServerConfig{{ServerNames: []string{"example.com"}}}}
	const token = "inspection-boundary-token-32-chars-aa"
	s := newTestServer(t, config.AdminConfig{Token: token}, Deps{Cache: &inspectionFake{}, LoadConfig: func() (*config.Config, error) { return cfg, nil }})
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cache/inspect", strings.NewReader(`{"selector":"SECRET_INVALID_JSON"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, r)
	if rr.Code != 400 || strings.Contains(rr.Body.String(), "SECRET_INVALID_JSON") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	s.handleV1CacheInspect(rr, httptest.NewRequest(http.MethodGet, "/api/v1/cache/inspect", nil))
	if rr.Code != 400 {
		t.Fatal(rr.Code)
	}
	rr = postV1JSON(t, s, "/api/v1/cache/inspect", token, "", adminapi.CacheInspectRequest{Selector: adminapi.CacheSelector{Kind: "key", Key: "GET\nexample.com\n/a?token=SECRET_QUERY"}})
	if rr.Code != 200 || strings.Contains(rr.Body.String(), "SECRET_QUERY") {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if configuredInspectionScope(cache.NormalizedSelector{Kind: cache.SelectorKey, Key: "invalid"}, cfg) {
		t.Fatal("malformed normalized key accepted")
	}
}

func TestInspectionContextIdentityCannotReplaceLiveCredentials(t *testing.T) {
	p, err := rbac.Build(true, rbac.RoleAdmin, nil, []rbac.PrincipalDef{{Name: "root", Role: rbac.RoleAdmin, Token: "root-inspection-boundary-token-32"}, {Name: "viewer", Role: rbac.RoleViewer, Token: "viewer-inspection-boundary-token-32"}}, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	s.UpdatePolicy(p)
	id := rbac.Identity{Principal: "viewer", Role: rbac.RoleViewer, Permissions: []rbac.Permission{rbac.StatusRead}}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/cache/purge", nil)
	r = r.WithContext(rbac.WithIdentity(r.Context(), id))
	rr := httptest.NewRecorder()
	if s.verifyLivePurgePermission(rr, r) || rr.Code != 403 {
		t.Fatal(rr.Code)
	}
	id.Permissions = []rbac.Permission{rbac.CacheInspect}
	r = r.WithContext(rbac.WithIdentity(r.Context(), id))
	rr = httptest.NewRecorder()
	if s.verifyLiveCachePermission(rr, r, rbac.CacheInspect) || rr.Code != 401 {
		t.Fatal("context identity admitted without a live credential", rr.Code)
	}
}

func TestInspectionSamplingOnlyConfigDiff(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		before := &config.Config{Cache: config.CacheConfig{Enabled: enabled}}
		after := *before
		after.Cache.DecisionSamples = true
		d := DiffConfigs(before, &after)
		n := 0
		for _, m := range d.Modifications {
			if m.Detail == "Change bounded cache decision sampling" {
				n++
			}
		}
		if n != 1 || !d.coveredPaths["cache.decision_samples"] {
			t.Fatalf("enabled=%v diff=%+v", enabled, d)
		}
	}
}
