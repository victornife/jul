// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestAdminRuntimePatchValidationBranches(t *testing.T) {
	var cfg config.Config

	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{Op: "admin_console_set"}); err == nil {
		t.Fatal("admin_console_set accepted missing enabled")
	}

	off := false
	summary, err := applyAdminRuntimePatch(&cfg, patchRequest{Op: "admin_console_set", Enabled: &off})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.Console == nil || *cfg.Admin.Console || !strings.Contains(summary, "disabled") {
		t.Fatalf("console disable patch not applied: summary=%q cfg=%#v", summary, cfg.Admin)
	}

	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{Op: "admin_plugin_upload_set"}); err == nil {
		t.Fatal("admin_plugin_upload_set accepted missing payload")
	}

	blank := "   "
	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{
		Op:                "admin_plugin_upload_set",
		AdminPluginUpload: &adminPluginUploadPatch{Directory: &blank},
	}); err == nil {
		t.Fatal("admin_plugin_upload_set accepted blank directory")
	}

	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{Op: "admin_runtime_unknown"}); err == nil {
		t.Fatal("unsupported admin runtime patch operation accepted")
	}
}

func TestPrepareAdminRuntimeCompletesPreparedSnapshot(t *testing.T) {
	off := false
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &off,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     "  ./plugins  ",
	}
	srv := New(cfg, testLogger(t), Deps{PluginsCompiled: true})
	prepared := PrepareAuth(cfg, nil)
	got, err := srv.PrepareAdminRuntime(cfg, prepared)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.snapshot == nil {
		t.Fatal("prepared runtime snapshot is nil")
	}
	if !filepath.IsAbs(got.snapshot.cfg.PluginUploadDir) {
		t.Fatalf("upload dir was not normalized: %q", got.snapshot.cfg.PluginUploadDir)
	}
	if !got.snapshot.pluginsCompiled {
		t.Fatal("prepared runtime snapshot did not capture plugin compile capability")
	}
}

func TestPreflightConfigUsesReversibleUploadProbe(t *testing.T) {
	on := true
	candidate := filepath.Join(t.TempDir(), "future", "plugins")
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     candidate,
	}
	if err := PreflightConfig(cfg); err != nil {
		t.Fatalf("upload preflight failed: %v", err)
	}
	if _, err := os.Stat(candidate); !os.IsNotExist(err) {
		t.Fatalf("reversible preflight created candidate directory: %v", err)
	}
}

func TestHandleConfigSettingsReadAndMethodContract(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true}, testLogger(t), Deps{})

	readReq := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	readRec := httptest.NewRecorder()
	srv.handleConfigSettings(readRec, readReq)
	if readRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", readRec.Code, readRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/config/settings", nil)
	deleteRec := httptest.NewRecorder()
	srv.handleConfigSettings(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", deleteRec.Code)
	}
	if got := deleteRec.Header().Get("Allow"); got != "GET, POST, PUT" {
		t.Fatalf("Allow = %q, want GET, POST, PUT", got)
	}
}

func TestHandlePluginsProjectsPinnedEnabledUploadPolicy(t *testing.T) {
	on := true
	parsed := pluginPatchConfig()
	srv := newTestServer(t, config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 7,
		PluginUploadDir:     t.TempDir(),
	}, Deps{
		LoadConfig:      func() (*config.Config, error) { return parsed, nil },
		PluginsCompiled: true,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/plugins", nil)
	rec := httptest.NewRecorder()
	srv.handlePlugins(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"upload_enabled":true`) || !strings.Contains(body, `"upload_max_size_mb":7`) {
		t.Fatalf("pinned upload policy missing from projection: %s", body)
	}
}
