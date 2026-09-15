// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
)

// TestRuntimeHealthyDetectsDegradedAuditSinkAndRepairAfterFix reproduces a
// post-#412 pre-soak review finding: a durable audit sink that failed to open
// must report unhealthy even when its configuration is byte-identical to what
// is currently installed, because the failure could be resolved externally
// (the operator makes the path writable again) without any config edit — and
// RuntimeHealthy must not report healthy again until a real reload actually
// repairs it via PrepareAdminRuntime.
func TestRuntimeHealthyDetectsDegradedAuditSinkAndRepairAfterFix(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	cfg := config.AdminConfig{
		AuditLogFile:        filepath.Join(blocker, "audit.jsonl"),
		AuditLogRotateMaxMB: 100,
		AuditLogRotateKeep:  14,
	}
	s := newTestServer(t, cfg, Deps{})

	if st := s.audit.statusReport(); st == nil || st.Healthy {
		t.Fatalf("audit sink status = %+v, want degraded at startup", st)
	}
	if s.RuntimeHealthy(cfg) {
		t.Fatal("RuntimeHealthy = true, want false while the audit sink is degraded")
	}

	// The operator repairs the filesystem without changing configuration.
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}

	// Still unhealthy: nothing has retried opening the sink yet, matching the
	// documented contract (a no-op reload must not treat this as reusable).
	if s.RuntimeHealthy(cfg) {
		t.Fatal("RuntimeHealthy = true immediately after an external fix, before any reload retried the open")
	}

	// The normal reload path (PrepareAdminRuntime, which a serving-change
	// assessment forced closed for this exact config must still reach) can
	// now repair the sink.
	prepared, err := s.PrepareAdminRuntime(cfg, PrepareAuth(cfg, nil))
	if err != nil {
		t.Fatalf("PrepareAdminRuntime: %v", err)
	}
	s.CommitPreparedAdminRuntime(prepared)

	if !s.RuntimeHealthy(cfg) {
		t.Fatal("RuntimeHealthy = false after the sink was repaired by a real reload")
	}
	if st := s.audit.statusReport(); st == nil || !st.Healthy {
		t.Fatalf("audit sink status = %+v, want healthy after repair", st)
	}
}

// TestRuntimeHealthyDetectsDegradedPluginUploadDirectory covers the same
// invariant for the plugin-upload directory: a preflight failure must report
// unhealthy even with unchanged configuration, and clear once the directory
// is externally repaired.
func TestRuntimeHealthyDetectsDegradedPluginUploadDirectory(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "uploads")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	enabled := true
	cfg := config.AdminConfig{
		PluginUploadEnabled: &enabled,
		PluginUploadMaxSize: 32,
		PluginUploadDir:     blocker,
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{})

	if s.RuntimeHealthy(cfg) {
		t.Fatal("RuntimeHealthy = true, want false while plugin_upload_dir is not a directory")
	}

	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}

	if !s.RuntimeHealthy(cfg) {
		t.Fatal("RuntimeHealthy = false after plugin_upload_dir was externally repaired")
	}
}
