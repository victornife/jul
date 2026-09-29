// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"fmt"
	"io"
	"os"

	"jul/internal/config"
)

// readConfigFile uses the same 16 MiB, regular-file bound as startup. Managed
// apply, recovery and watcher paths must not read a larger disk candidate than
// the serving configuration parser would accept.
func readConfigFile(path string) ([]byte, error) {
	return config.NewTOMLSource(path).ReadRaw()
}

const maxStateMarkerBytes = 64 << 10

func readStateMarker(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStateMarkerBytes {
		return nil, fmt.Errorf("state marker %q must be a regular file of at most %d bytes", path, maxStateMarkerBytes)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxStateMarkerBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxStateMarkerBytes {
		return nil, fmt.Errorf("state marker %q exceeds %d bytes", path, maxStateMarkerBytes)
	}
	return b, nil
}
