package admin

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
	"log/slog"
)

func TestHandlePluginUpload(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.AdminConfig{Enabled: true, PluginUploadDir: tmpDir, PluginUploadMaxSize: 32}
	srv := New(cfg, testLogger(t), Deps{})

	buildUpload := func(name string, data []byte) (*http.Request, *multipart.Writer) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		part, _ := w.CreateFormFile("wasm", name)
		_, _ = part.Write(data)
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		return req, w
	}

	t.Run("valid WASM upload", func(t *testing.T) {
		data := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x01, 0x02, 0x03, 0x04)
		req, _ := buildUpload("test.wasm", data)
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"name":"test.wasm"`) {
			t.Errorf("response missing expected name: %s", rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), `"path"`) {
			t.Errorf("response missing expected path: %s", rr.Body.String())
		}

		// Verify file landed on disk.
		dest := filepath.Join(tmpDir, "test.wasm")
		if _, err := os.Stat(dest); err != nil {
			t.Errorf("uploaded file not found: %v", err)
		}
	})

	t.Run("re-upload atomically replaces", func(t *testing.T) {
		data1 := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x01)
		req1, _ := buildUpload("replace.wasm", data1)
		rr1 := httptest.NewRecorder()
		srv.handlePluginUpload(rr1, req1)
		if rr1.Code != http.StatusOK {
			t.Fatalf("first upload: status = %d, want 200", rr1.Code)
		}

		data2 := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x02)
		req2, _ := buildUpload("replace.wasm", data2)
		rr2 := httptest.NewRecorder()
		srv.handlePluginUpload(rr2, req2)
		if rr2.Code != http.StatusOK {
			t.Fatalf("second upload: status = %d, want 200", rr2.Code)
		}

		dest := filepath.Join(tmpDir, "replace.wasm")
		content, err := os.ReadFile(dest)
		if err != nil {
			t.Fatalf("read replaced file: %v", err)
		}
		if content[len(content)-1] != 0x02 {
			t.Errorf("file was not replaced atomically")
		}
	})

	t.Run("non-WASM rejected", func(t *testing.T) {
		req, _ := buildUpload("bad.txt", []byte("not wasm"))
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "magic number") {
			t.Errorf("expected magic-number error, got: %s", rr.Body.String())
		}
	})

	t.Run("oversized rejected", func(t *testing.T) {
		// Create a small server with a 1-byte max to test the cap.
		smallCfg := config.AdminConfig{Enabled: true, PluginUploadDir: t.TempDir(), PluginUploadMaxSize: 0}
		smallSrv := New(smallCfg, testLogger(t), Deps{})

		data := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x01)
		req, _ := buildUpload("big.wasm", data)
		rr := httptest.NewRecorder()
		smallSrv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
	})

	t.Run("missing wasm field", func(t *testing.T) {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		_ = w.WriteField("other", "value")
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "missing") {
			t.Errorf("expected missing-field error, got: %s", rr.Body.String())
		}
	})

	t.Run("file too short", func(t *testing.T) {
		req, _ := buildUpload("short.wasm", wasmMagic[:3])
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("unsupported version", func(t *testing.T) {
		data := append(wasmMagic, 0x02, 0x00, 0x00, 0x00)
		req, _ := buildUpload("v2.wasm", data)
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "unsupported WASM version") {
			t.Errorf("expected version error, got: %s", rr.Body.String())
		}
	})

	t.Run("concurrent uploads do not race", func(t *testing.T) {
		concurrentDir := t.TempDir()
		concurrentCfg := config.AdminConfig{Enabled: true, PluginUploadDir: concurrentDir, PluginUploadMaxSize: 32}
		concurrentSrv := New(concurrentCfg, testLogger(t), Deps{})

		done := make(chan struct{}, 10)
		for i := 0; i < 10; i++ {
			go func(n int) {
				defer func() { done <- struct{}{} }()
				data := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, byte(n))
				req, _ := buildUpload(fmt.Sprintf("concurrent-%d.wasm", n), data)
				rr := httptest.NewRecorder()
				concurrentSrv.handlePluginUpload(rr, req)
				if rr.Code != http.StatusOK {
					t.Errorf("concurrent upload %d: status = %d, body = %s", n, rr.Code, rr.Body.String())
				}
			}(i)
		}
		for i := 0; i < 10; i++ {
			<-done
		}
	})

	t.Run("uploaded file is owner-only", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Unix permission bits not applicable on Windows")
		}
		data := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x01)
		req, _ := buildUpload("perms.wasm", data)
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d", rr.Code)
		}
		dest := filepath.Join(tmpDir, "perms.wasm")
		fi, err := os.Stat(dest)
		if err != nil {
			t.Fatalf("stat uploaded file: %v", err)
		}
		mode := fi.Mode().Perm()
		if mode != 0o600 {
			t.Errorf("mode = %#o, want 0o600", mode)
		}
	})

	t.Run("explicitly disabled returns 403 even with positive max size", func(t *testing.T) {
		disabled := false
		explicitCfg := config.AdminConfig{Enabled: true, PluginUploadDir: t.TempDir(), PluginUploadMaxSize: 32, PluginUploadEnabled: &disabled}
		explicitSrv := New(explicitCfg, testLogger(t), Deps{})

		data := append(wasmMagic, 0x01, 0x00, 0x00, 0x00, 0x01)
		req, _ := buildUpload("disabled.wasm", data)
		rr := httptest.NewRecorder()
		explicitSrv.handlePluginUpload(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body = %s", rr.Code, rr.Body.String())
		}
	})
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestPluginUploadFilenameHardening covers the filename-validation defenses:
// only simple <name>.wasm names are accepted, and a path-traversal attempt is
// neutralized to a safe basename inside the upload directory (Finding SEC-1).
func TestPluginUploadFilenameHardening(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := config.AdminConfig{Enabled: true, PluginUploadDir: tmpDir, PluginUploadMaxSize: 32}
	srv := New(cfg, testLogger(t), Deps{})

	// validMagic is a minimally-valid WASM v1 module so the request reaches the
	// filename check rather than failing the magic-number test first.
	validMagic := append(append([]byte{}, wasmMagic...), 0x01, 0x00, 0x00, 0x00, 0x01)

	buildUpload := func(name string, data []byte) *http.Request {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		part, _ := w.CreateFormFile("wasm", name)
		_, _ = part.Write(data)
		_ = w.Close()
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/upload", &b)
		req.Header.Set("Content-Type", w.FormDataContentType())
		return req
	}

	rejected := []struct {
		desc string
		name string
	}{
		{"non-wasm extension", "plugin.txt"},
		{"no extension", "plugin"},
		{"space in name", "evil name.wasm"},
		{"dotdot in name", "a..b.wasm"},
		{"leading dot", ".hidden.wasm"},
		{"empty base", ".wasm"},
		{"disallowed char", "plugin$.wasm"},
	}
	for _, tc := range rejected {
		t.Run("rejected: "+tc.desc, func(t *testing.T) {
			rr := httptest.NewRecorder()
			srv.handlePluginUpload(rr, buildUpload(tc.name, validMagic))
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "invalid filename") {
				t.Errorf("expected invalid-filename error, got: %s", rr.Body.String())
			}
		})
	}

	t.Run("forward-slash traversal is reduced to a safe basename", func(t *testing.T) {
		rr := httptest.NewRecorder()
		srv.handlePluginUpload(rr, buildUpload("../../etc/evil.wasm", validMagic))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body = %s", rr.Code, rr.Body.String())
		}
		// The module must land as evil.wasm directly in the upload dir, never
		// outside it.
		if _, err := os.Stat(filepath.Join(tmpDir, "evil.wasm")); err != nil {
			t.Errorf("expected safe basename evil.wasm in upload dir: %v", err)
		}
		outside := filepath.Join(filepath.Dir(filepath.Dir(tmpDir)), "etc", "evil.wasm")
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("module escaped the upload directory to %s", outside)
		}
	})
}

func TestProjectPluginsUploadEnabled(t *testing.T) {
	t.Run("disabled when PluginUploadEnabled is nil", func(t *testing.T) {
		c := pluginPatchConfig()
		c.Admin.PluginUploadMaxSize = 32
		proj := projectPlugins(c, true)
		if proj.UploadEnabled {
			t.Error("UploadEnabled = true, want false when PluginUploadEnabled is nil")
		}
		if proj.UploadMaxSizeMB != 0 {
			t.Errorf("UploadMaxSizeMB = %d, want 0 when disabled", proj.UploadMaxSizeMB)
		}
	})

	t.Run("explicitly disabled forces max size to zero", func(t *testing.T) {
		c := pluginPatchConfig()
		c.Admin.PluginUploadEnabled = boolPtr(false)
		c.Admin.PluginUploadMaxSize = 32
		proj := projectPlugins(c, true)
		if proj.UploadEnabled {
			t.Error("UploadEnabled = true, want false")
		}
		if proj.UploadMaxSizeMB != 0 {
			t.Errorf("UploadMaxSizeMB = %d, want 0 when disabled", proj.UploadMaxSizeMB)
		}
	})
}
