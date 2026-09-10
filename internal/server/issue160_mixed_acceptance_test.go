// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"context"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestIssue160MixedCertificateAndAuditPublishInOneReloadPlan(t *testing.T) {
	dir := t.TempDir()
	certA, keyA := writeSelfSigned(t, dir, "cert-a", "a.example.com")
	certB, keyB := writeSelfSigned(t, dir, "cert-b", "a.example.com")
	addr := freePort(t)

	initial := tlsCfgFor(addr, certA, keyA, "a.example.com")
	initial.Admin = config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:19090",
		Token:               "issue160-token",
		AuditLogFile:        filepath.Join(dir, "audit-a.jsonl"),
		AuditLogRotateMaxMB: 100,
		AuditLogRotateKeep:  14,
	}
	candidate := tlsCfgFor(addr, certB, keyB, "a.example.com")
	candidate.Admin = initial.Admin
	candidate.Admin.AuditLogFile = filepath.Join(dir, "audit-b.jsonl")
	candidate.Admin.AuditLogRotateMaxMB = 32
	candidate.Admin.AuditLogRotateKeep = 5

	srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue160Factory(addr), nil, nil)
	srv.baseCtx = context.Background()
	entry := boundTLSEntry(t, srv, initial, addr)
	srv.listeners[addr] = entry
	oldCertFP := entry.certFingerprint

	resolvedCandidate, err := config.NewCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var adminCommitted atomic.Int64
	var adminAborted atomic.Int64
	preparedAdmin := NewPreparedCommit(
		func() { adminCommitted.Add(1) },
		func() { adminAborted.Add(1) },
	)
	plan := srv.newReloadPlan(context.Background(), candidate, resolvedCandidate, preparedAdmin)
	if err := plan.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Lifecycle(); err != nil {
		t.Fatalf("certificate+audit candidate unexpectedly restart-bound: %v", err)
	}
	if err := plan.Prepare(); err != nil {
		t.Fatal(err)
	}
	if err := plan.StageListeners(); err != nil {
		t.Fatal(err)
	}
	if _, err := plan.Publish(); err != nil {
		t.Fatal(err)
	}

	if adminCommitted.Load() != 1 || adminAborted.Load() != 0 {
		t.Fatalf("prepared admin commit=%d abort=%d want 1/0", adminCommitted.Load(), adminAborted.Load())
	}
	if entry.certFingerprint == oldCertFP {
		t.Fatal("certificate provider did not rotate in the mixed publish")
	}
	live := srv.LiveSnapshot().EffectiveConfig
	if live == nil || live.Admin.AuditLogFile != candidate.Admin.AuditLogFile || live.Admin.AuditLogRotateMaxMB != 32 || live.Admin.AuditLogRotateKeep != 5 {
		t.Fatalf("audit settings not published with certificate rotation: %+v", live)
	}
	if got := live.Servers[0].TLS.Cert; got != certB {
		t.Fatalf("live certificate path=%q want %q", got, certB)
	}
}

func TestIssue160MixedHistoryAndAuditCandidateRejectsBeforePublish(t *testing.T) {
	addr := freePort(t)
	dir := t.TempDir()
	initial := cfgWith(addr)
	initial.Admin = config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:19090",
		Token:               "issue160-token",
		HistoryDir:          filepath.Join(dir, "history-a"),
		HistoryKeep:         50,
		AuditLogFile:        filepath.Join(dir, "audit-a.jsonl"),
		AuditLogRotateMaxMB: 100,
		AuditLogRotateKeep:  14,
	}
	candidate := cfgWith(addr)
	candidate.Admin = initial.Admin
	candidate.Admin.HistoryDir = filepath.Join(dir, "history-b")
	candidate.Admin.HistoryKeep = 25
	candidate.Admin.AuditLogFile = filepath.Join(dir, "audit-b.jsonl")
	candidate.Admin.AuditLogRotateKeep = 3

	srv := New(initial, initial, lifecycle.Fingerprint{}, quietLogger(), issue160Factory(addr), nil, nil)
	resolvedCandidate, err := config.NewCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	var adminCommitted atomic.Int64
	var adminAborted atomic.Int64
	preparedAdmin := NewPreparedCommit(
		func() { adminCommitted.Add(1) },
		func() { adminAborted.Add(1) },
	)
	plan := srv.newReloadPlan(context.Background(), candidate, resolvedCandidate, preparedAdmin)
	if err := plan.Resolve(); err != nil {
		t.Fatal(err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	err = plan.Lifecycle()
	if err == nil || !strings.Contains(err.Error(), "restart_required") {
		t.Fatalf("history+audit lifecycle=%v want restart_required", err)
	}
	plan.Abort()

	if adminCommitted.Load() != 0 || adminAborted.Load() != 1 {
		t.Fatalf("restart-bound mixed candidate commit=%d abort=%d want 0/1", adminCommitted.Load(), adminAborted.Load())
	}
	live := srv.LiveSnapshot().EffectiveConfig
	if live == nil || live.Admin.HistoryDir != initial.Admin.HistoryDir || live.Admin.HistoryKeep != initial.Admin.HistoryKeep || live.Admin.AuditLogFile != initial.Admin.AuditLogFile || live.Admin.AuditLogRotateKeep != initial.Admin.AuditLogRotateKeep {
		t.Fatalf("restart-bound history+audit candidate partially published: %+v", live)
	}
}
