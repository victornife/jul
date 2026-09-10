// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
)

func runtimeUploadRequest(t *testing.T, name string, data []byte) *http.Request {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("wasm", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

func TestAdminRuntimeCapturedUploadGeneration(t *testing.T) {
	dirA := filepath.Join(t.TempDir(), "a")
	dirB := filepath.Join(t.TempDir(), "b")
	on := true
	off := false
	oldCfg := config.AdminConfig{
		Enabled: true, PluginUploadEnabled: &on, PluginUploadMaxSize: 1, PluginUploadDir: dirA,
	}
	srv := New(oldCfg, testLogger(t), Deps{})

	// Simulate a request that entered the stable mux before Publish.
	oldSnap := srv.currentAuth()
	data := append([]byte{}, wasmMagic...)
	data = append(data, 0x01, 0x00, 0x00, 0x00, 0x7f)
	oldReq := runtimeUploadRequest(t, "old.wasm", data)
	oldReq = oldReq.WithContext(context.WithValue(oldReq.Context(), adminRuntimeContextKey{}, oldSnap))

	newCfg := oldCfg
	newCfg.PluginUploadEnabled = &off
	newCfg.PluginUploadDir = dirB
	newCfg.PluginUploadMaxSize = 2
	srv.UpdateLiveAdminConfig(newCfg)

	oldRR := httptest.NewRecorder()
	srv.handlePluginUpload(oldRR, oldReq)
	if oldRR.Code != http.StatusOK {
		t.Fatalf("in-flight old generation status = %d; body=%s", oldRR.Code, oldRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirA, "old.wasm")); err != nil {
		t.Fatalf("old generation did not write entirely to A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "old.wasm")); !os.IsNotExist(err) {
		t.Fatalf("old generation leaked into B: err=%v", err)
	}

	newReq := runtimeUploadRequest(t, "new.wasm", data)
	newRR := httptest.NewRecorder()
	srv.handlePluginUpload(newRR, newReq)
	if newRR.Code != http.StatusForbidden {
		t.Fatalf("new generation status = %d, want 403; body=%s", newRR.Code, newRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirB, "new.wasm")); !os.IsNotExist(err) {
		t.Fatalf("disabled new generation wrote a file: err=%v", err)
	}
}

func TestCaptureAdminRuntimeSnapshotPinsAuthAndPolicy(t *testing.T) {
	on := true
	oldCfg := config.AdminConfig{Enabled: true, Token: "old", PluginUploadEnabled: &on, PluginUploadMaxSize: 1, PluginUploadDir: t.TempDir()}
	srv := New(oldCfg, testLogger(t), Deps{})

	seen := make(chan *authSnapshot, 1)
	h := srv.captureAdminRuntimeSnapshot(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen <- srv.requestAdminSnapshot(r)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	pinned := <-seen

	newCfg := oldCfg
	newCfg.Token = "new"
	newCfg.PluginUploadMaxSize = 9
	srv.UpdateLiveAdminConfig(newCfg)
	if pinned == srv.currentAuth() {
		t.Fatal("Publish reused mutable snapshot pointer")
	}
	if pinned.cfg.Token != "old" || pinned.cfg.PluginUploadMaxSize != 1 {
		t.Fatalf("pinned snapshot mutated: token=%q max=%d", pinned.cfg.Token, pinned.cfg.PluginUploadMaxSize)
	}
}

func TestPluginUploadPreflightDoesNotCreateCandidateDirectory(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "future", "plugins")
	if err := preflightPluginUploadDir(dir); err != nil {
		t.Fatalf("preflight missing dir: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("preflight created live candidate directory: err=%v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".jul-upload-preflight-") {
			t.Fatalf("preflight probe leaked: %s", e.Name())
		}
	}
}

func TestPluginUploadPreflightRejectsSymlinkDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	base := t.TempDir()
	realDir := filepath.Join(base, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	if err := preflightPluginUploadDir(link); err == nil {
		t.Fatal("symlink upload directory accepted")
	}
}

func TestPluginUploadRejectsSymlinkDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation may require elevated privileges")
	}
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "escape.wasm")); err != nil {
		t.Fatal(err)
	}
	root, err := openPluginUploadRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writePluginUploadFile(root, "escape.wasm", []byte("replacement")); err == nil {
		t.Fatal("symlink destination accepted")
	}
	got, err := os.ReadFile(outside)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "sentinel" {
		t.Fatalf("outside file changed: %q", got)
	}
}
