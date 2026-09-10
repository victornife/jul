// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package lifecycle

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestIssue157GenerateTransfer is temporary scaffolding used only to run the
// authoritative generator in GitHub Actions where the repository/toolchain are
// available. It is removed before the implementation PR is finalized.
func TestIssue157GenerateTransfer(t *testing.T) {
	if os.Getenv("GITHUB_ACTIONS") != "true" {
		t.Skip("CI transfer helper")
	}
	cmd := exec.Command("go", "generate", ".")
	cmd.Dir = "."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go generate: %v\n%s", err, out)
	}
	paths := []string{
		filepath.Join("..", "..", "docs", "config-lifecycle.yaml"),
		filepath.Join("..", "..", "docs", "generated", "config-lifecycle.md"),
		filepath.Join("..", "..", "docs", "generated", "config-lifecycle.json"),
	}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var compressed bytes.Buffer
		zw := gzip.NewWriter(&compressed)
		if _, err := zw.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("ISSUE157_GENERATED_BEGIN %s\n%s\nISSUE157_GENERATED_END %s\n", filepath.Base(path), base64.StdEncoding.EncodeToString(compressed.Bytes()), filepath.Base(path))
	}
	t.Fatal("intentional transfer stop; remove helper after generated mirrors are committed")
}
