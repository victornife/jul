// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestInspectPluginUploadDirHealthCategories(t *testing.T) {
	base := t.TempDir()
	ready := filepath.Join(base, "ready")
	if err := os.Mkdir(ready, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := inspectPluginUploadDirHealth(ready); got != "ready" {
		t.Fatalf("ready health = %q", got)
	}

	missing := filepath.Join(base, "missing")
	if got := inspectPluginUploadDirHealth(missing); got != "creatable" {
		t.Fatalf("missing health = %q", got)
	}

	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := inspectPluginUploadDirHealth(file); got != "invalid" {
		t.Fatalf("regular-file health = %q", got)
	}

	if runtime.GOOS != "windows" {
		link := filepath.Join(base, "link")
		if err := os.Symlink(ready, link); err != nil {
			t.Fatal(err)
		}
		if got := inspectPluginUploadDirHealth(link); got != "invalid" {
			t.Fatalf("symlink health = %q", got)
		}
	}
}

func TestInspectPluginUploadDirHealthUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX execute permissions are required to force Lstat EACCES")
	}
	base := t.TempDir()
	locked := filepath.Join(base, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })
	if got := inspectPluginUploadDirHealth(filepath.Join(locked, "child")); got != "unavailable" {
		t.Skipf("platform/filesystem reports %q instead of EACCES-backed unavailable", got)
	}
}

func TestAdminRuntimePrepareErrorContract(t *testing.T) {
	inner := errors.New("boom")
	err := newAdminRuntimePrepareError("test_category", inner)
	if !strings.Contains(err.Error(), "test_category") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("unexpected error text: %q", err.Error())
	}
	if !errors.Is(err, inner) {
		t.Fatal("prepare error did not unwrap to underlying error")
	}
}

func TestAdminRuntimeStatusIncludesRecordedDiagnostics(t *testing.T) {
	srv := New(config.AdminConfig{Enabled: true, PluginUploadMaxSize: 0}, testLogger(t), Deps{})
	srv.recordAdminPrepareFailure(adminPrepareFailureUploadDirectory)
	srv.recordPluginUploadRejection(uploadRejectStorageUnavailable)
	status := srv.adminRuntimeStatus(nil)
	if status == nil {
		t.Fatal("runtime status is nil")
	}
	if status.PreparationFailure != adminPrepareFailureUploadDirectory {
		t.Fatalf("prepare failure = %q", status.PreparationFailure)
	}
	if status.LastUploadRejection != uploadRejectStorageUnavailable {
		t.Fatalf("upload rejection = %q", status.LastUploadRejection)
	}
	if status.UploadDirectoryHealth != "disabled" {
		t.Fatalf("disabled upload health = %q", status.UploadDirectoryHealth)
	}

	srv.clearAdminPrepareFailure()
	status = srv.adminRuntimeStatus(nil)
	if status.PreparationFailure != "" {
		t.Fatalf("prepare failure not cleared: %q", status.PreparationFailure)
	}
}
