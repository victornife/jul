package atomicfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteCreatesFileWithSecureMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := Write(path, []byte("hello"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("content = %q, want %q", got, "hello")
	}
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode = %o, want 600", fi.Mode().Perm())
		}
	}
	assertNoTempLeftovers(t, dir)
}

func TestWritePreservesExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits are not meaningful on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("v2"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %o, want 644 (existing mode must be preserved)", fi.Mode().Perm())
	}
	got, _ := os.ReadFile(path)
	if string(got) != "v2" {
		t.Fatalf("content = %q, want %q", got, "v2")
	}
}

func TestWriteReplacesContentAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, _ := os.ReadFile(path)
	if string(got) != "new" {
		t.Fatalf("content = %q, want %q", got, "new")
	}
	assertNoTempLeftovers(t, dir)
}

func TestWriteFailsWhenDirMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist", "config.toml")

	if err := Write(path, []byte("x"), 0o600); err == nil {
		t.Fatal("expected error writing into a missing directory")
	}
	// A failed write must not leave a stray temp file in the parent temp dir.
	assertNoTempLeftovers(t, dir)
}

func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Errorf("leftover temp file in %s: %s", dir, e.Name())
		}
	}
}
