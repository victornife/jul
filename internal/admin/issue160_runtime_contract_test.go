// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
)

func TestAuditPublishDoesNotWaitForPhysicalWriterMutex(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLog(16)
	closeAuditLogOnCleanup(t, a)
	p1, err := a.prepareTransition(mustAuditCfg(t, path, 10, 14))
	if err != nil {
		t.Fatal(err)
	}
	p1.commit()
	owner := a.currentSink.owner

	p2, err := a.prepareTransition(mustAuditCfg(t, path, 5, 7))
	if err != nil {
		t.Fatal(err)
	}
	owner.mu.Lock() // models a physical write blocked inside the sink owner
	done := make(chan struct{})
	go func() { p2.commit(); close(done) }()
	select {
	case <-done:
		// Publish only touched process/event state and owner lifecycle state.
	case <-time.After(250 * time.Millisecond):
		owner.mu.Unlock()
		t.Fatal("Publish waited for physical writer mutex")
	}
	owner.mu.Unlock()
	p2.retire(context.Background())
}

func TestAuditWriteFailureKeepsRingAndAdvancesGlobalID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(16, path, 10, 14, nil)
	closeAuditLogOnCleanup(t, a)
	if a.currentSink == nil {
		t.Fatal("sink not active")
	}
	// Closing the active OS file is a deterministic real-filesystem write failure.
	a.currentSink.owner.mu.Lock()
	if err := a.currentSink.owner.file.Close(); err != nil {
		t.Fatal(err)
	}
	a.currentSink.owner.mu.Unlock()

	a.record(AuditEvent{Operation: "failed-write", Result: "success"})
	a.record(AuditEvent{Operation: "later-write", Result: "success"})
	snap := a.snapshot("", "", 0)
	if len(snap) != 2 || snap[0].ID != 2 || snap[1].ID != 1 {
		t.Fatalf("ring/IDs after write failure = %+v", snap)
	}
	st := a.statusReport()
	if st == nil || st.Healthy || st.WriteFailures != 2 || st.LastFailureCategory != string(auditFailureWrite) {
		t.Fatalf("unexpected failed status: %+v", st)
	}
}

func TestAuditPrepareFailureDoesNotPoisonHealthyLiveSink(t *testing.T) {
	d := t.TempDir()
	aPath := filepath.Join(d, "a.jsonl")
	a := newAuditLogWithSink(16, aPath, 10, 14, nil)
	closeAuditLogOnCleanup(t, a)
	a.record(AuditEvent{Operation: "healthy", Result: "success"})
	before := a.statusReport()
	if before == nil || !before.Healthy {
		t.Fatalf("live A not healthy: %+v", before)
	}

	blocker := filepath.Join(d, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := a.prepareTransition(mustAuditCfg(t, filepath.Join(blocker, "b.jsonl"), 10, 14))
	if err == nil {
		t.Fatal("candidate B unexpectedly prepared")
	}
	after := a.statusReport()
	if after == nil || !after.Healthy || after.Generation != before.Generation {
		t.Fatalf("failed candidate changed live health: before=%+v after=%+v", before, after)
	}
	a.record(AuditEvent{Operation: "still-a", Result: "success"})
	if ids := readAuditIDs(t, aPath); len(ids) != 2 || ids[0] != 1 || ids[1] != 2 {
		t.Fatalf("old sink stopped after failed prepare: %v", ids)
	}
}

func TestAuditDisableClearsActiveDegradationAndRingContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	a := newAuditLogWithSink(16, path, 10, 14, nil)
	a.mu.Lock()
	a.activeFailure = auditFailureWrite
	a.activeFailureAt = time.Now().UTC()
	a.writeFailures = 1
	a.mu.Unlock()
	p, err := a.prepareTransition(auditSinkConfig{})
	if err != nil {
		t.Fatal(err)
	}
	p.commit()
	p.retire(context.Background())
	if st := a.statusReport(); st != nil {
		t.Fatalf("disabled sink should not gate health: %+v", st)
	}
	a.record(AuditEvent{Operation: "ring-only", Result: "success"})
	if got := a.snapshot("", "", 0); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("ring continuity: %+v", got)
	}
}

