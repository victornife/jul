// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestRequestAdminSnapshotFallbacks(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true}, testLogger(t), Deps{})
	want := srv.currentAuth()

	if got := srv.requestAdminSnapshot(nil); got != want {
		t.Fatal("nil request did not fall back to current snapshot")
	}
	if got := srv.requestAdminSnapshot(httptest.NewRequest("GET", "/", nil)); got != want {
		t.Fatal("request without pinned snapshot did not fall back to current snapshot")
	}
}

func TestPrepareAdminRuntimeNilPreparedAndFailure(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true}, testLogger(t), Deps{})
	off := false
	cfg := config.AdminConfig{
		Enabled:                 true,
		PluginUploadEnabled:     &off,
		PluginUploadMaxSize:     1,
		PluginUploadDir:         "  ",
	}
	got, err := srv.PrepareAdminRuntime(cfg, nil)
	if err != nil {
		t.Fatalf("disabled upload prepare: %v", err)
	}
	if got != nil {
		t.Fatalf("nil prepared auth became %#v", got)
	}

	on := true
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.PluginUploadEnabled = &on
	cfg.PluginUploadDir = file
	if _, err := srv.PrepareAdminRuntime(cfg, nil); err == nil {
		t.Fatal("prepare accepted a regular file as plugin upload directory")
	}
}

func TestCompleteAdminRuntimeSnapshotNil(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true}, testLogger(t), Deps{})
	if got := srv.completeAdminRuntimeSnapshot(nil); got != nil {
		t.Fatalf("nil snapshot completed as %#v", got)
	}
}

func TestPluginUploadHelpersAndExistingDirectoryPreflight(t *testing.T) {
	var cfg config.AdminConfig
	if !pluginUploadEnabled(cfg) {
		t.Fatal("nil compatibility flag should be enabled for directly constructed config")
	}
	off := false
	cfg.PluginUploadEnabled = &off
	if pluginUploadEnabled(cfg) {
		t.Fatal("explicit false upload flag reported enabled")
	}

	dir := t.TempDir()
	if got := normalizePluginUploadDir("  " + dir + "  "); got != filepath.Clean(dir) {
		t.Fatalf("normalized dir = %q, want %q", got, filepath.Clean(dir))
	}
	if err := preflightPluginUploadDir(dir); err != nil {
		t.Fatalf("existing directory preflight: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".jul-upload-") {
			t.Fatalf("preflight leaked probe %q", entry.Name())
		}
	}
}

func TestPreflightRejectsRegularFileAndFindsNearestAncestor(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := preflightPluginUploadDir(file); err == nil {
		t.Fatal("regular file accepted as upload directory")
	}
	if _, err := nearestExistingDirectory(file); err == nil {
		t.Fatal("regular file accepted as nearest existing directory")
	}

	ancestor := filepath.Join(base, "existing")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(ancestor, "future", "plugins")
	got, err := nearestExistingDirectory(candidate)
	if err != nil {
		t.Fatalf("nearest existing directory: %v", err)
	}
	if got != ancestor {
		t.Fatalf("nearest ancestor = %q, want %q", got, ancestor)
	}
}

func TestProbePluginUploadDirectoryRejectsNonDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := probePluginUploadDirectory(file); err == nil {
		t.Fatal("probe accepted a regular file")
	}
}

func TestOpenPluginUploadRootCreatesMissingDirectoryAndRejectsUnsafePath(t *testing.T) {
	base := t.TempDir()
	missing := filepath.Join(base, "new", "plugins")
	root, err := openPluginUploadRoot(missing)
	if err != nil {
		t.Fatalf("open missing upload root: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(missing)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() {
		t.Fatalf("created upload root is not a directory: %v", fi.Mode())
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if root, err := openPluginUploadRoot(file); err == nil {
		_ = root.Close()
		t.Fatal("regular file accepted as upload root")
	}

	if runtime.GOOS != "windows" {
		link := filepath.Join(base, "link")
		if err := os.Symlink(missing, link); err != nil {
			t.Fatal(err)
		}
		if root, err := openPluginUploadRoot(link); err == nil {
			_ = root.Close()
			t.Fatal("symlink accepted as upload root")
		}
	}
}

func TestWritePluginUploadFileRejectsUnavailableAndSpecialDestinations(t *testing.T) {
	if err := writePluginUploadFile(nil, "x.wasm", []byte("x")); err == nil {
		t.Fatal("nil upload root accepted")
	}

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dest.wasm"), 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := writePluginUploadFile(root, "dest.wasm", []byte("x")); err == nil {
		t.Fatal("directory destination accepted as regular upload file")
	}
}

func TestWritePluginUploadFileCleansTempOnCreateFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write permissions are not POSIX-like on Windows")
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := writePluginUploadFile(root, "blocked.wasm", []byte("x")); err == nil {
		t.Fatal("write unexpectedly succeeded in non-writable upload directory")
	}
}
