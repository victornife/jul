// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

// dump-lifecycle-registry prints the Go lifecycle registry as JSON for
// external validators (scripts/docs-check.py). It is not part of the server
// binary.
package main

import (
	"encoding/json"
	"fmt"
	"os"

	"jul/internal/lifecycle"
)

type entryJSON struct {
	Path      string `json:"path"`
	Class     string `json:"class"`
	Subsystem string `json:"subsystem"`
	Reason    string `json:"reason"`
}

func main() {
	out := make([]entryJSON, 0, len(lifecycle.Registry))
	for _, e := range lifecycle.Registry {
		out = append(out, entryJSON{
			Path:      e.Path,
			Class:     e.Class.String(),
			Subsystem: e.Subsystem,
			Reason:    e.Reason,
		})
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
