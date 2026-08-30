// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"jul/internal/atomicfile"
	"jul/internal/config"
	"jul/internal/migrate/nginx"
)

const (
	importExitOK         = 0
	importExitInternal   = 1
	importExitUsage      = 2
	importExitFindings   = 3
	importExitParse      = 4
	importExitValidation = 5
	importExitIO         = 6
)

// cmdImport translates or assesses a foreign configuration. The NGINX command
// keeps the original positional/-o grammar and adds explicit assessment modes:
//
//	jul import nginx [--input nginx.conf] [--output jul.toml]
//	jul import nginx --assess [--source-order] nginx.conf
//	jul import nginx --json [--path-style relative|absolute] nginx.conf
//	jul import nginx --follow-includes [--root DIR] --report assessment.json nginx.conf
//
// Exit codes are stable for automation: 0 success, 1 internal error, 2 usage,
// 3 blocking/strict findings, 4 NGINX parse error, 5 generated-candidate
// validation error, and 6 file I/O error.
func cmdImport(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "error: missing import source; usage: jul import nginx [options] <nginx.conf>")
		return importExitUsage
	}
	source := args[0]
	if source != "nginx" {
		fmt.Fprintf(stderr, "error: unknown import source %q; supported sources: nginx\n", source)
		return importExitUsage
	}

	fs := flag.NewFlagSet("import nginx", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var outPath string
	fs.StringVar(&outPath, "o", "", "write the generated config to this file (default: stdout)")
	fs.StringVar(&outPath, "output", "", "write the generated config to this file (alias of -o)")
	inputPath := fs.String("input", "", "path to the NGINX configuration file (alternative to the positional argument)")
	strict := fs.Bool("strict", false, "exit 3 when approximated, ignored, blocking, or Jul lint findings exist")
	assess := fs.Bool("assess", false, "emit a human migration assessment without writing generated config")
	reportPath := fs.String("report", "", "write the versioned JSON assessment to this file")
	jsonOut := fs.Bool("json", false, "emit only the versioned JSON assessment to stdout")
	pathStyleRaw := fs.String("path-style", "relative", "render assessment source paths as relative or absolute")
	sourceOrder := fs.Bool("source-order", false, "render the human assessment in source order (requires --assess)")
	followIncludes := fs.Bool("follow-includes", false, "resolve includes under a bounded assessment root")
	rootPath := fs.String("root", "", "confine include traversal to this directory (default: input file directory)")
	includeRootPath := fs.String("include-root", "", "alias of --root")
	defaults := nginx.DefaultIncludeLimits()
	maxIncludeDepth := fs.Int("max-include-depth", defaults.MaxDepth, "maximum nested include depth")
	maxIncludeFiles := fs.Int("max-include-files", defaults.MaxFiles, "maximum source files, including the root file")
	maxIncludeFileBytes := fs.Int64("max-include-file-bytes", defaults.MaxFileBytes, "maximum bytes read from one source file")
	maxIncludeTotalBytes := fs.Int64("max-include-total-bytes", defaults.MaxTotalBytes, "maximum bytes read across the source tree")
	maxIncludeGlobMatches := fs.Int("max-include-glob-matches", defaults.MaxGlobMatches, "maximum files matched by one include glob")
	if err := fs.Parse(args[1:]); err != nil {
		return importExitUsage
	}

	pathStyle, err := nginx.ParseAssessmentPathStyle(*pathStyleRaw)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return importExitUsage
	}
	if *sourceOrder && !*assess {
		fmt.Fprintln(stderr, "error: --source-order requires --assess because JSON result order is already deterministic")
		return importExitUsage
	}
	includeRoot, err := resolveIncludeRootFlags(*rootPath, *includeRootPath)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return importExitUsage
	}
	if !*followIncludes && includeRoot != "" {
		fmt.Fprintln(stderr, "error: --root/--include-root requires --follow-includes")
		return importExitUsage
	}
	if *maxIncludeDepth < 1 || *maxIncludeFiles < 1 || *maxIncludeFileBytes < 1 || *maxIncludeTotalBytes < 1 || *maxIncludeGlobMatches < 1 {
		fmt.Fprintln(stderr, "error: include limits must be positive")
		return importExitUsage
	}
	assessmentOptions := nginx.AssessmentOptions{PathStyle: pathStyle}
	importOptions := nginx.ImportOptions{
		Assessment:     assessmentOptions,
		FollowIncludes: *followIncludes,
		IncludeRoot:    includeRoot,
		IncludeLimits: nginx.IncludeLimits{
			MaxDepth:       *maxIncludeDepth,
			MaxFiles:       *maxIncludeFiles,
			MaxFileBytes:   *maxIncludeFileBytes,
			MaxTotalBytes:  *maxIncludeTotalBytes,
			MaxGlobMatches: *maxIncludeGlobMatches,
		},
	}

	inPath, err := resolveImportInput(*inputPath, fs.Args())
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return importExitUsage
	}
	if err := validateImportOutputs(outPath, *reportPath, *assess, *jsonOut); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return importExitUsage
	}
	assessmentRequested := *assess || *jsonOut || *reportPath != ""

	cfg, report, err := nginx.ImportFileWithImportOptions(inPath, importOptions)
	if err != nil {
		code, class, findingCode, message := classifyImportReadError(err)
		assessment := nginx.FailureAssessmentWithOptions(inPath, class, findingCode, message, assessmentOptions)
		assessment.SetSourceOrder(*sourceOrder)
		if emitErr := emitImportAssessment(assessment, *assess, *jsonOut, *reportPath); emitErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", emitErr)
			return importExitIO
		}
		if !assessmentRequested {
			fmt.Fprintf(stderr, "error: %s\n", message)
		}
		return code
	}
	if report == nil {
		fmt.Fprintln(stderr, "error: importer returned no migration report")
		return importExitInternal
	}
	assessment := report.Assessment
	if assessment == nil {
		assessment = nginx.FailureAssessmentWithOptions(inPath, nginx.AssessmentValidationError, "NGX_ASSESSMENT_MISSING", "migration assessment was not produced", assessmentOptions)
		report.Assessment = assessment
	}
	assessment.SetSourceOrder(*sourceOrder)

	toml, err := config.Marshal(cfg)
	if err != nil {
		fmt.Fprintln(stderr, "error: could not marshal the translated config")
		return importExitInternal
	}

	// Re-parse and validate the output exactly as the server would. Assessment
	// mode is read-only, but still runs this authoritative candidate check.
	loaded, perr := config.Parse(toml)
	if perr != nil {
		assessment.SetValidation([]error{perr}, nil)
		if emitErr := emitImportAssessment(assessment, *assess, *jsonOut, *reportPath); emitErr != nil {
			fmt.Fprintf(stderr, "error: %v\n", emitErr)
			return importExitIO
		}
		if !assessmentRequested {
			fmt.Fprintln(stderr, "error: translated config did not round-trip; not written")
		}
		return importExitValidation
	}
	verrs := flattenErrors(config.Validate(loaded))
	warns := config.Lint(loaded)
	assessment.SetValidation(verrs, warns)

	if *reportPath != "" {
		if err := writeAssessmentFile(*reportPath, assessment); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return importExitIO
		}
	}
	if *jsonOut {
		if err := writeAssessment(stdout, assessment); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return importExitInternal
		}
		return importAssessmentExit(assessment, *strict, warns)
	}
	if *assess {
		fmt.Fprint(stdout, assessment.Human())
		return importAssessmentExit(assessment, *strict, warns)
	}

	color := wantColor(stderr)
	for _, e := range verrs {
		printDiagnostic(stderr, config.Diagnostic{Severity: config.SeverityError, Message: e.Error()}, color)
	}
	for _, d := range warns {
		printDiagnostic(stderr, d, color)
	}
	if len(verrs) > 0 {
		fmt.Fprintf(stderr, "\nerror: the translated config has %d validation error(s); not written\n", len(verrs))
		return importExitValidation
	}
	if assessment.SourcePolicy.FollowInclude && !assessment.SourcePolicy.Complete {
		fmt.Fprintln(stderr, "error: include traversal was incomplete; generated config was not written")
		return importExitFindings
	}

	// Preserve the existing generated TOML byte contract: assessment metadata is
	// separate and the legacy comment header remains unchanged.
	header := report.Header()
	var body strings.Builder
	body.WriteString(header)
	if !strings.HasSuffix(header, "\n") {
		body.WriteByte('\n')
	}
	body.WriteByte('\n')
	body.Write(toml)
	out := []byte(body.String())

	if outPath != "" {
		if err := atomicfile.Write(outPath, out, 0o600); err != nil {
			fmt.Fprintf(stderr, "error: %v\n", err)
			return importExitIO
		}
		fmt.Fprintf(stderr, "wrote %s\n", outPath)
	} else {
		_, _ = stdout.Write(out)
	}

	fmt.Fprintln(stderr)
	fmt.Fprint(stderr, report.Summary())
	if assessmentRequested && assessment.HasBlocking() {
		return importExitFindings
	}
	if *strict && (assessment.HasWarnings() || len(warns) > 0) {
		return importExitFindings
	}
	return importExitOK
}

