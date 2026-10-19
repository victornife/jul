// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"flag"
	"fmt"
	"jul/internal/versionid"
)

func cmdVersionKey(args []string) int {
	if len(args) == 0 || args[0] != "init" {
		fmt.Fprintln(stderr, "usage: jul version-key init [--file PATH]")
		return 2
	}
	fs := flag.NewFlagSet("version-key init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("file", "", "protected effective-version key file (or JUL_EFFECTIVE_VERSION_KEY_FILE)")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(stderr, "version-key init accepts no positional arguments")
		return 2
	}
	if *path == "" {
		p, err := versionid.DefaultPath()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*path = p
	}
	if _, err := versionid.Init(*path); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Effective version key ready at %q. Preserve this protected file in backups.\n", *path)
	return 0
}
