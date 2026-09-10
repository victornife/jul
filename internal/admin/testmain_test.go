// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestMain canonicalizes the macOS temporary directory before tests
// create audit destinations. macOS exposes /var as a symlink to
// /private/var, while the production audit contract deliberately
// rejects symlinked parent components. Tests should exercise that
// contract from the canonical filesystem path rather than weakening it.
func TestMain(m *testing.M) {
	if runtime.GOOS == "darwin" {
		if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
			_ = os.Setenv("TMPDIR", resolved)
		}
	}
	os.Exit(m.Run())
}
