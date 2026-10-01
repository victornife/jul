// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"jul/internal/config"
	"jul/internal/middleware"
)

func fileSinkGeneration(t *testing.T, path string, maxMB, keep int) (middleware.AccessSink, *accessFileLease) {
	t.Helper()
	cfg := config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "json", RotateMaxMB: maxMB, RotateKeep: keep}
	sinks, closers, err := BuildAccessSinks(cfg, newBase())
	if err != nil {
		t.Fatal(err)
	}
	lease := closers[0].(*accessFileLease)
	t.Cleanup(func() { _ = lease.Close() })
	return sinks[0], lease
}

// readAccessRecords returns every request ID found in the active file and its
// rotated backups, failing on any line that is not a complete JSON record.
func readAccessRecords(t *testing.T, path string) (ids []string, activeIDs map[string]bool) {
	t.Helper()
	ext := filepath.Ext(path)
	backups, err := filepath.Glob(strings.TrimSuffix(path, ext) + "-*" + ext)
	if err != nil {
		t.Fatal(err)
	}
	activeIDs = map[string]bool{}
	for _, file := range append(backups, path) {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range bytes.Split(bytes.TrimSpace(data), []byte("\n")) {
			if len(line) == 0 {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal(line, &rec); err != nil {
				t.Fatalf("%s line %d is not a complete record: %v", filepath.Base(file), i+1, err)
			}
			id, _ := rec["request_id"].(string)
			ids = append(ids, id)
			if file == path {
				activeIDs[id] = true
			}
		}
	}
	return ids, activeIDs
}

// A rotation-policy change on the same path keeps one writer. The policy is
// adopted on the new generation's first record, an aborted candidate never
// changes it, and the draining generation does not switch it back (#502).
func TestAccessFilePolicyChangeSharesOneWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	oldSink, oldLease := fileSinkGeneration(t, path, 5, 5)
	oldSink.Log(sampleRecord())

	newSink, newLease := fileSinkGeneration(t, path, 5, 2)
	if newLease.state != oldLease.state || oldLease.state.refs != 2 {
		t.Fatal("a policy change on the same path opened a second writer")
	}
	// A candidate that is prepared and aborted never writes.
	_, abortedLease := fileSinkGeneration(t, path, 7, 9)
	if err := abortedLease.Close(); err != nil {
		t.Fatal(err)
	}
	st := oldLease.state
	if st.writer.MaxSize != 5 || st.writer.MaxBackups != 5 {
		t.Fatalf("policy before the new generation wrote = %d MB / %d, want the serving 5 / 5", st.writer.MaxSize, st.writer.MaxBackups)
	}

	newSink.Log(sampleRecord())
	if st.writer.MaxBackups != 2 {
		t.Fatalf("rotate_keep after the new generation's first record = %d, want 2", st.writer.MaxBackups)
	}
	oldSink.Log(sampleRecord()) // draining generation
	if st.writer.MaxBackups != 2 {
		t.Fatalf("the draining generation switched rotate_keep back to %d", st.writer.MaxBackups)
	}
	ids, _ := readAccessRecords(t, path)
	if len(ids) != 3 {
		t.Fatalf("records = %d, want 3", len(ids))
	}

	// Last-owner close releases the path.
	_ = oldLease.Close()
	_ = newLease.Close()
	accessFiles.Lock()
	_, retained := accessFiles.entries[oldLease.path]
	accessFiles.Unlock()
	if retained {
		t.Fatal("last owner did not release the backing writer")
	}
}

// Size-triggered rotation while an old and a new generation with different
// policies both log to the same path: every record is written exactly once,
// intact, and records written after the rotation land in the active file,
// not in a renamed backup (#502).
func TestAccessFilePolicyChangeRotationDuringDrain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	oldSink, _ := fileSinkGeneration(t, path, 2, 10)
	newSink, _ := fileSinkGeneration(t, path, 1, 10)

	pad := strings.Repeat("x", 900)
	const perGen = 1600 // ~3 MB in total: at least two 1 MB rotations
	var wg sync.WaitGroup
	for g, sink := range map[string]middleware.AccessSink{"old": oldSink, "new": newSink} {
		wg.Go(func() {
			for i := range perGen {
				rec := sampleRecord()
				rec.RequestID = fmt.Sprintf("%s-%04d-%s", g, i, pad)
				sink.Log(rec)
			}
		})
	}
	wg.Wait()
	final := sampleRecord()
	final.RequestID = "final-after-rotation"
	oldSink.Log(final)

	ids, active := readAccessRecords(t, path)
	if len(ids) != 2*perGen+1 {
		t.Fatalf("records = %d, want %d (lost or duplicated records)", len(ids), 2*perGen+1)
	}
	sort.Strings(ids)
	for i := 1; i < len(ids); i++ {
		if ids[i] == ids[i-1] {
			t.Fatalf("record %q written twice", ids[i][:12])
		}
	}
	ext := filepath.Ext(path)
	backups, _ := filepath.Glob(strings.TrimSuffix(path, ext) + "-*" + ext)
	if len(backups) == 0 {
		t.Fatal("no rotation happened; the test did not exercise rotation during drain")
	}
	if !active["final-after-rotation"] {
		t.Fatal("a record written after rotation went to a renamed backup instead of the active file")
	}
}
