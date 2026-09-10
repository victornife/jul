// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

func TestPluginUploadRejectsWrongMethod(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 1, PluginUploadDir: t.TempDir()}, testLogger(t), Deps{})
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, httptest.NewRequest(http.MethodGet, "/api/plugins/upload", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestPluginUploadRejectsInvalidMultipart(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 1, PluginUploadDir: t.TempDir()}, testLogger(t), Deps{})
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", bytes.NewBufferString("not multipart"))
	req.Header.Set("Content-Type", "multipart/form-data")
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPluginUploadRejectsRequestAboveCapturedLimit(t *testing.T) {
	dir := t.TempDir()
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 1, PluginUploadDir: dir}, testLogger(t), Deps{})
	data := make([]byte, (1<<20)+1)
	copy(data, wasmMagic)
	data[4] = 0x01
	req := runtimeUploadRequest(t, "large.wasm", data)
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "large.wasm")); !os.IsNotExist(err) {
		t.Fatalf("oversized upload reached storage: %v", err)
	}
}

func TestPluginUploadReportsCapturedDirectoryUnavailable(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 1, PluginUploadDir: file}, testLogger(t), Deps{})
	data := append(append([]byte{}, wasmMagic...), 0x01, 0x00, 0x00, 0x00)
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, runtimeUploadRequest(t, "valid.wasm", data))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPluginUploadReportsUnsafeDestinationWriteFailure(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "occupied.wasm"), 0o700); err != nil {
		t.Fatal(err)
	}
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 1, PluginUploadDir: dir}, testLogger(t), Deps{})
	data := append(append([]byte{}, wasmMagic...), 0x01, 0x00, 0x00, 0x00)
	rr := httptest.NewRecorder()
	srv.handlePluginUpload(rr, runtimeUploadRequest(t, "occupied.wasm", data))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}
