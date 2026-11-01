// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"jul/internal/cache"
	"jul/internal/config"
	"jul/internal/plugins"
)

// observeCutoverModules compares candidate byte probes with the immutable
// serving-generation inventory. Re-reading the old source path would see the
// candidate's bytes on both sides after an in-place module replacement.
// Unreadable/missing proof remains absent so the cache owner conservatively
// includes that referenced plugin route; runtime Prepare still compiles and
// pins the exact bytes before committing any cutover.
func observeCutoverModules(ctx context.Context, candidate *config.Config, serving map[string]string) cache.CutoverModuleIdentities {
	observed := cache.CutoverModuleIdentities{Before: serving, After: make(map[string]string)}
	if candidate == nil {
		return observed
	}
	seen := make(map[string]bool)
	for _, srv := range candidate.Servers {
		for _, loc := range srv.Locations {
			if !loc.Cache || loc.Withdrawn {
				continue
			}
			for _, name := range cache.RoutePluginNames(srv, loc) {
				if ctx != nil && ctx.Err() != nil {
					return observed
				}
				if seen[name] {
					continue
				}
				seen[name] = true
				if pc, ok := candidate.Plugins[name]; ok {
					if module, err := plugins.ReadModule(pc); err == nil {
						observed.After[name] = module.Identity.Digest
					}
				}
			}
		}
	}
	return observed
}
