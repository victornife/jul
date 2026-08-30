// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package supportbundle

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenVerifiedRegularFileRejectsSymlinkedParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation is not consistently permitted on Windows CI")
	}
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(realDirectory, "access.log")
	if err := os.WriteFile(logPath, []byte("line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	file, _, err := openVerifiedRegularFile(filepath.Join(linkedDirectory, "access.log"))
	if file != nil {
		_ = file.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "symbolic-link component") {
		t.Fatalf("symlinked parent error = %v", err)
	}
}
