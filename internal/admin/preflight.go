// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"fmt"
	"os"
	"path/filepath"

	"jul/internal/config"
)

// PreflightConfig validates that an AdminConfig can be applied at the next
// process startup. For the history directory, audit-log directory, and plugin
// upload directory it proves writability by creating and immediately removing a
// temporary sentinel file. It does not retain any file handle.
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
	if cfg.PluginUploadDir != "" && cfg.PluginUploadEnabled != nil && *cfg.PluginUploadEnabled {
		if err := probeWritable(cfg.PluginUploadDir, "[admin] plugin_upload_dir"); err != nil {
			return err
		}
	}
	return nil
}

// probeWritable creates and immediately removes a temporary file inside dir to
// verify that the directory is writable without retaining any open handle. If
// the directory does not exist it is created first (MkdirAll), so the check
// also validates that the path can be created under the filesystem root.
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
