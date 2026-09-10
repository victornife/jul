// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestIssue160DiskFullRetainsRingDegradesAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	owner := a.currentSink.owner
	base := owner.file
	owner.file = &auditFaultFile{
		base: base,
		writeFn: func([]byte) (int, error) {
			return 0, syscall.ENOSPC
		},
	}

	a.record(AuditEvent{Operation: "disk-full", Result: "success"})
	got := a.snapshot("", "", 0)
	if len(got) != 1 || got[0].ID != 1 || got[0].Operation != "disk-full" {
		t.Fatalf("ring did not retain failed durable event: %+v", got)
	}
	st := a.statusReport()
	if st == nil || st.Healthy || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureWrite) {
		t.Fatalf("disk-full status=%+v", st)
	}

	owner.mu.Lock()
	owner.file = base
	owner.mu.Unlock()
	a.record(AuditEvent{Operation: "disk-recovered", Result: "success"})
	st = a.statusReport()
	if st == nil || !st.Healthy || st.WriteFailures != 1 || st.LastFailureCategory != string(auditFailureWrite) {
		t.Fatalf("health did not recover while preserving cumulative failure history: %+v", st)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue160PersistenceWarningsAreRateLimitedAndNeverReaudit(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, logger)
	owner := a.currentSink.owner
	base := owner.file
	owner.file = &auditFaultFile{
		base: base,
		writeFn: func([]byte) (int, error) {
			return 0, syscall.ENOSPC
		},
	}

	for i := 0; i < 3; i++ {
		a.record(AuditEvent{Operation: "persistence-failure", Result: "success"})
	}
	got := a.snapshot("", "", 0)
	if len(got) != 3 {
		t.Fatalf("operator warning recursively created audit events: ring len=%d want 3", len(got))
	}
	if count := strings.Count(logs.String(), "audit sink persistence failed"); count != 1 {
		t.Fatalf("persistence warning amplification count=%d want 1; logs=%q", count, logs.String())
	}
	st := a.statusReport()
	if st == nil || st.WriteFailures != 3 || st.Healthy {
		t.Fatalf("warning throttle hid failure accounting/health: %+v", st)
	}

	owner.mu.Lock()
	owner.file = base
	owner.mu.Unlock()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestIssue160PhysicalOwnerRegistryIsReleasedAfterDisableRetire(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(8, path, 10, 4, nil)
	if len(a.sinkOwners) != 1 {
		t.Fatalf("startup owner registry size=%d want 1", len(a.sinkOwners))
	}

	disabled, err := a.prepareTransition(auditSinkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	disabled.commit()
	disabled.retire(t.Context())

	a.mu.Lock()
	owners := len(a.sinkOwners)
	a.mu.Unlock()
	if owners != 0 {
		t.Fatalf("retired physical owner remained registered: %d", owners)
	}
	if st := a.statusReport(); st != nil {
		t.Fatalf("disabled sink still reported configured: %+v", st)
	}
}
