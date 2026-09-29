// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDescriptorSetRejectsOversizedFileBeforeDecode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.pb")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxDescriptorSetBytes + 1); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadRoutesFromFile(path); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("oversized descriptor result = %v", err)
	}
}
