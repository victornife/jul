// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "strings"

// StorageTarget is one admin-owned storage location for the storage headroom
// view (#437). Path is internal: callers measure it and must never expose it.
type StorageTarget struct {
	Category string
	Path     string
}

// StorageTargets returns the admin-owned locations the current admin runtime
// generation actually writes: the durable audit sink, the plugin upload
// directory while uploads are enabled, and configuration history while
// configuration authority is managed (a file_owned process never records it).
func (s *Server) StorageTargets() []StorageTarget {
	if s == nil {
		return nil
	}
	cfg := s.currentAuth().cfg
	var out []StorageTarget
	if p := strings.TrimSpace(cfg.AuditLogFile); p != "" {
		out = append(out, StorageTarget{Category: "audit_log", Path: p})
	}
	if pluginUploadEnabled(cfg) && cfg.PluginUploadMaxSize > 0 {
		out = append(out, StorageTarget{Category: "plugin_upload", Path: normalizePluginUploadDir(cfg.PluginUploadDir)})
	}
	if s.hist.enabled() && s.currentAuthority().Mode == "managed" {
		out = append(out, StorageTarget{Category: "config_history", Path: s.hist.dir})
	}
	return out
}

// AuditWriteHealth reports the durable audit sink's cumulative write failures
// and whether it is failing now. ok is false when no durable sink is
// configured.
func (s *Server) AuditWriteHealth() (failures uint64, failing, ok bool) {
	if s == nil || s.audit == nil {
		return 0, false, false
	}
	st := s.audit.statusReport()
	if st == nil {
		return 0, false, false
	}
	return st.WriteFailures, !st.Healthy, true
}