func resolveIncludeRootFlags(rootPath, includeRootPath string) (string, error) {
	rootPath = strings.TrimSpace(rootPath)
	includeRootPath = strings.TrimSpace(includeRootPath)
	if rootPath == "" {
		return includeRootPath, nil
	}
	if includeRootPath == "" {
		return rootPath, nil
	}
	left, err := filepath.Abs(rootPath)
	if err != nil {
		return "", fmt.Errorf("resolve --root: %w", err)
	}
	right, err := filepath.Abs(includeRootPath)
	if err != nil {
		return "", fmt.Errorf("resolve --include-root: %w", err)
	}
	if filepath.Clean(left) != filepath.Clean(right) {
		return "", fmt.Errorf("--root and --include-root must name the same directory when both are provided")
	}
	return rootPath, nil
}

func resolveImportInput(flagPath string, positional []string) (string, error) {
	switch {
	case flagPath != "" && len(positional) > 0:
		return "", fmt.Errorf("use either --input or one positional NGINX file, not both")
	case flagPath != "":
		return flagPath, nil
	case len(positional) == 1:
		return positional[0], nil
	case len(positional) == 0:
		return "", fmt.Errorf("provide one NGINX configuration file with --input or as a positional argument")
	default:
		return "", fmt.Errorf("provide exactly one NGINX configuration file")
	}
}