func TestAdminAuditSinkPatchSparseAndExplicitDisable(t *testing.T) {
	cfg := &config.Config{Admin: config.AdminConfig{
		AuditLogFile:        "a.jsonl",
		AuditLogRotateMaxMB: 100,
		AuditLogRotateKeep:  14,
	}}
	keep := 7
	detail, err := applyPatch(cfg, patchRequest{Op: "admin_audit_sink_set", AdminAuditSink: &adminAuditSinkPatch{RotateKeep: &keep}})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.AuditLogFile != "a.jsonl" || cfg.Admin.AuditLogRotateMaxMB != 100 || cfg.Admin.AuditLogRotateKeep != 7 {
		t.Fatalf("sparse patch touched unrelated fields: %+v", cfg.Admin)
	}
	if !strings.Contains(detail, "audit_log_rotate_keep") {
		t.Fatalf("detail=%q", detail)
	}

	empty := ""
	if _, err := applyPatch(cfg, patchRequest{Op: "admin_audit_sink_set", AdminAuditSink: &adminAuditSinkPatch{File: &empty}}); err != nil {
		t.Fatal(err)
	}
	if cfg.Admin.AuditLogFile != "" {
		t.Fatalf("explicit empty path did not disable: %q", cfg.Admin.AuditLogFile)
	}
}

func TestAdminAuditSinkPatchRejectsNegativeAndEmptyPayload(t *testing.T) {
	cfg := &config.Config{}
	neg := -1
	if _, err := applyPatch(cfg, patchRequest{Op: "admin_audit_sink_set", AdminAuditSink: &adminAuditSinkPatch{RotateMaxMB: &neg}}); err == nil {
		t.Fatal("negative rotation accepted")
	}
	if _, err := applyPatch(cfg, patchRequest{Op: "admin_audit_sink_set", AdminAuditSink: &adminAuditSinkPatch{}}); err == nil {
		t.Fatal("empty sparse patch accepted")
	}
}

func TestReadyzAuditFailureIsBoundedAndDoesNotLeakPath(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "SECRET-AUDIT-PATH", "audit.jsonl")
	s := &Server{audit: newAuditLog(8)}
	s.audit.sinkConfigured = true
	s.audit.sinkCfg = auditSinkConfig{publicPath: secretPath, path: secretPath, maxMB: 100, keep: 14}
	s.audit.activeFailure = auditFailureOpen
	rr := httptest.NewRecorder()
	s.handleReadyz(rr, httptest.NewRequest("GET", "/readyz", nil))
	if rr.Code != 503 {
		t.Fatalf("status=%d", rr.Code)
	}
	body := rr.Body.String()
	if strings.Contains(body, secretPath) || strings.Contains(body, "SECRET-AUDIT-PATH") {
		t.Fatalf("readyz leaked path: %s", body)
	}
	var got map[string]string
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["reason"] != "audit_sink" || got["status"] != "not ready" {
		t.Fatalf("readyz=%v", got)
	}
}

func TestResolveAuditSinkConfigCanonicalDefaultsAndValidation(t *testing.T) {
	cfg, err := resolveAuditSinkConfig("./audit.jsonl", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.maxMB != 100 || cfg.keep != 14 || !cfg.enabled() {
		t.Fatalf("defaults=%+v", cfg)
	}
	if _, err := resolveAuditSinkConfig("audit.jsonl", -1, 14); err == nil {
		t.Fatal("negative max accepted")
	}
	if _, err := resolveAuditSinkConfig("audit.jsonl", 100, -1); err == nil {
		t.Fatal("negative keep accepted")
	}
	if cfg, err := resolveAuditSinkConfig("", 0, 0); err != nil || cfg.enabled() {
		t.Fatalf("empty path=%+v err=%v", cfg, err)
	}
}
