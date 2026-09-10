// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"
)

func mustAuditCfg(t *testing.T, path string, maxMB, keep int) auditSinkConfig {
	t.Helper()
	cfg, err := resolveAuditSinkConfig(path, maxMB, keep)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func readAuditIDs(t *testing.T, paths ...string) []int64 {
	t.Helper()
	var ids []int64
	for _, path := range paths {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var ev AuditEvent
			if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
				_ = f.Close()
				t.Fatalf("parse %s: %v", path, err)
			}
			ids = append(ids, ev.ID)
		}
		if err := sc.Err(); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		_ = f.Close()
	}
	return ids
}

func TestAuditSinkTransitionKeepsRingAndGlobalIDs(t *testing.T) {
	d := t.TempDir()
	aPath := filepath.Join(d, "a.jsonl")
	bPath := filepath.Join(d, "b.jsonl")
	a := newAuditLog(64)

	a.record(AuditEvent{Operation: "before", Result: "success"})
	pa, err := a.prepareTransition(mustAuditCfg(t, aPath, 1, 3))
	if err != nil { t.Fatal(err) }
	pa.commit()
	a.record(AuditEvent{Operation: "on-a", Result: "success"})

	pb, err := a.prepareTransition(mustAuditCfg(t, bPath, 1, 3))
	if err != nil { t.Fatal(err) }
	pb.commit()
	pb.retire(context.Background())
	a.record(AuditEvent{Operation: "on-b", Result: "success"})

	poff, err := a.prepareTransition(auditSinkConfig{})
	if err != nil { t.Fatal(err) }
	poff.commit()
	poff.retire(context.Background())
	a.record(AuditEvent{Operation: "after", Result: "success"})

	snap := a.snapshot("", "", 0)
	if len(snap) != 4 { t.Fatalf("ring len=%d want 4", len(snap)) }
	for i, want := range []int64{4, 3, 2, 1} {
		if snap[i].ID != want { t.Fatalf("snapshot[%d].ID=%d want %d", i, snap[i].ID, want) }
	}
	if got := readAuditIDs(t, aPath); len(got) != 1 || got[0] != 2 { t.Fatalf("A IDs=%v want [2]", got) }
	if got := readAuditIDs(t, bPath); len(got) != 1 || got[0] != 3 { t.Fatalf("B IDs=%v want [3]", got) }
}

func TestAuditSinkSamePathSharesPhysicalOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLog(16)
	p1, err := a.prepareTransition(mustAuditCfg(t, path, 10, 14))
	if err != nil { t.Fatal(err) }
	p1.commit()
	owner := a.currentSink.owner

	p2, err := a.prepareTransition(mustAuditCfg(t, path, 5, 7))
	if err != nil { t.Fatal(err) }
	if p2 == nil || p2.candidate == nil { t.Fatal("expected real rotation-policy transition") }
	if p2.candidate.owner != owner { t.Fatal("same-path transition opened a second physical owner") }
	p2.commit()
	p2.retire(context.Background())
	if a.currentSink.owner != owner { t.Fatal("physical owner changed after same-path handoff") }
	a.record(AuditEvent{Operation: "same-path", Result: "success"})
	if err := a.Close(); err != nil { t.Fatal(err) }
	if got := readAuditIDs(t, path); len(got) != 1 || got[0] != 1 { t.Fatalf("IDs=%v", got) }
}

func TestAuditSinkExactNoopDoesNotChurnGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLog(8)
	cfg := mustAuditCfg(t, path, 0, 0)
	p, err := a.prepareTransition(cfg)
	if err != nil { t.Fatal(err) }
	p.commit()
	gen := a.currentSink.id
	owner := a.currentSink.owner
	p2, err := a.prepareTransition(cfg)
	if err != nil { t.Fatal(err) }
	if p2 != nil { t.Fatal("exact effective config should be a no-op") }
	if a.currentSink.id != gen || a.currentSink.owner != owner { t.Fatal("no-op churned sink generation") }
}

func TestAuditSinkDisabledNoop(t *testing.T) {
	a := newAuditLog(8)
	p, err := a.prepareTransition(auditSinkConfig{})
	if err != nil { t.Fatal(err) }
	if p != nil { t.Fatal("disabled to disabled must not create a generation") }
}

func TestAuditSinkAbortPreservesExistingBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	before := []byte("existing-history\n")
	if err := os.WriteFile(path, before, 0o600); err != nil { t.Fatal(err) }
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 1, 2))
	if err != nil { t.Fatal(err) }
	p.abort()
	after, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	if string(after) != string(before) { t.Fatalf("prepare/abort changed existing history: %q", after) }
}

