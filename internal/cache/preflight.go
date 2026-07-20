// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"fmt"
	"os"

	"jul/internal/config"
)

// Preflight validates that a CacheConfig can be applied at the next process
// startup without consulting the running cache. When a disk tier is configured
// it proves the disk_path directory is writable by creating and immediately
// removing a temporary sentinel file. It does not retain any file handle.
func Preflight(cfg config.CacheConfig) error {
	if !cfg.Enabled || cfg.DiskPath == "" {
		return nil
	}
	if err := os.MkdirAll(cfg.DiskPath, 0o700); err != nil {
		return fmt.Errorf("[cache] disk_path %q: cannot create directory: %w", cfg.DiskPath, err)
	}
	tmp, err := os.CreateTemp(cfg.DiskPath, ".preflight-*")
	if err != nil {
		return fmt.Errorf("[cache] disk_path %q: directory not writable: %w", cfg.DiskPath, err)
	}
	_ = tmp.Close()
	_ = os.Remove(tmp.Name())
	return nil
}
