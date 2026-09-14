// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHistoryRetentionPrunesRawAndSidecarAsPair(t *testing.T) {
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	entries, err := h.list()
	if err != nil || len(entries) != 3 {
		t.Fatalf("seed list len=%d err=%v", len(entries), err)
	}
	oldest := entries[len(entries)-1].ID
	if !h.setRetention(2) {
		t.Fatal("3 -> 2 must request a post-Publish prune")
	}
	if err := h.pruneCurrent(); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{historyExt, historyMetaExt} {
		if _, err := os.Stat(filepath.Join(h.dir, oldest+ext)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("oldest %s survived pair prune: %v", ext, err)
		}
	}
}

func TestHistoryRetentionMetadataDeleteFailurePreservesPair(t *testing.T) {
	h := newHistory(t.TempDir(), 2)
	seedHistoryWithMeta(t, h, 2)
	entries, err := h.list()
	if err != nil || len(entries) != 2 {
		t.Fatalf("seed list len=%d err=%v", len(entries), err)
	}
	oldest := entries[len(entries)-1].ID
	h.setRetention(1)
	h.remove = func(path string) error {
		if filepath.Ext(path) == historyMetaExt {
			return errors.New("injected metadata delete failure")
		}
		return os.Remove(path)
	}
	if err := h.pruneCurrent(); err == nil {
		t.Fatal("expected prune degradation")
	}
	for _, ext := range []string{historyExt, historyMetaExt} {
		if _, err := os.Stat(filepath.Join(h.dir, oldest+ext)); err != nil {
			t.Fatalf("pair member %s was removed after metadata failure: %v", ext, err)
		}
	}
}

func TestManagedHistorySnapshotUsesPublishedCandidateRetention(t *testing.T) {
	// Managed history finalization runs only after the apply reaches a terminal
	// committed result. Therefore the candidate retention is already published;
	// the pre-apply rollback snapshot created by that same apply participates in
	// the candidate policy, never a speculative pre-Publish policy.
	h := newHistory(t.TempDir(), 3)
	seedHistoryWithMeta(t, h, 3)
	s := &Server{hist: h}
	if !h.setRetention(1) {
		t.Fatal("3 -> 1 must be a tightening")
	}
	previous := []byte("[global]\nlog_level = \"info\"\n")
	id, err := s.RecordManagedHistory(ApplyRequestContext{Operation: ApplyOperationConfigApply}, ConfigApplyResult{
		ApplyID: "closure-retention",
		OK:      true,
		Mode:    "hot",
	}, previous)
	if err != nil || id == "" {
		t.Fatalf("managed snapshot id=%q err=%v", id, err)
	}
	entries, err := h.list()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].ID != id {
		t.Fatalf("candidate retention did not govern same-apply snapshot: %+v want only %s", entries, id)
	}
}

func TestHistoryRetentionConcurrentSnapshotGetListPruneUpdateRaceClean(t *testing.T) {
	h := newHistory(t.TempDir(), 20)
	seedHistoryWithMeta(t, h, 5)
	var wg sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				h.setRetention((worker+i)%8 + 1)
				id, _, _ := h.snapshotWithMeta([]byte("value = 1\n"), &HistoryMetadata{})
				_, _ = h.list()
				if id != "" {
					_, _ = h.get(id) // deletion races are an allowed not-found result
				}
				_ = h.pruneCurrent()
			}
		}(worker)
	}
	wg.Wait()
}