func TestAuditSinkAbortRemovesCandidateOwnedEmptyArtifacts(t *testing.T) {
	d := t.TempDir()
	parent := filepath.Join(d, "new", "nested")
	path := filepath.Join(parent, "audit.jsonl")
	a := newAuditLog(8)
	p, err := a.prepareTransition(mustAuditCfg(t, path, 1, 2))
	if err != nil { t.Fatal(err) }
	if _, err := os.Stat(path); err != nil { t.Fatalf("candidate placeholder: %v", err) }
	p.abort()
	if _, err := os.Stat(path); !os.IsNotExist(err) { t.Fatalf("candidate file survived abort: %v", err) }
	if _, err := os.Stat(filepath.Join(d, "new")); !os.IsNotExist(err) { t.Fatalf("candidate dirs survived abort: %v", err) }
}

func TestAuditSinkRejectsFinalSymlink(t *testing.T) {
	if os.Getenv("GOOS") == "windows" { t.Skip("symlink privileges vary on Windows") }
	d := t.TempDir()
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, []byte("history"), 0o600); err != nil { t.Fatal(err) }
	link := filepath.Join(d, "audit.jsonl")
	if err := os.Symlink(target, link); err != nil { t.Skipf("symlink unsupported: %v", err) }
	_, err := prepareAuditFileOwner(link)
	if err == nil { t.Fatal("symlink destination accepted") }
	got, _ := os.ReadFile(target)
	if string(got) != "history" { t.Fatal("symlink target was modified") }
}

func TestAuditSinkConcurrentCutoverExactlyOneDestination(t *testing.T) {
	d := t.TempDir()
	aPath := filepath.Join(d, "a.jsonl")
	bPath := filepath.Join(d, "b.jsonl")
	a := newAuditLog(2048)
	pa, err := a.prepareTransition(mustAuditCfg(t, aPath, 8, 3))
	if err != nil { t.Fatal(err) }
	pa.commit()

	const n = 400
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			a.record(AuditEvent{Operation: fmt.Sprintf("race-%03d", i), Result: "success"})
		}(i)
	}
	close(start)
	pb, err := a.prepareTransition(mustAuditCfg(t, bPath, 8, 3))
	if err != nil { t.Fatal(err) }
	pb.commit()
	wg.Wait()
	pb.retire(context.Background())
	if err := a.Close(); err != nil { t.Fatal(err) }

	ids := append(readAuditIDs(t, aPath), readAuditIDs(t, bPath)...)
	if len(ids) != n { t.Fatalf("durable events=%d want %d", len(ids), n) }
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for i, id := range ids {
		if id != int64(i+1) { t.Fatalf("durable ID set has gap/duplicate at %d: %v", i, ids) }
	}
}

func TestAuditSinkRotationCompatibilityAndRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	// Seed close to 1 MiB so a normal audit event forces rotation without a huge test.
	seed := make([]byte, 1024*1024-32)
	for i := range seed { seed[i] = 'x' }
	if err := os.WriteFile(path, seed, 0o640); err != nil { t.Fatal(err) }
	a := newAuditLogWithSink(8, path, 1, 1, nil)
	a.record(AuditEvent{Operation: "rotate", Result: "success", Detail: "forces rotation"})
	if err := a.Close(); err != nil { t.Fatal(err) }
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil { t.Fatal(err) }
	var backups int
	for _, e := range entries {
		if e.Name() != filepath.Base(path) { backups++ }
	}
	if backups != 1 { t.Fatalf("backups=%d want 1", backups) }
	ids := readAuditIDs(t, path)
	if len(ids) != 1 || ids[0] != 1 { t.Fatalf("active rotated sink IDs=%v", ids) }
}

func TestAuditSinkStatusRecoversOnSuccessfulWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 1, 2, nil)
	a.mu.Lock()
	a.activeFailure = auditFailureWrite
	a.activeFailureAt = time.Now().UTC()
	a.writeFailures = 2
	a.mu.Unlock()
	a.record(AuditEvent{Operation: "recover", Result: "success"})
	st := a.statusReport()
	if st == nil || !st.Healthy || st.LastFailureCategory != "" { t.Fatalf("status did not recover: %+v", st) }
	if st.WriteFailures != 2 { t.Fatalf("cumulative failures reset: %+v", st) }
}
