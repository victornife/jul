// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
)

type issue157BlockingBody struct {
	reader  *bytes.Reader
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *issue157BlockingBody) Read(p []byte) (int, error) {
	b.once.Do(func() {
		close(b.started)
		<-b.release
	})
	return b.reader.Read(p)
}

func (*issue157BlockingBody) Close() error { return nil }

type issue157FailingBody struct {
	reader *bytes.Reader
	limit  int
	read   int
}

func (b *issue157FailingBody) Read(p []byte) (int, error) {
	if b.read >= b.limit {
		return 0, context.Canceled
	}
	remaining := b.limit - b.read
	if len(p) > remaining {
		p = p[:remaining]
	}
	n, err := b.reader.Read(p)
	b.read += n
	if b.read >= b.limit && err == nil {
		return n, context.Canceled
	}
	return n, err
}

func (*issue157FailingBody) Close() error { return nil }

func issue157Multipart(t *testing.T, name string, payload []byte) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("wasm", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), writer.FormDataContentType()
}

func issue157ValidWASM(extra int) []byte {
	payload := make([]byte, 8+extra)
	copy(payload, wasmMagic)
	payload[4] = 0x01
	return payload
}

func TestPluginUploadDirectorySwitchDuringRealInFlightRead(t *testing.T) {
	on := true
	dirA := t.TempDir()
	dirB := t.TempDir()
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 2,
		PluginUploadDir:     dirA,
	}
	srv := New(cfg, testLogger(t), Deps{})
	handler := srv.captureAdminRuntimeSnapshot(http.HandlerFunc(srv.handlePluginUpload))

	payload := issue157ValidWASM(128)
	bodyBytes, contentType := issue157Multipart(t, "in-flight-a.wasm", payload)
	blocked := &issue157BlockingBody{
		reader:  bytes.NewReader(bodyBytes),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	req.Body = blocked
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rr, req)
	}()

	<-blocked.started // request generation is already pinned; body has not arrived.
	cfg.PluginUploadDir = dirB
	srv.UpdateLiveAdminConfig(cfg)
	close(blocked.release)
	<-done

	if rr.Code != http.StatusOK {
		t.Fatalf("in-flight A upload status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirA, "in-flight-a.wasm")); err != nil {
		t.Fatalf("old-generation upload did not finish in directory A: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dirB, "in-flight-a.wasm")); !os.IsNotExist(err) {
		t.Fatalf("old-generation upload leaked into directory B: err=%v", err)
	}

	newRR := httptest.NewRecorder()
	handler.ServeHTTP(newRR, runtimeUploadRequest(t, "new-b.wasm", payload))
	if newRR.Code != http.StatusOK {
		t.Fatalf("new-generation B upload status = %d, want 200; body=%s", newRR.Code, newRR.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dirB, "new-b.wasm")); err != nil {
		t.Fatalf("new-generation upload did not use directory B: %v", err)
	}
}

func TestPluginUploadPolicyHotTransitionOffOnOff(t *testing.T) {
	on := true
	off := false
	dir := t.TempDir()
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &off,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     dir,
	}
	srv := New(cfg, testLogger(t), Deps{})
	handler := srv.captureAdminRuntimeSnapshot(http.HandlerFunc(srv.handlePluginUpload))
	payload := issue157ValidWASM(16)

	serve := func(name string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, runtimeUploadRequest(t, name, payload))
		return rr
	}
	if got := serve("off-before.wasm").Code; got != http.StatusForbidden {
		t.Fatalf("initial disabled status = %d, want 403", got)
	}

	cfg.PluginUploadEnabled = &on
	srv.UpdateLiveAdminConfig(cfg)
	if got := serve("enabled.wasm").Code; got != http.StatusOK {
		t.Fatalf("enabled status = %d, want 200", got)
	}

	cfg.PluginUploadEnabled = &off
	srv.UpdateLiveAdminConfig(cfg)
	if got := serve("off-after.wasm").Code; got != http.StatusForbidden {
		t.Fatalf("re-disabled status = %d, want 403", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "enabled.wasm")); err != nil {
		t.Fatalf("re-disabling removed the previously uploaded file: %v", err)
	}
}

func TestPluginUploadLimitIncreaseDoesNotRelaxPinnedRequest(t *testing.T) {
	on := true
	dir := t.TempDir()
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     dir,
	}
	srv := New(cfg, testLogger(t), Deps{})
	handler := srv.captureAdminRuntimeSnapshot(http.HandlerFunc(srv.handlePluginUpload))

	payload := issue157ValidWASM(1200 * 1024)
	bodyBytes, contentType := issue157Multipart(t, "old-strict.wasm", payload)
	blocked := &issue157BlockingBody{
		reader:  bytes.NewReader(bodyBytes),
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	req.Body = blocked
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		handler.ServeHTTP(rr, req)
	}()

	<-blocked.started
	cfg.PluginUploadMaxSize = 2
	srv.UpdateLiveAdminConfig(cfg)
	close(blocked.release)
	<-done
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("old strict request status = %d, want 413; body=%s", rr.Code, rr.Body.String())
	}

	newRR := httptest.NewRecorder()
	handler.ServeHTTP(newRR, runtimeUploadRequest(t, "new-relaxed.wasm", payload))
	if newRR.Code != http.StatusOK {
		t.Fatalf("new relaxed request status = %d, want 200; body=%s", newRR.Code, newRR.Body.String())
	}
}

func TestPluginUploadCanceledBodyLeavesNoArtifacts(t *testing.T) {
	on := true
	dir := t.TempDir()
	cfg := config.AdminConfig{
		Enabled:             true,
		PluginUploadEnabled: &on,
		PluginUploadMaxSize: 1,
		PluginUploadDir:     dir,
	}
	srv := New(cfg, testLogger(t), Deps{})
	bodyBytes, contentType := issue157Multipart(t, "partial.wasm", issue157ValidWASM(256))
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", nil)
	req.Body = &issue157FailingBody{reader: bytes.NewReader(bodyBytes), limit: len(bodyBytes) / 2}
	req.Header.Set("Content-Type", contentType)
	rr := httptest.NewRecorder()

	srv.handlePluginUpload(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("canceled/partial upload status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("canceled upload left artifacts: %v", names)
	}
}

func TestPluginUploadPreflightPermissionFailureLeavesNoProbe(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory permission semantics do not apply on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission denial cannot be asserted when tests run as root")
	}
	parent := t.TempDir()
	target := filepath.Join(parent, "future", "plugins")
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	err := preflightPluginUploadDir(target)
	if restoreErr := os.Chmod(parent, 0o700); restoreErr != nil {
		t.Fatalf("restore parent permissions: %v", restoreErr)
	}
	if err == nil {
		t.Fatal("preflight accepted a non-writable parent")
	}
	entries, readErr := os.ReadDir(parent)
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jul-upload-preflight-") {
			t.Fatalf("failed preflight left probe directory %q", entry.Name())
		}
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatalf("failed preflight created configured target: err=%v", statErr)
	}
}

var _ io.ReadCloser = (*issue157BlockingBody)(nil)
var _ io.ReadCloser = (*issue157FailingBody)(nil)
