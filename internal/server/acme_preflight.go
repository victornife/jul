// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"fmt"
	"os"

	"jul/internal/config"
)

// PreflightACMEStartup validates that ACME-enabled server blocks can be
// activated at the next process startup. It probes each unique ACME cache
// directory for writability without contacting any ACME endpoint or retaining
// any file handle.
func PreflightACMEStartup(servers []config.ServerConfig) error {
	seen := make(map[string]struct{})
	for _, s := range servers {
		if s.TLS == nil || s.TLS.ACME == nil || !s.TLS.ACME.Enabled {
			continue
		}
		dir := s.TLS.ACME.CacheDir
		if dir == "" {
			dir = "./jul-data/certs"
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("[tls.acme] cache_dir %q: cannot create directory: %w", dir, err)
		}
		tmp, err := os.CreateTemp(dir, ".preflight-*")
		if err != nil {
			return fmt.Errorf("[tls.acme] cache_dir %q: directory not writable: %w", dir, err)
		}
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}
	return nil
}
