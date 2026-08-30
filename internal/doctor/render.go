// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package doctor

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"jul/internal/diagnostics"
)

// WriteJSON writes the versioned report without ANSI or progress output.
func WriteJSON(writer io.Writer, report diagnostics.Report) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

// RenderText writes deterministic human output in registry order.
func RenderText(writer io.Writer, report diagnostics.Report) error {
	if _, err := fmt.Fprintf(writer, "jul doctor: %s (%d passed, %d warning(s), %d error(s), %d skipped)\n",
		report.Summary.Status,
		report.Summary.Passed,
		report.Summary.Warnings,
		report.Summary.Errors,
		report.Summary.Skipped,
	); err != nil {
		return err
	}
	for _, result := range report.Checks {
		label := strings.ToUpper(string(result.Status))
		if _, err := fmt.Fprintf(writer, "%-7s %-22s %s\n", label, result.Code, result.Message); err != nil {
			return err
		}
		if result.Remediation != "" && result.Status != diagnostics.StatusPass {
			if _, err := fmt.Fprintf(writer, "        remediation: %s\n", result.Remediation); err != nil {
				return err
			}
		}
	}
	return nil
}

// ExitCode applies the repository-wide CLI contract: errors fail with 1;
// warnings fail with 2 only in strict mode; all other reports succeed.
func ExitCode(report diagnostics.Report, strict bool) int {
	if report.Summary.Errors > 0 {
		return 1
	}
	if strict && report.Summary.Warnings > 0 {
		return 2
	}
	return 0
}
