// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: AGPL-3.0-only

//go:build ignore

package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"jul/internal/migrate/nginx/corpus"
)

func main() {
	root := flag.String("root", "testdata/nginx-corpus", "path to the NGINX migration corpus")
	check := flag.Bool("check", false, "fail when the committed inventory differs")
	write := flag.Bool("write", false, "replace the committed inventory after review")
	flag.Parse()
	if *check && *write {
		fatalf("-check and -write are mutually exclusive")
	}

	fixtures, err := corpus.Discover(*root)
	if err != nil {
		fatalf("discover corpus: %v", err)
	}
	matrix, err := corpus.LoadCoverage(*root)
	if err != nil {
		fatalf("load coverage: %v", err)
	}
	if err := matrix.Validate(fixtures); err != nil {
		fatalf("validate coverage: %v", err)
	}
	data, err := corpus.BuildInventory(fixtures, matrix).JSON()
	if err != nil {
		fatalf("marshal inventory: %v", err)
	}
	path := corpus.InventoryPath(*root)
	switch {
	case *write:
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fatalf("write %s: %v", path, err)
		}
	case *check:
		expected, err := os.ReadFile(path)
		if err != nil {
			fatalf("read %s: %v", path, err)
		}
		if !bytes.Equal(expected, data) {
			fatalf("%s is stale; run `go run -tags importer scripts/nginx-corpus-report.go -write` and review the diff", path)
		}
	default:
		if _, err := os.Stdout.Write(data); err != nil {
			fatalf("write inventory: %v", err)
		}
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "nginx corpus report: "+format+"\n", args...)
	os.Exit(1)
}
