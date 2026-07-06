// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"flag"
	"fmt"
	"strings"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/migrate/nginx"
)

// cmdImport translates a foreign configuration into a Jul.IA TOML config.
// Currently the only supported source is nginx:
//
//	jul import nginx [-o out.toml] [-strict] <nginx.conf>
//
// The translated config is re-parsed and validated (applying defaults, exactly
// as the server would) before it is emitted, so the output is known to load. A
// report of untranslated directives is written to stderr and embedded as a
// comment header in the output. Exit codes: 0 = ok, 1 = parse/translate error or
// invalid output, 2 = warnings present under -strict.
func cmdImport(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: missing import source; usage: jul import nginx [-o out.toml] [-strict] <nginx.conf>")
		return 1
	}
	source := args[0]
	if source != "nginx" {
		fmt.Fprintf(stderr, "error: unknown import source %q; supported sources: nginx\n", source)
		return 1
	}

	fs := flag.NewFlagSet("import nginx", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("o", "", "write the generated config to this file (default: stdout)")
	strict := fs.Bool("strict", false, "exit non-zero when the generated config has warnings")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "error: provide exactly one nginx configuration file to import")
		return 1
	}
	inPath := fs.Arg(0)

	cfg, report, err := nginx.ImportFile(inPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}

	toml, err := config.Marshal(cfg)
	if err != nil {
		fmt.Fprintf(stderr, "error: could not marshal the translated config: %v\n", err)
		return 1
	}

	// Re-parse and validate the output exactly as the server would, so we never
	// emit a config that will not load. Defaults are applied on Parse.
	loaded, perr := config.Parse(toml)
	if perr != nil {
		fmt.Fprintf(stderr, "error: the translated config did not round-trip: %v\n", perr)
		return 1
	}
	verrs := flattenErrors(config.Validate(loaded))
	warns := config.Lint(loaded)

	// Compose the final document: comment header + TOML body.
	var body strings.Builder
	body.WriteString(report.Header())
	if !strings.HasSuffix(report.Header(), "\n") {
		body.WriteByte('\n')
	}
	body.WriteByte('\n')
	body.Write(toml)
	out := []byte(body.String())

	color := wantColor(stderr)
	for _, e := range verrs {
		printDiagnostic(stderr, config.Diagnostic{Severity: config.SeverityError, Message: e.Error()}, color)
	}
	for _, d := range warns {
		printDiagnostic(stderr, d, color)
	}

	if len(verrs) > 0 {
		fmt.Fprintf(stderr, "\nerror: the translated config has %d validation error(s); not written\n", len(verrs))
		return 1
	}

	if *outPath != "" {
		if err := atomicfile.Write(*outPath, out, 0o600); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return 1
		}
		fmt.Fprintf(stderr, "wrote %s\n", *outPath)
	} else {
		_, _ = stdout.Write(out)
	}

	fmt.Fprintln(stderr)
	fmt.Fprint(stderr, report.Summary())
	if *strict && len(warns) > 0 {
		return 2
	}
	return 0
}
