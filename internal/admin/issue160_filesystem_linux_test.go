// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package admin

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestIssue160FilesystemCandidatePermissionsAndAbortCleanup(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a", "b", "audit.jsonl")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o640 {
		t.Fatalf("candidate file mode=%v err=%v, want 0640", func() os.FileMode {
			if info != nil {
				return info.Mode().Perm()
			}
			return 0
		}(), err)
	}
	for _, dir := range []string{filepath.Join(root, "a"), filepath.Join(root, "a", "b")} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o750 {
			t.Fatalf("%s mode=%o want 0750", dir, info.Mode().Perm())
		}
	}
	p.abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("candidate file survived abort: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Fatalf("candidate directories survived abort: %v", err)
	}
}

func TestIssue160FilesystemRejectsSymlinkParentAndSpecialFinal(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "real")
	if err := os.Mkdir(realDir, 0o750); err != nil {
		t.Fatal(err)
	}
	linkDir := filepath.Join(root, "link")
	if err := os.Symlink(realDir, linkDir); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAuditFileOwner(filepath.Join(linkDir, "audit.jsonl")); err == nil {
		t.Fatal("symlink parent accepted")
	}
	fifo := filepath.Join(root, "audit.fifo")
	if err := syscall.Mkfifo(fifo, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAuditFileOwner(fifo); err == nil {
		t.Fatal("FIFO final destination accepted")
	}
	dirFinal := filepath.Join(root, "audit.dir")
	if err := os.Mkdir(dirFinal, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareAuditFileOwner(dirFinal); err == nil {
		t.Fatal("directory final destination accepted")
	}
}

func TestIssue160FilesystemAbortNeverDeletesExternalReplacement(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "audit.jsonl")
	saved := filepath.Join(root, "candidate.saved")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 10, 4))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, saved); err != nil {
		t.Fatal(err)
	}
	external := []byte("external-owner\n")
	if err := os.WriteFile(path, external, 0o600); err != nil {
		t.Fatal(err)
	}
	p.abort()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(external) {
		t.Fatalf("external replacement changed: %q", got)
	}
	if _, err := os.Stat(saved); err != nil {
		t.Fatalf("renamed candidate was deleted: %v", err)
	}
}

func TestIssue160FilesystemUnwritableParentFailsWithoutTouchingLiveSink(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses directory permission checks")
	}
	root := t.TempDir()
	aPath := filepath.Join(root, "a.jsonl")
	a := newAuditLogWithSink(8, aPath, 10, 4, nil)
	blocked := filepath.Join(root, "blocked")
	if err := os.Mkdir(blocked, 0o500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(blocked, 0o700)
	if _, err := a.prepareTransition(mustAuditCfg(t, filepath.Join(blocked, "b.jsonl"), 10, 4)); err == nil {
		t.Fatal("unwritable candidate prepared")
	}
	a.record(AuditEvent{Operation: "still-a", Result: "success"})
	ids := readAuditIDs(t, aPath)
	if len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("live A disturbed: %v", ids)
	}
	_ = a.Close()
}
