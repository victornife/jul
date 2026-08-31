// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestPreflightAccessSinksDisabledDoesNotTouchFilesystem(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "access.log")
	err := PreflightAccessSinks(config.AccessLogConfig{
		Enabled: config.Bool(false),
		Sinks:   []string{"file"},
		File:    path,
	})
	if err != nil {
		t.Fatalf("preflight disabled access log: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("disabled preflight created directory: %v", err)
	}
}

func TestPreflightAccessSinksEnabledProvesFileDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "access.log")
	err := PreflightAccessSinks(config.AccessLogConfig{
		Enabled: config.Bool(true),
		Sinks:   []string{"file"},
		File:    path,
	})
	if err != nil {
		t.Fatalf("preflight enabled access log: %v", err)
	}
	if st, err := os.Stat(filepath.Dir(path)); err != nil || !st.IsDir() {
		t.Fatalf("enabled preflight did not create writable directory: stat=%v err=%v", st, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("preflight must not retain the real log file: %v", err)
	}
}

// TestPreflightAccessSinksReportsUnwritableDirectory covers PreflightAccessSinks'
// error path: a probeWritableDir failure must surface, not be swallowed.
func TestPreflightAccessSinksReportsUnwritableDirectory(t *testing.T) {
	orig := probeCloseFile
	// Actually close the file so no locked handle is left behind (Windows
	// cannot remove/clean up a still-open file) while still reporting a failure.
	probeCloseFile = func(f *os.File) error { _ = f.Close(); return errors.New("injected close failure") }
	defer func() { probeCloseFile = orig }()

	path := filepath.Join(t.TempDir(), "logs", "access.log")
	err := PreflightAccessSinks(config.AccessLogConfig{
		Enabled: config.Bool(true),
		Sinks:   []string{"file"},
		File:    path,
	})
	if err == nil {
		t.Fatal("expected an error when the writability probe fails")
	}
	if !strings.Contains(err.Error(), "file sink") {
		t.Fatalf("error does not identify the file sink: %v", err)
	}
}
