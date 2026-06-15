//go:build !importer

package main

import "fmt"

// cmdImport is the stub used when the binary is built without the importer tag.
// The real implementation (import.go) pulls in the nginx parser, which we keep
// out of the lean default build.
func cmdImport(args []string) int {
	fmt.Fprintln(stderr, "error: `jul import` requires a build with the importer tag (rebuild with -tags importer)")
	return 1
}
