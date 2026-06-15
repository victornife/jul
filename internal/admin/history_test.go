package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHistorySnapshotAndList(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 50)

	id1, err := h.snapshot([]byte("a = 1\n"))
	if err != nil {
		t.Fatalf("snapshot 1: %v", err)
	}
	id2, err := h.snapshot([]byte("a = 2\n"))
	if err != nil {
		t.Fatalf("snapshot 2: %v", err)
	}
	if id1 == "" || id2 == "" {
		t.Fatalf("snapshot returned empty id: %q %q", id1, id2)
	}

	list, err := h.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	// Newest first.
	if list[0].ID != id2 {
		t.Errorf("newest = %q, want %q", list[0].ID, id2)
	}
	if list[0].Size == 0 {
		t.Error("expected non-zero snapshot size")
	}

	got, err := h.get(id1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got) != "a = 1\n" {
		t.Errorf("get content = %q, want %q", got, "a = 1\n")
	}
}

func TestHistorySkipsEmptyContent(t *testing.T) {
	h := newHistory(t.TempDir(), 50)
	id, err := h.snapshot([]byte("   \n\t"))
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id for whitespace-only content, got %q", id)
	}
	list, _ := h.list()
	if len(list) != 0 {
		t.Errorf("expected no snapshots, got %d", len(list))
	}
}

func TestHistoryPruneKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 3)
	for i := 0; i < 6; i++ {
		if _, err := h.snapshot([]byte("v\n")); err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
	}
	list, err := h.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("retained %d snapshots, want 3", len(list))
	}
	// Confirm only three files remain on disk.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Errorf("on-disk files = %d, want 3", len(entries))
	}
}

func TestHistoryGetRejectsPathTraversal(t *testing.T) {
	dir := t.TempDir()
	// Plant a file outside the history dir we must never be able to read.
	secret := filepath.Join(filepath.Dir(dir), "secret.toml")
	if err := os.WriteFile(secret, []byte("token = \"top-secret\"\n"), 0o600); err != nil {
		t.Fatalf("seed secret: %v", err)
	}
	h := newHistory(dir, 50)

	bad := []string{
		"../secret",
		"..\\secret",
		"/etc/passwd",
		"a/../../secret",
		"foo/bar",
		"",
		"with space",
	}
	for _, id := range bad {
		if _, err := h.get(id); err == nil {
			t.Errorf("get(%q) succeeded, want rejection", id)
		}
	}
}

func TestHistoryDisabledNoOps(t *testing.T) {
	h := newHistory("", 50)
	if h.enabled() {
		t.Fatal("blank dir should be disabled")
	}
	id, err := h.snapshot([]byte("a = 1\n"))
	if err != nil || id != "" {
		t.Errorf("disabled snapshot = (%q, %v), want empty/nil", id, err)
	}
	list, err := h.list()
	if err != nil || list != nil {
		t.Errorf("disabled list = (%v, %v), want nil/nil", list, err)
	}
	if _, err := h.get("anything"); err == nil {
		t.Error("disabled get should error")
	}
}

func TestValidHistoryID(t *testing.T) {
	good := []string{"20240101T120000.000Z", "20240101T120000.000Z-1", "abc_DEF-123"}
	for _, id := range good {
		if !validHistoryID(id) {
			t.Errorf("validHistoryID(%q) = false, want true", id)
		}
	}
	bad := []string{"", "..", "a/b", "a\\b", "a..b", "x/", string(make([]byte, 65))}
	for _, id := range bad {
		if validHistoryID(id) {
			t.Errorf("validHistoryID(%q) = true, want false", id)
		}
	}
}