func validateImportOutputs(outPath, reportPath string, assess, jsonOut bool) error {
	if jsonOut && assess {
		return fmt.Errorf("--json and --assess are alternative stdout formats")
	}
	if (jsonOut || assess) && outPath != "" {
		return fmt.Errorf("assessment-only mode does not write generated config; remove --output/-o")
	}
	if jsonOut && reportPath != "" {
		return fmt.Errorf("--json already writes the assessment to stdout; remove --report")
	}
	if reportPath == "-" {
		return fmt.Errorf("use --json for an assessment on stdout; --report requires a file path")
	}
	if outPath != "" && reportPath != "" && outPath == reportPath {
		return fmt.Errorf("generated config and assessment report must use different paths")
	}
	return nil
}

func classifyImportReadError(err error) (int, nginx.AssessmentClass, string, string) {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return importExitIO, nginx.AssessmentParseError, "NGX_INPUT_IO", "NGINX configuration could not be read"
	}
	return importExitParse, nginx.AssessmentParseError, "NGX_PARSE_ERROR", "NGINX configuration could not be parsed"
}

func emitImportAssessment(assessment *nginx.Assessment, human, jsonOut bool, reportPath string) error {
	if reportPath != "" {
		if err := writeAssessmentFile(reportPath, assessment); err != nil {
			return err
		}
	}
	if jsonOut {
		return writeAssessment(stdout, assessment)
	}
	if human {
		fmt.Fprint(stdout, assessment.Human())
	}
	return nil
}

func writeAssessmentFile(path string, assessment *nginx.Assessment) error {
	data, err := assessment.JSON()
	if err != nil {
		return fmt.Errorf("encode assessment: %w", err)
	}
	if err := atomicfile.Write(path, data, 0o600); err != nil {
		return fmt.Errorf("write assessment %s: %w", path, err)
	}
	return nil
}

func writeAssessment(w interface{ Write([]byte) (int, error) }, assessment *nginx.Assessment) error {
	data, err := assessment.JSON()
	if err != nil {
		return fmt.Errorf("encode assessment: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write assessment: %w", err)
	}
	return nil
}

func importAssessmentExit(assessment *nginx.Assessment, strict bool, warnings []config.Diagnostic) int {
	if assessment == nil {
		return importExitInternal
	}
	if assessment.Summary.ValidationErrors > 0 || assessment.Validation.Status == "invalid" {
		return importExitValidation
	}
	if assessment.HasBlocking() {
		return importExitFindings
	}
	if strict && (assessment.HasWarnings() || len(warnings) > 0) {
		return importExitFindings
	}
	return importExitOK
}
