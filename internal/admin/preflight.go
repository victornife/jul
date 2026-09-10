// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"os"
	"path/filepath"

	"jul/internal/config"
)

// PreflightConfig validates admin filesystem targets before persistence. The
// upload directory uses #157's reversible probe so a missing candidate path is
// not created until an upload is actually admitted under the published policy.
func PreflightConfig(cfg config.AdminConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.HistoryDir != "" {
		if err := probeWritable(cfg.HistoryDir, "[admin] history_dir"); err != nil {
			return err
		}
	}
	if cfg.AuditLogFile != "" {
		dir := filepath.Dir(cfg.AuditLogFile)
		if err := probeWritable(dir, "[admin] audit_log_file directory"); err != nil {
			return err
		}
	}
	if cfg.PluginUploadEnabled == nil || *cfg.PluginUploadEnabled {
		if err := preflightPluginUploadDir(cfg.PluginUploadDir); err != nil {
			return err
		}
	}
	return nil
}

func probeWritable(dir, label string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("%s %q: cannot create directory: %w", label, dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".preflight-*")
	if err != nil {
		return fmt.Errorf("%s %q: directory not writable: %w", label, dir, err)
	}
	_ = tmp.Close()
	_ = os.Remove(tmp.Name())
	return nil
}
