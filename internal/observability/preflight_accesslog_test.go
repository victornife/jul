// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"os"
	"path/filepath"
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
