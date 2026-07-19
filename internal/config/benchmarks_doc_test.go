// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// TestBenchmarksDocTOMLBlocks parses and validates every ```toml block in
// docs/benchmarks.md. This ensures published tuning examples never drift out
// of sync with the current config schema.
func TestBenchmarksDocTOMLBlocks(t *testing.T) {
	blocks := extractBenchmarksTOML(t)
	if len(blocks) == 0 {
		t.Fatal("no TOML blocks found in docs/benchmarks.md")
	}

	for i, block := range blocks {
		block := block
		t.Run(fmt.Sprintf("block_%d_line_%d", i+1, block.line), func(t *testing.T) {
			cfg, err := Parse([]byte(block.text))
			if err != nil {
				t.Fatalf("parse error at line %d:\n%s", block.line, err)
			}
			if err := Validate(cfg); err != nil {
				t.Fatalf("validate error at line %d:\n%s", block.line, err)
			}
		})
	}
}

type tomlBlock struct {
	line int
	text string
}

func extractBenchmarksTOML(t *testing.T) []tomlBlock {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot determine test file location")
	}
	repoRoot := filepath.Join(filepath.Dir(file), "..", "..")
	docPath := filepath.Join(repoRoot, "docs", "benchmarks.md")

	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}
	text := string(data)

	// Match ```toml ... ``` fenced blocks. Normalize CRLF to LF first so the
	// fence regex and line counting are consistent regardless of checkout mode.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	re := regexp.MustCompile("(?s)```toml\n(.*?)```")
	var blocks []tomlBlock
	for _, m := range re.FindAllStringIndex(text, -1) {
		line := strings.Count(text[:m[0]], "\n") + 1
		blockText := text[m[0]:m[1]]
		// Strip fences.
		blockText = strings.TrimPrefix(blockText, "```toml\n")
		blockText = strings.TrimSuffix(blockText, "```")
		blocks = append(blocks, tomlBlock{line: line, text: blockText})
	}
	return blocks
}
