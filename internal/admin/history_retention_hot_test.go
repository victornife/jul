// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"jul/internal/config"
)

func countHistorySnapshots(t *testing.T, h *history) int {
	t.Helper()
	names, err := h.snapshotFiles()
	if err != nil {
		t.Fatal(err)
	}
	return len(names)
}

func seedHistoryWithMeta(t *testing.T, h *history, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, _, err := h.snapshotWithMeta([]byte("value = 1\n"), &HistoryMetadata{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoryRetentionStagesThenPrunesOnlyAfterPublish(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	s := &Server{hist: h, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}

	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	if got := h.retention(); got != 3 {
		t.Fatalf("Prepare mutated live retention: got %d want 3", got)
	}
	if got := countHistorySnapshots(t, h); got != 3 {
		t.Fatalf("Prepare pruned files: got %d want 3", got)
	}

	s.CommitPreparedAdminRuntime(prepared)
	if got := h.retention(); got != 1 {
		t.Fatalf("Publish retention=%d want 1", got)
	}
	if got := countHistorySnapshots(t, h); got != 3 {
		t.Fatalf("Publish performed destructive prune: got %d want 3", got)
	}

	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := countHistorySnapshots(t, h); got != 1 {
		t.Fatalf("PostCommit prune count=%d want 1", got)
	}
}

func TestHistoryRetentionAbortAndIncreaseDeleteNothing(t *testing.T) {
	h := newHistory(t.TempDir(), 2)
	seedHistoryWithMeta(t, h, 2)
	s := &Server{hist: h}

	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.AbortPreparedAdminRuntime(prepared)
	if got := h.retention(); got != 2 {
		t.Fatalf("Abort retention=%d want 2", got)
	}
	if got := countHistorySnapshots(t, h); got != 2 {
		t.Fatalf("Abort deleted snapshots: %d", got)
	}

	candidate.HistoryKeep = 5
	prepared, err = s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := countHistorySnapshots(t, h); got != 2 {
		t.Fatalf("retention increase deleted snapshots: %d", got)
	}
}

func TestHistoryRetentionPruneFailureIsAdvisory(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	h.remove = func(string) error { return errors.New("injected remove failure") }
	s := &Server{hist: h, log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	candidate := config.AdminConfig{HistoryKeep: 1}
	prepared, err := s.PrepareAdminRuntime(candidate, PrepareAuth(candidate, nil))
	if err != nil {
		t.Fatal(err)
	}
	s.CommitPreparedAdminRuntime(prepared)
	s.RetirePreparedAdminRuntime(context.Background(), prepared)
	if got := h.retention(); got != 1 {
		t.Fatalf("applied retention rolled back after prune failure: %d", got)
	}
	status := s.historyRetentionStatus.Load()
	if status == nil || *status != "prune_failed" {
		t.Fatalf("history health=%v want prune_failed", status)
	}
}

func TestHistoryRetentionConcurrentOperationsRaceClean(t *testing.T) {
	h := newHistory(t.TempDir(), 20)
	seedHistoryWithMeta(t, h, 5)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				h.setRetention((i+j)%7 + 1)
				_, _ = h.list()
				_ = h.pruneCurrent()
			}
		}(i)
	}
	wg.Wait()
}

func TestHistoryPruneLeavesForeignFilesUntouched(t *testing.T) {
	dir := t.TempDir()
	h := newHistory(dir, 1)
	seedHistoryWithMeta(t, h, 3)
	foreign := filepath.Join(dir, "README.txt")
	if err := os.WriteFile(foreign, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.pruneCurrent(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(foreign); err != nil || string(got) != "foreign" {
		t.Fatalf("foreign file changed: data=%q err=%v", got, err)
	}
}
