// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestCacheCutoverRequestBinding(t *testing.T) {
	for _, tc := range []struct {
		query, body, apply, want string
		bad                      bool
	}{
		{"", "", "hot", "", false},
		{"cache_cutover=retain", "", "hot", "retain", false},
		{"", "invalidate", "hot", "invalidate", false},
		{"cache_cutover=invalidate", "invalidate", "hot", "invalidate", false},
		{"cache_cutover=retain", "invalidate", "hot", "", true},
		{"cache_cutover=retain&cache_cutover=retain", "", "hot", "", true},
		{"cache_cutover=bogus", "", "hot", "", true},
		{"", "retain", "stage_restart", "", true},
	} {
		t.Run(tc.query+tc.body+tc.apply, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/config/apply?"+tc.query, nil)
			err := bindCacheCutoverRequest(r, tc.body, tc.apply)
			if (err != nil) != tc.bad {
				t.Fatalf("err=%v bad=%t", err, tc.bad)
			}
			if !tc.bad && applyRequestContext(r, ApplyOperationConfigApply).CacheCutover != tc.want {
				t.Fatal("mode lost before coordinator")
			}
		})
	}
}

func TestMalformedCacheCutoverQueryRejected(t *testing.T) {
	for _, raw := range []string{"cache_cutover=invalidate%ZZ", "cache_cutover=invalidate;extra=x"} {
		for _, bodyMode := range []string{"", "retain", "invalidate"} {
			r := httptest.NewRequest(http.MethodPost, "/api/config/apply", nil)
			r.URL.RawQuery = raw
			if err := bindCacheCutoverRequest(r, bodyMode, "hot"); err == nil {
				t.Fatalf("malformed explicit override accepted: query=%q body=%q bound=%q", raw, bodyMode, config.CacheCutoverMode(r.Context()))
			}
			if config.CacheCutoverMode(r.Context()) != "" {
				t.Fatal("rejected request changed the publication option")
			}
		}
	}
}

func TestMalformedCacheCutoverQueryRejectedAcrossEndpoints(t *testing.T) {
	writes := 0
	s := newTestServer(t, config.AdminConfig{}, Deps{
		WriteConfigRaw: func([]byte) error { writes++; return nil },
		LoadConfig:     func() (*config.Config, error) { return &config.Config{}, nil },
	})
	const patch = `{"base_version":"v1","ops":[{"op":"server_add","listen":":9090"}]}`
	const rollback = `{"base_version":"v1","id":"snapshot"}`
	for _, endpoint := range []struct {
		path, method, contentType, body string
		handler                         http.HandlerFunc
	}{
		{"/api/config/apply", http.MethodPost, "application/toml", "[global]\n", s.handleConfigApply},
		{"/api/v1/config/apply", http.MethodPost, "application/toml", "[global]\n", s.handleV1ConfigApply},
		{"/api/config/preview", http.MethodPost, "application/toml", "[global]\n", s.handleConfigPreview},
		{"/api/v1/config/plan", http.MethodPost, "application/toml", "[global]\n", s.handleV1ConfigPlan},
		{"/api/config/diff", http.MethodPost, "application/toml", "[global]\n", s.handleConfigDiff},
		{"/api/config/patch/preview", http.MethodPost, "application/json", patch, s.handleConfigPatchPreview},
		{"/api/config/patch/candidate", http.MethodPost, "application/json", patch, s.handleConfigPatchCandidate},
		{"/api/config/patch/apply", http.MethodPost, "application/json", patch, s.handleConfigPatchApply},
		{"/api/v1/config/patch", http.MethodPost, "application/json", patch, s.handleV1ConfigPatch},
		{"/api/v1/config/patch/apply", http.MethodPost, "application/json", patch, s.handleV1ConfigPatchApply},
		{"/api/config/rollback", http.MethodPost, "application/json", rollback, s.handleConfigRollback},
		{"/api/v1/config/rollback", http.MethodPost, "application/json", rollback, s.handleV1Rollback},
		{"/api/config/history/snapshot/diff", http.MethodGet, "", "", s.handleConfigHistoryDiff},
		{"/api/v1/config/history/snapshot/diff", http.MethodGet, "", "", s.handleV1HistoryDiff},
	} {
		for _, raw := range []string{"cache_cutover=invalidate%ZZ", "cache_cutover=invalidate;extra=x"} {
			t.Run(endpoint.path+"/"+raw, func(t *testing.T) {
				r := httptest.NewRequest(endpoint.method, endpoint.path, strings.NewReader(endpoint.body))
				r.URL.RawQuery = raw
				r.Header.Set("Content-Type", endpoint.contentType)
				// No Idempotency-Key: admission must reject malformed options itself.
				w := httptest.NewRecorder()
				endpoint.handler(w, r)
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cache_cutover") {
					t.Fatalf("malformed option was not rejected: %d %s", w.Code, w.Body.String())
				}
			})
		}
	}
	if writes != 0 {
		t.Fatal("malformed request changed configuration")
	}
}

