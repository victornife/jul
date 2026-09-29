// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedConfigReadUsesStartupSizeBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.toml")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate((16 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readConfigFile(path); err == nil {
		t.Fatal("managed raw read accepted a config over startup's limit")
	}
}

func TestStateMarkerRejectsOversizedInput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "marker.json")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxStateMarkerBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readStateMarker(path); err == nil {
		t.Fatal("oversized state marker accepted")
	}
}
