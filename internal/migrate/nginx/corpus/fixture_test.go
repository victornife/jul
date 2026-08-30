// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestRepositoryCorpusLoadsAndIsSanitized(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "testdata", "nginx-corpus"))
	fixtures, err := Discover(root)
	if err != nil {
		t.Fatalf("discover corpus: %v", err)
	}
	if len(fixtures) < 3 {
		t.Fatalf("fixture count = %d, want at least 3", len(fixtures))
	}
	seenSupported, seenApproximate, seenBlocking := false, false, false
	for _, fixture := range fixtures {
		for _, expected := range fixture.Manifest.Assessment.Results {
			switch expected.Class {
			case "supported":
				seenSupported = true
			case "approximated":
				seenApproximate = true
			case "blocking":
				seenBlocking = true
			}
		}
	}
	if !seenSupported || !seenApproximate || !seenBlocking {
		t.Fatalf("corpus classes: supported=%v approximate=%v blocking=%v", seenSupported, seenApproximate, seenBlocking)
	}
}
