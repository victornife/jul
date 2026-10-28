// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package cache

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSensitiveDiskErrorPreservesCauseWithoutCacheHash(t *testing.T) {
	hash := hashKey("fixture-sensitive-key")
	file := filepath.Join(t.TempDir(), hash)
	for _, err := range []error{
		&os.PathError{Op: "remove", Path: file, Err: os.ErrPermission},
		fmt.Errorf("wrapped: %w", &os.LinkError{Op: "rename", Old: file, New: "/tmp/fixture", Err: os.ErrPermission}),
		&os.LinkError{Op: "rename", Old: "/tmp/fixture", New: file, Err: os.ErrPermission},
	} {
		safe := safeDiskError(err)
		if !errors.Is(safe, os.ErrPermission) || strings.Contains(safe.Error(), hash) {
			t.Fatal("disk error did not preserve its cause without the cache-file hash")
		}
	}
	ordinary := &os.PathError{Op: "open", Path: "/tmp/fixture-file", Err: os.ErrNotExist}
	if safeDiskError(ordinary) != ordinary {
		t.Fatal("unrelated filesystem error was modified")
	}
}
