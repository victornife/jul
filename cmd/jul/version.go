// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
)

// buildMetadata is the stable output contract of `jul version`. The field set
// and JSON keys are part of the CLI contract: add fields, do not rename or
// remove them.
type buildMetadata struct {
	Product   string `json:"product"`
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Dirty     bool   `json:"dirty"`
	GoVersion string `json:"go_version"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
}

// collectBuildMetadata assembles the version report. The human-facing version
// string comes from the ldflags-injected `version` var (stamped by the release
// pipeline, the Dockerfile, and `make build`). The commit, build time, and dirty
// flag are read from the Go build info the toolchain embeds automatically
// (`vcs.*` settings), so they populate for any `go build` from the repo without
// extra ldflags — and degrade gracefully to "unknown" when absent (e.g. a build
// from an exported tarball with no VCS metadata).
func collectBuildMetadata() buildMetadata {
	m := buildMetadata{
		Product:   productName,
		Version:   version,
		Commit:    "unknown",
		BuildDate: "unknown",
		GoVersion: runtime.Version(),
		OS:        runtime.GOOS,
		Arch:      runtime.GOARCH,
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if info.GoVersion != "" {
			m.GoVersion = info.GoVersion
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				if s.Value != "" {
					m.Commit = s.Value
				}
			case "vcs.time":
				if s.Value != "" {
					m.BuildDate = s.Value
				}
			case "vcs.modified":
				m.Dirty = s.Value == "true"
			}
		}
	}
	return m
}

// writeText renders the human-readable report.
func (m buildMetadata) writeText(w io.Writer) {
	commit := m.Commit
	if m.Dirty {
		commit += " (modified)"
	}
	fmt.Fprintf(w, "%s %s\n", m.Product, m.Version)
	fmt.Fprintf(w, "  commit:    %s\n", commit)
	fmt.Fprintf(w, "  built:     %s\n", m.BuildDate)
	fmt.Fprintf(w, "  go:        %s\n", m.GoVersion)
	fmt.Fprintf(w, "  platform:  %s/%s\n", m.OS, m.Arch)
}

// cmdVersion prints the version and build metadata. With -json it emits the
// machine-readable form for scripts and CI. Exit codes: 0 = ok, 2 = usage error.
func cmdVersion(args []string) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "emit the version report as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	m := collectBuildMetadata()
	if *jsonOut {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "%s\n", b)
		return 0
	}
	m.writeText(stdout)
	return 0
}
