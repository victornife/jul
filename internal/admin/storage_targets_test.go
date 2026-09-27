// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
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
	s.audit = nil
	if _, _, ok := s.AuditWriteHealth(); ok {
		t.Fatal("nil audit reported a durable sink")
	}
}
