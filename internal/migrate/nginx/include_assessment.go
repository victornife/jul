// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	"strings"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

type resolvedDirectiveDecoration struct {
	directiveDecoration
	source AssessmentSource
	node   ngx.IDirective
}

func buildAssessmentForResolvedTree(src *ngx.Config, source string, report *Report, options AssessmentOptions, tree *resolvedSourceTree) *Assessment {
	assessment := BuildAssessmentWithOptions(src, source, report, options)
	if assessment == nil || tree == nil {
		return assessment
	}
	decorateResolvedAssessment(src, assessment, tree)
	assessment.finalize()
	return assessment
}

func decorateResolvedAssessment(src *ngx.Config, assessment *Assessment, tree *resolvedSourceTree) {
	if assessment == nil || tree == nil {
		return
	}
	assessment.SourcePolicy = tree.policy()
	assessment.Sources = tree.catalog.sources()
	if len(assessment.Sources) > 0 {
		assessment.Source = assessment.Sources[0].DisplayPath
	}

	decorations := collectResolvedDirectiveDecorations(src, tree)
	decorationOrdinal := 0
	sourceOrdinals := make(map[string]int)
	for i := range assessment.Results {
		result := &assessment.Results[i]
		if result.Synthetic {
			result.ID = ""
			result.RelatedResultIDs = nil
			result.Provenance = nil
			continue
		}

		var matched *resolvedDirectiveDecoration
		for decorationOrdinal < len(decorations) {
			decoration := decorations[decorationOrdinal]
			decorationOrdinal++
			if decoration.name != result.Directive {
				continue
			}
			if result.Line > 0 && decoration.line > 0 && result.Line != decoration.line {
				continue
			}
			matched = &decoration
			break
		}
		if matched != nil {
			applyDirectiveDecoration(result, matched.directiveDecoration, matched.source)
			if matched.node != nil && matched.node.GetName() == "include" {
				applyIncludeResolution(result, tree.includeResolution[matched.node])
			}
		}
		if result.Provenance == nil {
			result.Provenance = fallbackProvenance(*result, tree.rootSource)
		}
		sourceID := result.Provenance.SourceID
		if sourceID == "" {
			sourceID = tree.rootSource.ID
		}
		sourceOrdinals[sourceID]++
		result.ID = fmt.Sprintf("result-%s-%04d", sourceID, sourceOrdinals[sourceID])
		result.TargetMappings = targetMappingsForResult(*result)
		if result.Directive == "include" {
			result.GuidanceCodes = includeGuidanceCodes(result.Code)
		} else {
			result.GuidanceCodes = guidanceCodesForResult(*result)
		}
	}

	attachSyntheticRelationships(assessment)
	assessment.refreshGuidance()
}

func applyIncludeResolution(result *AssessmentResult, outcome includeResolution) {
	if result == nil {
		return
	}
	if outcome.Code == "" {
		outcome = includeResolution{
			Code:    "NGX_INCLUDE_DISABLED",
			Message: "include traversal is disabled; rerun with --follow-includes",
		}
	}
	result.Code = outcome.Code
	result.Risk = RiskOperational
	result.Message = outcome.Message
	result.TargetPaths = nil
	result.TargetMappings = nil
	result.Scope = "source"
	if outcome.Code == "NGX_INCLUDE_RESOLVED" {
		result.Class = AssessmentInformational
		result.Severity = AssessmentInfo
		return
	}
	result.Class = AssessmentBlocking
	result.Severity = AssessmentError
}

func includeGuidanceCodes(code string) []string {
	switch code {
	case "NGX_INCLUDE_RESOLVED":
		return nil
	case "NGX_INCLUDE_DISABLED":
		return []string{"GUIDE_INCLUDE_ENABLE"}
	default:
		return []string{"GUIDE_INCLUDE_RESOLVE"}
	}
}

func collectResolvedDirectiveDecorations(src *ngx.Config, tree *resolvedSourceTree) []resolvedDirectiveDecoration {
	matchers := make(map[string]*sourceIndexMatcher, len(tree.sourceData))
	for sourceID, data := range tree.sourceData {
		matchers[sourceID] = &sourceIndexMatcher{items: indexSourceDirectives(data)}
	}

	out := make([]resolvedDirectiveDecoration, 0)
	var walk func(AssessmentContext, []string, []ngx.IDirective)
	walk = func(context AssessmentContext, parentPath []string, directives []ngx.IDirective) {
		siblingCounts := map[string]int{}
		for _, directive := range orderedDirectives(directives) {
			if directive == nil {
				continue
			}
			childContext, recurse := nestedContext(context, directive.GetName())
			contextPath := append([]string(nil), parentPath...)
			if recurse {
				segment := assessmentContextSegment(directive, childContext)
				siblingCounts[segment]++
				if siblingCounts[segment] > 1 {
					segment = fmt.Sprintf("%s#%d", segment, siblingCounts[segment])
				}
				contextPath = append(contextPath, segment)
			}
			if len(contextPath) == 0 {
				contextPath = []string{"main"}
			}

			source, ok := tree.directiveSources[directive]
			if !ok {
				source = tree.rootSource
			}
			start := AssessmentPosition{Line: directive.GetLine()}
			var end AssessmentPosition
			if matcher := matchers[source.ID]; matcher != nil {
				if indexed, matched := matcher.match(directive.GetName(), directive.GetLine()); matched {
					start = indexed.Start
					end = indexed.End
				}
			}
			out = append(out, resolvedDirectiveDecoration{
				directiveDecoration: directiveDecoration{
					name:        directive.GetName(),
					line:        directive.GetLine(),
					start:       start,
					end:         end,
					contextPath: boundedSummary(strings.Join(contextPath, " > ")),
					summary:     safeDirectiveSummary(directive.GetName(), paramValues(directive)),
				},
				source: source,
				node:   directive,
			})
			if recurse {
				walk(childContext, contextPath, orderedChildren(directive))
			}
		}
	}
	walk(ContextMain, nil, topLevelDirectives(src))
	return out
}
