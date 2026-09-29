// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import "jul/internal/config"

// readConfigFile uses the same 16 MiB, regular-file bound as startup. Managed
// apply, recovery and watcher paths must not read a larger disk candidate than
// the serving configuration parser would accept.
func readConfigFile(path string) ([]byte, error) {
	return config.NewTOMLSource(path).ReadRaw()
}
