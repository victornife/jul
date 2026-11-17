// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jul/internal/config"
	"jul/internal/router"
)

func validateSpoolIsolation(cfg *config.Config, spoolRoot string) error {
	if cfg == nil || spoolRoot == "" {
		return fmt.Errorf("inspection spool: configuration and root are required")
	}
	spool, err := canonicalPath(spoolRoot)
	if err != nil {
		return fmt.Errorf("inspection spool: resolve root: %w", err)
	}
	for si := range cfg.Servers {
		for li := range cfg.Servers[si].Locations {
			loc := cfg.Servers[si].Locations[li]
			action, err := router.ActionOf(loc)
			if err != nil || action != router.ActionStatic || loc.Root == "" {
				continue
			}
			staticRoot, err := canonicalPath(loc.Root)
			if err != nil {
				return fmt.Errorf("servers[%d].locations[%d].root: resolve static root: %w", si, li, err)
			}
			if pathsOverlap(spool, staticRoot) {
				return fmt.Errorf("servers[%d].locations[%d].root overlaps global.inspection.spool_dir", si, li)
			}
		}
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	current := abs
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func pathsOverlap(a, b string) bool {
	return withinPath(a, b) || withinPath(b, a)
}

func withinPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !filepath.IsAbs(rel) && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
