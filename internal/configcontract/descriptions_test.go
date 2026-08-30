// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"path/filepath"
	"testing"

	"jul/internal/config"
)

func loadTestFieldComments(t *testing.T) map[string]string {
	t.Helper()
	comments, err := LoadFieldComments(filepath.Join(repoRoot(t), "internal", "config", "schema.go"))
	if err != nil {
		t.Fatalf("LoadFieldComments: %v", err)
	}
	return comments
}

// TestDescribeLeafCoversEverySchemaLeaf proves every public configurable leaf
// has a description, either derived from its Go doc comment or supplied by an
// explicit, reviewed override — so a new SchemaLeaves() entry cannot land
// without either (ADR 0019 §9/§21).
func TestDescribeLeafCoversEverySchemaLeaf(t *testing.T) {
	comments := loadTestFieldComments(t)
	var missing []string
	for _, p := range config.SchemaLeaves() {
		if _, ok := DescribeLeaf(comments, p); !ok {
			missing = append(missing, p.Path)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d schema leaves have no description (add a Go doc comment or a DescriptionOverrides entry): %v", len(missing), missing)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		want string
	}{
		{"simple", "MaxUDPSessions caps sessions. Zero applies a default.", "MaxUDPSessions caps sessions"},
		{"single sentence no trailing period handling", "Enabled toggles the feature.", "Enabled toggles the feature"},
		{"e.g. mid-sentence is not a sentence boundary", "Accepts values, e.g. gzip or br, in preference order. Second sentence.", "Accepts values, e.g. gzip or br, in preference order"},
		{"no period at all", "Just a fragment", "Just a fragment"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firstSentence(tc.doc); got != tc.want {
				t.Errorf("firstSentence(%q) = %q, want %q", tc.doc, got, tc.want)
			}
		})
	}
}
