// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package storagefs

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// FuzzProbeNeverCreatesOrLeaks probes arbitrary configured paths beneath a
// scratch tree: probing must never panic, create anything, or report capacity
// without a real positive total (#437).
func FuzzProbeNeverCreatesOrLeaks(f *testing.F) {
	for _, s := range []string{"", ".", "..", "a/b/c.log", "../../etc", "x\x00y", "link/../../z", "/", "a//b/./c", "\\\\?\\C:\\x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, rel string) {
		dir := t.TempDir()
		if err := os.Mkdir(filepath.Join(dir, "a"), 0o700); err != nil {
			t.Fatal(err)
		}
		before := tree(t, dir)
		res := Probe([]string{filepath.Join(dir, rel), rel})
		if after := tree(t, dir); !slices.Equal(before, after) {
			t.Fatalf("probing %q changed the tree: %v -> %v", rel, before, after)
		}
		for _, r := range res {
			if r.Reason == ReasonNone && r.Group < 0 {
				t.Fatalf("measured result without a group: %+v", r)
			}
			if r.Reason != ReasonNone && (r.Group != -1 || r.HaveAvailable || r.HaveTotal) {
				t.Fatalf("failed result carries capacity: %+v", r)
			}
			if state, ratio := DefaultHints.Classify(r); ratio != nil && (*ratio < 0 || *ratio > 1 || state == StateUnavailable) {
				t.Fatalf("classification %q/%v is not a real fraction for %+v", state, *ratio, r)
			}
		}
	})
}

func tree(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
