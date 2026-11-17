// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package inspection

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestSpool(t *testing.T, mode Mode, maxBytes, maxFiles uint64) (*Governor, *SpoolController, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "spool")
	g, err := New(Limits{
		Mode:                   mode,
		MaxBytes:               1 << 20,
		SpoolDir:               root,
		MaxSpoolBytes:          maxBytes,
		MaxSpoolFiles:          maxFiles,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		EvaluationQueueTimeout: time.Second,
		BaseChargeBytes:        16 << 10,
	}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewSpoolController(g, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close spool controller: %v", err)
		}
	})
	return g, c, root
}

func TestSpoolEnforceReservesBeforeWriteAndRecoversCleanupDebt(t *testing.T) {
	g, c, root := newTestSpool(t, Enforce, 3, 1)
	tx, err := c.NewTransaction()
	if err != nil {
		t.Fatal(err)
	}
	file, err := tx.CreateTemp("", "ignored*")
	if err != nil {
		t.Fatal(err)
	}
	if n, err := file.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first write = %d, %v; want 3, nil", n, err)
	}
	if _, err := file.Write([]byte("d")); !IsOutcome(err, SpoolBytesSaturated) {
		t.Fatalf("over-quota write error = %v, want spool byte refusal", err)
	}
	if _, err := tx.CreateTemp("", "ignored*"); !IsOutcome(err, SpoolFilesSaturated) {
		t.Fatalf("over-quota create error = %v, want spool file refusal", err)
	}
	if got := c.Snapshot(); got.UsedBytes != 3 || got.UsedFiles != 1 {
		t.Fatalf("reservation after rejected operations = %+v", got)
	}

	name := file.Name()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(name, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(name, "held"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Remove(name); err == nil {
		t.Fatal("remove unexpectedly succeeded for a non-empty directory")
	}
	if err := tx.Close(); err == nil {
		t.Fatal("transaction cleanup did not retain its failure")
	}
	if got := c.Snapshot(); got.UsedBytes != 3 || got.UsedFiles != 1 || got.DebtBytes != 3 || got.DebtFiles != 1 {
		t.Fatalf("cleanup debt = %+v", got)
	}

	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := NewSpoolController(g, root)
	if err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	defer recovered.Close()
	if _, err := os.Stat(filepath.Dir(name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale transaction directory remains after recovery: %v", err)
	}
}

func TestSpoolObserveRecordsWouldRejectAndContinues(t *testing.T) {
	g, c, _ := newTestSpool(t, Observe, 2, 1)
	tx, err := c.NewTransaction()
	if err != nil {
		t.Fatal(err)
	}
	first, err := tx.CreateTemp("", "ignored*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Write([]byte("abc")); err != nil {
		t.Fatalf("observe-mode write was refused: %v", err)
	}
	second, err := tx.CreateTemp("", "ignored*")
	if err != nil {
		t.Fatalf("observe-mode create was refused: %v", err)
	}
	if got := c.Snapshot(); got.UsedBytes != 3 || got.UsedFiles != 2 || got.WouldBytes != 3 || got.WouldFiles != 1 {
		t.Fatalf("observe snapshot = %+v", got)
	}
	snapshot := g.Snapshot()
	if snapshot.Rejections[RejectSpoolBytes][1] == 0 || snapshot.Rejections[RejectSpoolFiles][1] == 0 {
		t.Fatalf("observe rejections = %v", snapshot.Rejections)
	}
	if err := tx.Close(); err != nil {
		t.Fatal(err)
	}
	_ = second
}

func TestSpoolSuccessfulRetryClearsCleanupDebt(t *testing.T) {
	_, c, _ := newTestSpool(t, Enforce, 16, 4)
	tx, err := c.NewTransaction()
	if err != nil {
		t.Fatal(err)
	}
	file, err := tx.CreateTemp("", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(file.Name()); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(file.Name(), 0700); err != nil {
		t.Fatal(err)
	}
	held := filepath.Join(file.Name(), "held")
	if err := os.WriteFile(held, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tx.Remove(file.Name()); err == nil {
		t.Fatal("remove unexpectedly succeeded for a directory")
	}
	if got := c.Snapshot(); got.DebtBytes != 3 || got.DebtFiles != 1 {
		t.Fatalf("initial cleanup debt = %+v", got)
	}

	if err := os.Remove(held); err != nil {
		t.Fatal(err)
	}
	if err := tx.Close(); err == nil {
		t.Fatal("transaction should retain the initial cleanup error")
	}
	if got := c.Snapshot(); got.UsedBytes != 0 || got.UsedFiles != 0 || got.DebtBytes != 0 || got.DebtFiles != 0 {
		t.Fatalf("successful cleanup retry left reservations or debt: %+v", got)
	}
}

func TestSpoolRefusesUnmarkedRootAndUnownedRemove(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "not-owned")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "unrelated"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	g := spoolTestGovernor(t, root)
	if _, err := NewSpoolController(g, root); err == nil {
		t.Fatal("controller adopted a non-empty unmarked root")
	}
	if _, err := os.Stat(filepath.Join(root, "unrelated")); err != nil {
		t.Fatalf("unmarked root was modified: %v", err)
	}

	empty := filepath.Join(t.TempDir(), "spool")
	g = spoolTestGovernor(t, empty)
	c, err := NewSpoolController(g, empty)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := c.NewTransaction()
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Remove(outside); err == nil {
		t.Fatal("provider removed a path it did not create")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "keep" {
		t.Fatalf("unowned file changed: %q, %v", b, err)
	}
	_ = tx.Close()
	_ = c.Close()
}

func spoolTestGovernor(t *testing.T, root string) *Governor {
	t.Helper()
	g, err := New(Limits{
		Mode:                   Enforce,
		MaxBytes:               1 << 20,
		SpoolDir:               root,
		MaxSpoolBytes:          16,
		MaxSpoolFiles:          4,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		EvaluationQueueTimeout: time.Second,
		BaseChargeBytes:        16 << 10,
	}, Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}
