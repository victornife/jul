// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	"strings"
)

// HumanWithSourcePolicy renders the schema-v2 human report with the complete
// include trust boundary. Human remains available for compatibility; CLI
// surfaces use this method so a followed or incomplete tree is never described
// as "includes disabled".
func (a *Assessment) HumanWithSourcePolicy() string {
	if a == nil {
		return ""
	}
	report := a.Human()
	legacy := fmt.Sprintf("source policy: %s paths, includes disabled\n", a.SourcePolicy.PathStyle)
	return strings.Replace(report, legacy, a.sourcePolicyLine(), 1)
}

func (a *Assessment) sourcePolicyLine() string {
	if a == nil {
		return ""
	}
	if !a.SourcePolicy.FollowInclude {
		return fmt.Sprintf("source policy: %s paths, includes disabled, %d file(s), %d byte(s), complete=%t\n",
			a.SourcePolicy.PathStyle, a.SourcePolicy.FilesRead, a.SourcePolicy.TotalBytes, a.SourcePolicy.Complete)
	}
	line := fmt.Sprintf("source policy: %s paths, includes enabled, %d file(s), %d byte(s), complete=%t",
		a.SourcePolicy.PathStyle, a.SourcePolicy.FilesRead, a.SourcePolicy.TotalBytes, a.SourcePolicy.Complete)
	if limits := a.SourcePolicy.Limits; limits != nil {
		line += fmt.Sprintf(", limits depth=%d files=%d file_bytes=%d total_bytes=%d glob_matches=%d",
			limits.MaxDepth, limits.MaxFiles, limits.MaxFileBytes, limits.MaxTotalBytes, limits.MaxGlobMatches)
	}
	return line + "\n"
}
