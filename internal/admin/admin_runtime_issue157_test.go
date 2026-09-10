// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

type issue157ReadTracker struct {
	reads int
}

func (b *issue157ReadTracker) Read(_ []byte) (int, error) {
	b.reads++
	return 0, io.EOF
}
func (b *issue157ReadTracker) Close() error { return nil }

func TestPluginUploadDisabledRejectsBeforeReadingBody(t *testing.T) {
	disabled := false
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &disabled,
		PluginUploadMaxSize: 32,
		PluginUploadDir:     t.TempDir(),
	}
	srv := New(cfg, testLogger(t), Deps{})
	body := &issue157ReadTracker{}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	req.Body = body
	rr := httptest.NewRecorder()

	srv.handlePluginUpload(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	if body.reads != 0 {
		t.Fatalf("disabled upload read request body %d time(s), want 0", body.reads)
	}
	status := srv.adminRuntimeStatus(req)
	if status == nil || status.LastUploadRejection != uploadRejectDisabled {
		t.Fatalf("last upload rejection = %#v, want %q", status, uploadRejectDisabled)
	}
}

func TestPrepareAdminRuntimeFailureDoesNotPublish(t *testing.T) {
	disabled := false
	oldCfg := config.AdminConfig{
		Enabled:             true,
		Token:               "old-token",
		PluginUploadEnabled: &disabled,
		PluginUploadMaxSize: 32,
		PluginUploadDir:     t.TempDir(),
	}
	srv := New(oldCfg, testLogger(t), Deps{})
	before := srv.currentAuth()

	base := t.TempDir()
	badPath := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(badPath, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	candidate := oldCfg
	candidate.Token = "candidate-token"
	candidate.PluginUploadEnabled = &enabled
	candidate.PluginUploadDir = badPath
	prepared := PrepareAuth(candidate, nil)

	if _, err := srv.PrepareAdminRuntime(candidate, prepared); err == nil {
		t.Fatal("PrepareAdminRuntime accepted an unusable upload directory")
	}
	after := srv.currentAuth()
	if after != before {
		t.Fatal("failed PrepareAdminRuntime published a new admin generation")
	}
	if after.cfg.Token != "old-token" {
		t.Fatalf("live auth changed after failed prepare: token=%q", after.cfg.Token)
	}
	status := srv.adminRuntimeStatus(nil)
	if status == nil || status.PreparationFailure != adminPrepareFailureUploadDirectory {
		t.Fatalf("preparation failure status = %#v, want %q", status, adminPrepareFailureUploadDirectory)
	}
}

func TestAdminRuntimeSettingsProjectionUsesPinnedGeneration(t *testing.T) {
	on := true
	off := false
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	oldCfg := config.AdminConfig{
		Enabled:             true,
		Console:             &on,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 7,
		PluginUploadDir:     dirA,
	}
	srv := New(oldCfg, testLogger(t), Deps{})
	oldSnap := srv.currentAuth()
	req := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	req = withAdminRuntimeSnapshot(req, oldSnap)

	newCfg := oldCfg
	newCfg.Console = &off
	newCfg.PluginUploadEnabled = &off
	newCfg.PluginUploadMaxSize = 1
	newCfg.PluginUploadDir = dirB
	srv.UpdateLiveAdminConfig(newCfg)

	rr := httptest.NewRecorder()
	srv.handleAdminRuntimeSettingsRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got AdminRuntimeSettingsProjection
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Console || !got.PluginUploadEnabled || got.PluginUploadMaxSizeMB != 7 || got.PluginUploadDir != dirA {
		t.Fatalf("projection mixed generations: %#v", got)
	}

	newReq := httptest.NewRequest(http.MethodGet, "/api/config/settings", nil)
	newRR := httptest.NewRecorder()
	srv.handleAdminRuntimeSettingsRead(newRR, newReq)
	if err := json.Unmarshal(newRR.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Console || got.PluginUploadEnabled || got.PluginUploadMaxSizeMB != 1 || got.PluginUploadDir != dirB {
		t.Fatalf("new request did not observe published generation: %#v", got)
	}
}

func TestAdminRuntimeStatusDoesNotExposeSensitiveOrHighCardinalityValues(t *testing.T) {
	on := true
	dir := filepath.Join(t.TempDir(), "operator-secret-looking-directory")
	cfg := config.AdminConfig{
		Enabled:             true,
		Token:               "super-secret-token-value",
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 9,
		PluginUploadDir:     dir,
	}
	srv := New(cfg, testLogger(t), Deps{})
	srv.recordPluginUploadRejection(uploadRejectInvalidFilename)

	data, err := json.Marshal(srv.adminRuntimeStatus(nil))
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	for _, forbidden := range []string{cfg.Token, dir, "operator-secret-looking-directory"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("runtime status leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, uploadRejectInvalidFilename) {
		t.Fatalf("runtime status omitted bounded rejection category: %s", body)
	}
}

func TestAdminRuntimeTypedPatchesAreNarrowAndSparse(t *testing.T) {
	var cfg config.Config
	on := true
	summary, err := applyPatch(&cfg, patchRequest{Op: "admin_console_set", Enabled: &on})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Admin.ConsoleEnabled() || !strings.Contains(summary, "Console enabled") {
		t.Fatalf("console patch not applied: summary=%q cfg=%#v", summary, cfg.Admin)
	}

	enabled := false
	max := 12
	dir := "/tmp/issue157-do-not-log-this-value"
	summary, err = applyPatch(&cfg, patchRequest{
		Op: "admin_plugin_upload_set",
		AdminPluginUpload: &adminPluginUploadPatch{
			Enabled:   &enabled,
			MaxSizeMB: &max,
			Directory: &dir,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.PluginUploadEnabled == nil || *cfg.Admin.PluginUploadEnabled || cfg.Admin.PluginUploadMaxSize != max || cfg.Admin.PluginUploadDir != dir {
		t.Fatalf("upload patch not applied: %#v", cfg.Admin)
	}
	if strings.Contains(summary, dir) {
		t.Fatalf("audit/preview summary leaked upload directory: %q", summary)
	}
	if _, err := applyPatch(&cfg, patchRequest{Op: "admin_plugin_upload_set", AdminPluginUpload: &adminPluginUploadPatch{}}); err == nil {
		t.Fatal("empty sparse upload patch accepted")
	}
	bad := -1
	if _, err := applyPatch(&cfg, patchRequest{Op: "admin_plugin_upload_set", AdminPluginUpload: &adminPluginUploadPatch{MaxSizeMB: &bad}}); err == nil {
		t.Fatal("negative upload max accepted")
	}
}

func TestPluginUploadMaxLimitIsPinnedAcrossPublish(t *testing.T) {
	on := true
	dir := t.TempDir()
	oldCfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 2,
		PluginUploadDir:     dir,
	}
	srv := New(oldCfg, testLogger(t), Deps{})
	payload := make([]byte, 1200*1024)
	copy(payload, wasmMagic)
	payload[4] = 0x01

	oldReq := runtimeUploadRequest(t, "old-limit.wasm", payload)
	oldReq = withAdminRuntimeSnapshot(oldReq, srv.currentAuth())
	newCfg := oldCfg
	newCfg.PluginUploadMaxSize = 1
	srv.UpdateLiveAdminConfig(newCfg)

	oldRR := httptest.NewRecorder()
	srv.handlePluginUpload(oldRR, oldReq)
	if oldRR.Code != http.StatusOK {
		t.Fatalf("old in-flight request status = %d, want 200; body=%s", oldRR.Code, oldRR.Body.String())
	}

	newRR := httptest.NewRecorder()
	srv.handlePluginUpload(newRR, runtimeUploadRequest(t, "new-limit.wasm", payload))
	if newRR.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("new request status = %d, want 413; body=%s", newRR.Code, newRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "new-limit.wasm")); !os.IsNotExist(err) {
		t.Fatalf("oversized new request created final file: err=%v", err)
	}
}

func TestPluginUploadDisablePreservesExistingFiles(t *testing.T) {
	on := true
	off := false
	dir := t.TempDir()
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     dir,
	}
	srv := New(cfg, testLogger(t), Deps{})
	data := append(append([]byte{}, wasmMagic...), 0x01, 0x00, 0x00, 0x00, 0x7f)
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, runtimeUploadRequest(t, "kept.wasm", data))
	if rr.Code != http.StatusOK {
		t.Fatalf("initial upload status = %d; body=%s", rr.Code, rr.Body.String())
	}

	cfg.PluginUploadEnabled = &off
	srv.UpdateLiveAdminConfig(cfg)
	rr = httptest.NewRecorder()
	srv.handlePluginUpload(rr, runtimeUploadRequest(t, "blocked.wasm", data))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("disabled upload status = %d, want 403", rr.Code)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.wasm")); err != nil {
		t.Fatalf("disabling uploads removed existing file: %v", err)
	}
}
