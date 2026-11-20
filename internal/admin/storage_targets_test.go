// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"log/slog"
	"path/filepath"
	"reflect"
	"testing"

	"jul/internal/config"
)

func TestStorageTargetsFollowTheAdminRuntime(t *testing.T) {
	var nilServer *Server
	if got := nilServer.StorageTargets(); got != nil {
		t.Fatalf("nil server targets = %v", got)
	}
	if _, _, ok := nilServer.AuditWriteHealth(); ok {
		t.Fatal("nil server reported an audit sink")
	}

	dir := t.TempDir()
	auditFile := filepath.Join(dir, "audit", "audit.jsonl")
	uploads := filepath.Join(dir, "plugins")
	history := filepath.Join(dir, "history")
	mode := "managed"
	s := New(config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:0",
		HistoryDir:          history,
		AuditLogFile:        " " + auditFile + " ",
		PluginUploadDir:     uploads,
		PluginUploadEnabled: config.Bool(true),
		PluginUploadMaxSize: 8,
	}, slog.New(slog.DiscardHandler), Deps{Authority: func() ConfigAuthorityStatus { return ConfigAuthorityStatus{Mode: mode} }})
	t.Cleanup(func() { _ = s.audit.Close() })

	want := []StorageTarget{
		{Category: "audit_log", Path: auditFile},
		{Category: "plugin_upload", Path: uploads},
		{Category: "config_history", Path: history},
	}
	if got := s.StorageTargets(); !reflect.DeepEqual(got, want) {
		t.Fatalf("managed targets = %v, want %v", got, want)
	}

	// file_owned never records history; disabled uploads write nothing.
	mode = "file_owned"
	cfg := s.currentAuth().cfg
	cfg.PluginUploadEnabled = config.Bool(false)
	cfg.AuditLogFile = ""
	s.UpdateAuth(cfg, nil)
	if got := s.StorageTargets(); len(got) != 0 {
		t.Fatalf("file_owned targets = %v", got)
	}

	failures, failing, ok := s.AuditWriteHealth()
	if !ok || failing || failures != 0 {
		t.Fatalf("healthy durable sink = (%d, %v, %v)", failures, failing, ok)
	}
}

func TestAuditWriteHealthWithoutDurableSink(t *testing.T) {
	s := New(config.AdminConfig{Enabled: true, Listen: "127.0.0.1:0"}, slog.New(slog.DiscardHandler), Deps{})
	if _, _, ok := s.AuditWriteHealth(); ok {
		t.Fatal("memory-only audit reported a durable sink")
	}
	if _, _, configured := s.AuditWriteStatus(); configured {
		t.Fatal("memory-only audit reported a configured durable sink")
	}
	s.audit = nil
	if _, _, ok := s.AuditWriteHealth(); ok {
		t.Fatal("nil audit reported a durable sink")
	}
	if _, _, configured := s.AuditWriteStatus(); configured {
		t.Fatal("nil audit reported a configured durable sink")
	}

	failedStartup := newAuditLog(auditCap)
	failedStartup.setStartupSinkFailure(auditSinkConfig{publicPath: "configured"}, auditFailureOpen)
	failedServer := &Server{audit: failedStartup}
	if observed, failing, configured := failedServer.AuditWriteStatus(); !configured || !observed || !failing {
		t.Fatalf("configured sink with observed startup open failure = (%v, %v, %v)", observed, failing, configured)
	}
	_ = failedStartup.Close()
}

func TestAuditWriteStatusSeparatesDurabilityFromMaintenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	s := New(config.AdminConfig{
		Enabled:             true,
		Listen:              "127.0.0.1:0",
		AuditLogFile:        path,
		AuditLogRotateMaxMB: 1,
		AuditLogRotateKeep:  2,
	}, slog.New(slog.DiscardHandler), Deps{})
	t.Cleanup(func() { _ = s.audit.Close() })

	if observed, failing, configured := s.AuditWriteStatus(); !configured || observed || failing {
		t.Fatalf("initial audit write state = (%v, %v, %v), want unknown", observed, failing, configured)
	}
	s.audit.record(AuditEvent{Operation: "write-health", Result: "success"})
	if observed, failing, configured := s.AuditWriteStatus(); !configured || !observed || failing {
		t.Fatalf("successful audit write state = (%v, %v, %v)", observed, failing, configured)
	}

	owner := s.audit.currentSink.owner
	owner.noteOrderedHealth(auditWriteResult{
		category: auditFailureCleanup,
		err:      errors.New("retention cleanup failed"),
		durable:  true,
	})
	if _, failing, configured := s.AuditWriteHealth(); !configured || !failing {
		t.Fatal("retention failure should remain visible to the broader audit-health surface")
	}
	if observed, failing, configured := s.AuditWriteStatus(); !configured || !observed || failing {
		t.Fatalf("maintenance-only state = (%v, %v, %v), want observed success", observed, failing, configured)
	}

	owner.noteOrderedHealth(auditWriteResult{category: auditFailureWrite, err: errors.New("injected write failure")})
	if observed, failing, configured := s.AuditWriteStatus(); !configured || !observed || !failing {
		t.Fatalf("failed audit write state = (%v, %v, %v)", observed, failing, configured)
	}
	owner.noteOrderedHealth(auditWriteResult{durable: true})
	if observed, failing, configured := s.AuditWriteStatus(); !configured || !observed || failing {
		t.Fatalf("recovered audit write state = (%v, %v, %v)", observed, failing, configured)
	}
}