func TestInvalidCacheCutoverRejectedBeforeApply(t *testing.T) {
	writes := 0
	s := newTestServer(t, config.AdminConfig{}, Deps{WriteConfigRaw: func([]byte) error { writes++; return nil }, LoadConfig: func() (*config.Config, error) { return &config.Config{}, nil }})
	for _, path := range []string{"/api/config/apply?cache_cutover=bad", "/api/config/apply?mode=stage_restart&cache_cutover=retain", "/api/v1/config/apply?cache_cutover=bad"} {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("not valid TOML"))
		r.Header.Set("Content-Type", "application/toml")
		w := httptest.NewRecorder()
		if strings.Contains(path, "/v1/") {
			s.handleV1ConfigApply(w, r)
		} else {
			s.handleConfigApply(w, r)
		}
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cache_cutover") {
			t.Fatalf("%s got %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/config/patch/apply?mode=stage_restart", "/api/v1/config/patch/apply?mode=stage_restart", "/api/v1/config/patch", "/api/config/rollback", "/api/v1/config/rollback"} {
		body := `{"base_version":"v1","ops":[{"op":"server_add","listen":":9090"}],"cache_cutover":"bad"}`
		if strings.Contains(path, "rollback") {
			body = `{"base_version":"v1","id":"snapshot","cache_cutover":"bad"}`
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		switch {
		case strings.HasPrefix(path, "/api/config/patch"):
			s.handleConfigPatchApply(w, r)
		case strings.HasPrefix(path, "/api/v1/config/patch/apply"):
			s.handleV1ConfigPatchApply(w, r)
		case path == "/api/v1/config/patch":
			s.handleV1ConfigPatch(w, r)
		case path == "/api/config/rollback":
			s.handleConfigRollback(w, r)
		default:
			s.handleV1Rollback(w, r)
		}
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cache_cutover") {
			t.Fatalf("%s got %d %s", path, w.Code, w.Body.String())
		}
	}
	if writes != 0 {
		t.Fatal("invalid request mutated config")
	}
}

func TestCacheCutoverPreviewUsesOneChangeOverride(t *testing.T) {
	before := &config.Config{Cache: config.CacheConfig{Enabled: true, ReloadPolicy: "retain"}, Servers: []config.ServerConfig{{Listen: ":8080", ServerNames: []string{"example.com"}, Locations: []config.LocationConfig{{Match: config.MatchConfig{Path: "/", Type: "prefix"}, Cache: true, ProxyPass: "http://127.0.0.1:9001"}}}}}
	after := *before
	after.Servers = append([]config.ServerConfig(nil), before.Servers...)
	after.Servers[0].Locations = append([]config.LocationConfig(nil), before.Servers[0].Locations...)
	after.Servers[0].Locations[0].ProxyPass = "http://127.0.0.1:9002"
	for _, tc := range []struct{ mode, want string }{{"", "retain"}, {"retain", "retain"}, {"invalidate", "invalidate_on_publish"}} {
		diff := diffConfigs(before, &after, tc.mode)
		if diff.CacheCutover == nil || diff.CacheCutover.Disposition != tc.want {
			t.Fatalf("%s plan=%+v", tc.mode, diff.CacheCutover)
		}
	}
	if before.Cache.ReloadPolicy != "retain" || after.Cache.ReloadPolicy != "retain" {
		t.Fatal("ephemeral choice changed persisted config")
	}
}

func TestCacheCutoverRejectsUncorrelatedEmbeddingHooks(t *testing.T) {
	writes := 0
	s := newTestServer(t, config.AdminConfig{}, Deps{WriteConfigRaw: func([]byte) error { writes++; return nil }})
	for _, path := range []string{"/api/config/apply?cache_cutover=invalidate", "/api/config/patch/apply", "/api/config/rollback"} {
		body := `{"base_version":"v1","ops":[{"op":"server_add","listen":":9090"}],"cache_cutover":"retain"}`
		if strings.Contains(path, "rollback") {
			body = `{"id":"snapshot","cache_cutover":"invalidate"}`
		}
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		switch {
		case strings.Contains(path, "patch"):
			s.handleConfigPatchApply(w, r)
		case strings.Contains(path, "rollback"):
			s.handleConfigRollback(w, r)
		default:
			s.handleConfigApply(w, r)
		}
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "cache_cutover") {
			t.Fatalf("%s got %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/api/config/apply", "/api/v1/config/apply"} {
		r := httptest.NewRequest(http.MethodPost, path+"?cache_cutover=retain", nil)
		if err := bindCacheCutoverRequest(r, "", "hot"); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		if requireCacheCutoverSupport(w, r, false) || w.Code != http.StatusNotImplemented {
			t.Fatalf("unsupported mode accepted: %d", w.Code)
		}
		if !requireCacheCutoverSupport(httptest.NewRecorder(), r, true) {
			t.Fatal("supported mode refused")
		}
	}
	if !requireCacheCutoverSupport(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/config/apply", nil), false) {
		t.Fatal("default compatibility refused")
	}
	if writes != 0 {
		t.Fatal("unsupported request wrote configuration")
	}
}
