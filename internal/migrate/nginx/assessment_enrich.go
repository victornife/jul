// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
)

// AssessmentOptions controls shareable assessment presentation without
// changing translation semantics. Include traversal deliberately remains off
// until MIG-02B (#349).
type AssessmentOptions struct {
	PathStyle AssessmentPathStyle
}

// AssessmentProvenance is the secret-safe source location for one result.
// Raw directive arguments and source excerpts are intentionally excluded.
type AssessmentProvenance struct {
	SourceID    string             `json:"source_id"`
	DisplayPath string             `json:"display_path"`
	Start       AssessmentPosition `json:"start"`
	End         AssessmentPosition `json:"end,omitempty"`
	ContextPath string             `json:"context_path"`
	Directive   string             `json:"directive"`
	Summary     string             `json:"summary"`
}

type directiveDecoration struct {
	name        string
	line        int
	start       AssessmentPosition
	end         AssessmentPosition
	contextPath string
	summary     string
}

type sourceIndexMatcher struct {
	items []indexedDirective
	next  int
}

func normalizedAssessmentOptions(options AssessmentOptions) AssessmentOptions {
	style, err := ParseAssessmentPathStyle(string(options.PathStyle))
	if err != nil {
		style = AssessmentPathRelative
	}
	options.PathStyle = style
	return options
}

func decorateAssessment(src *ngx.Config, source string, assessment *Assessment, options AssessmentOptions) {
	if assessment == nil {
		return
	}
	options = normalizedAssessmentOptions(options)
	policy, rootSource, data := rootAssessmentSource(source, options.PathStyle)
	assessment.SourcePolicy = policy
	assessment.Sources = []AssessmentSource{rootSource}
	assessment.Source = rootSource.DisplayPath

	decorations := collectDirectiveDecorations(src, data)
	parsedOrdinal := 0
	decorationOrdinal := 0
	for i := range assessment.Results {
		result := &assessment.Results[i]
		if result.Synthetic {
			continue
		}
		parsedOrdinal++
		result.ID = fmt.Sprintf("result-%s-%04d", rootSource.ID, parsedOrdinal)
		for decorationOrdinal < len(decorations) {
			decoration := decorations[decorationOrdinal]
			decorationOrdinal++
			if decoration.name != result.Directive {
				continue
			}
			if result.Line > 0 && decoration.line > 0 && result.Line != decoration.line {
				continue
			}
			applyDirectiveDecoration(result, decoration, rootSource)
			break
		}
		if result.Provenance == nil {
			result.Provenance = fallbackProvenance(*result, rootSource)
		}
		result.TargetMappings = targetMappingsForResult(*result)
		result.GuidanceCodes = guidanceCodesForResult(*result)
	}

	attachSyntheticRelationships(assessment)
	assessment.refreshGuidance()
}

func rootAssessmentSource(source string, style AssessmentPathStyle) (AssessmentSourcePolicy, AssessmentSource, []byte) {
	if strings.TrimSpace(source) == "" {
		source = "nginx.conf"
	}
	root := filepath.Dir(source)
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	catalog, err := newSourceCatalog(root, style)
	if err != nil || catalog == nil {
		catalog, err = newSourceCatalog(".", AssessmentPathRelative)
	}
	if err != nil || catalog == nil {
		fallback := AssessmentSource{ID: "source-0001", DisplayPath: filepath.ToSlash(filepath.Base(source))}
		return AssessmentSourcePolicy{PathStyle: AssessmentPathRelative, Root: "."}, fallback, nil
	}
	data, readErr := os.ReadFile(source)
	if readErr == nil {
		registered, registerErr := catalog.register(source, "", 0, data)
		if registerErr == nil {
			return catalog.policy(false), registered, data
		}
	}

	display, displayErr := assessmentDisplayPath(catalog.root, source, catalog.style)
	if displayErr != nil {
		display = filepath.ToSlash(filepath.Base(source))
	}
	fallback := AssessmentSource{ID: "source-0001", DisplayPath: display}
	return catalog.policy(false), fallback, nil
}

func collectDirectiveDecorations(src *ngx.Config, data []byte) []directiveDecoration {
	matcher := sourceIndexMatcher{}
	if len(data) > 0 {
		matcher.items = indexSourceDirectives(data)
	}
	out := make([]directiveDecoration, 0)
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

			start := AssessmentPosition{Line: directive.GetLine()}
			var end AssessmentPosition
			if indexed, ok := matcher.match(directive.GetName(), directive.GetLine()); ok {
				start = indexed.Start
				end = indexed.End
			}
			out = append(out, directiveDecoration{
				name:        directive.GetName(),
				line:        directive.GetLine(),
				start:       start,
				end:         end,
				contextPath: boundedSummary(strings.Join(contextPath, " > ")),
				summary:     safeDirectiveSummary(directive.GetName(), paramValues(directive)),
			})
			if recurse {
				walk(childContext, contextPath, orderedChildren(directive))
			}
		}
	}
	walk(ContextMain, nil, topLevelDirectives(src))
	return out
}

func (m *sourceIndexMatcher) match(name string, line int) (indexedDirective, bool) {
	if m == nil || len(m.items) == 0 || m.next >= len(m.items) {
		return indexedDirective{}, false
	}
	if item := m.items[m.next]; item.Name == name {
		m.next++
		if line == 0 || item.Start.Line == line {
			return item, true
		}
		return indexedDirective{}, false
	}
	for i := m.next; i < len(m.items); i++ {
		item := m.items[i]
		if item.Name == name && (line == 0 || item.Start.Line == line) {
			m.next = i + 1
			return item, true
		}
	}
	return indexedDirective{}, false
}

func applyDirectiveDecoration(result *AssessmentResult, decoration directiveDecoration, source AssessmentSource) {
	if result == nil {
		return
	}
	result.Line = decoration.start.Line
	result.Provenance = &AssessmentProvenance{
		SourceID:    source.ID,
		DisplayPath: source.DisplayPath,
		Start:       decoration.start,
		End:         decoration.end,
		ContextPath: decoration.contextPath,
		Directive:   result.Directive,
		Summary:     decoration.summary,
	}
}

func fallbackProvenance(result AssessmentResult, source AssessmentSource) *AssessmentProvenance {
	contextPath := string(result.Context)
	if contextPath == "" {
		contextPath = "main"
	}
	return &AssessmentProvenance{
		SourceID:    source.ID,
		DisplayPath: source.DisplayPath,
		Start:       AssessmentPosition{Line: result.Line},
		ContextPath: contextPath,
		Directive:   result.Directive,
		Summary:     safeDirectiveSummary(result.Directive, nil),
	}
}

func assessmentContextSegment(directive ngx.IDirective, childContext AssessmentContext) string {
	name := directive.GetName()
	switch childContext {
	case ContextHTTP, ContextEvents, ContextStream, ContextMail:
		return string(childContext)
	case ContextServer:
		return assessmentServerSegment(directive)
	case ContextLocation:
		modifier, path, ok := locationModifierAndPath(directive)
		if !ok || strings.TrimSpace(path) == "" {
			return "location"
		}
		label := strings.TrimSpace(strings.TrimSpace(modifier) + " " + sanitizeSummaryToken(path))
		return boundedSummary("location[" + label + "]")
	case ContextUpstream:
		params := paramValues(directive)
		if len(params) == 0 {
			return "upstream"
		}
		return boundedSummary("upstream[" + sanitizeSummaryToken(params[0]) + "]")
	case ContextLimitExcept:
		params := safeContextValues(paramValues(directive), 4)
		if len(params) == 0 {
			return "limit_except"
		}
		return boundedSummary("limit_except[" + strings.Join(params, ",") + "]")
	case ContextVariable:
		return boundedSummary(name + "[redacted-expression]")
	default:
		return boundedSummary(sanitizeSummaryToken(name))
	}
}

func assessmentServerSegment(directive ngx.IDirective) string {
	var listen string
	var names []string
	for _, child := range orderedChildren(directive) {
		switch child.GetName() {
		case "listen":
			if listen == "" {
				listen, _ = parseListen(paramValues(child))
				if listen == "" {
					values := safeContextValues(paramValues(child), 1)
					if len(values) > 0 {
						listen = values[0]
					}
				}
			}
		case "server_name":
			if len(names) == 0 {
				names = safeContextValues(paramValues(child), 3)
			}
		}
	}
	parts := make([]string, 0, 2)
	if len(names) > 0 {
		parts = append(parts, strings.Join(names, ","))
	}
	if listen != "" {
		parts = append(parts, sanitizeSummaryToken(listen))
	}
	if len(parts) == 0 {
		return "server"
	}
	return boundedSummary("server[" + strings.Join(parts, " @ ") + "]")
}

func safeContextValues(values []string, limit int) []string {
	if limit < 1 || len(values) == 0 {
		return nil
	}
	if len(values) > limit {
		values = values[:limit]
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = sanitizeSummaryToken(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func targetMappingsForResult(result AssessmentResult) []AssessmentTargetMapping {
	paths := append([]string(nil), result.TargetPaths...)
	if len(paths) == 0 {
		paths = inferredTargetPaths(result)
	}
	paths = uniqueSortedStrings(paths)
	if len(paths) == 0 {
		return nil
	}
	relation := "direct"
	switch {
	case strings.HasPrefix(result.Code, "NGX_REALIP"):
		relation = "combines_with_siblings"
	case len(paths) > 1:
		relation = "expands_to_multiple"
	case result.Class == AssessmentApproximated:
		relation = "approximate"
	}
	return []AssessmentTargetMapping{{Relation: relation, Paths: paths}}
}

func inferredTargetPaths(result AssessmentResult) []string {
	switch result.Code {
	case "NGX_SERVER_EXTRA_LISTEN", "NGX_SERVER_LISTEN_OPTION":
		return []string{"servers[].listen", "servers[].tls.enabled"}
	case "NGX_SERVER_RETURN":
		return []string{"servers[].locations[]"}
	case "NGX_LOCATION_ALIAS":
		return []string{"servers[].locations[].root"}
	case "NGX_LOCATION_MATCH":
		return []string{"servers[].locations[].match"}
	case "NGX_LOCATION_PROXY_PASS_URI":
		return []string{"servers[].locations[].proxy_pass", "servers[].locations[].rewrites[]"}
	case "NGX_LOCATION_RETURN_BODY":
		return []string{"servers[].locations[].return"}
	case "NGX_LOCATION_REWRITE_FLAG":
		return []string{"servers[].locations[].rewrites[]"}
	case "NGX_LOCATION_LIMIT_EXCEPT":
		return []string{"servers[].locations[].match.methods"}
	case "NGX_UPSTREAM_IP_HASH", "NGX_UPSTREAM_HASH", "NGX_UPSTREAM_RANDOM":
		return []string{"upstreams[].strategy"}
	case "NGX_UPSTREAM_SERVER_DOWN":
		return []string{"upstreams[].servers[]"}
	default:
		return nil
	}
}

func guidanceCodesForResult(result AssessmentResult) []string {
	if result.Code == "JUL_CANDIDATE_VALIDATION" {
		return []string{"GUIDE_CANDIDATE_VALIDATION"}
	}
	if result.Directive == "include" || strings.Contains(result.Code, "_INCLUDE") {
		return []string{"GUIDE_INCLUDE_ENABLE"}
	}
	if result.Class != AssessmentBlocking && result.Class != AssessmentApproximated {
		return nil
	}
	code := strings.ToUpper(result.Code)
	directive := strings.ToLower(result.Directive)
	switch {
	case strings.Contains(code, "REALIP_HEADER") || strings.Contains(code, "REAL_IP_HEADER"):
		return []string{"GUIDE_REALIP_HEADER"}
	case strings.Contains(code, "REALIP_CONFLICT") || strings.Contains(code, "REAL_IP_CONFLICT"):
		return []string{"GUIDE_REALIP_LISTENER"}
	case strings.Contains(code, "_IF") || result.Context == ContextVariable || directive == "map" || directive == "geo" || directive == "split_clients":
		return []string{"GUIDE_CONDITIONAL_MANUAL"}
	case strings.Contains(code, "PROXY_PASS_DYNAMIC"):
		return []string{"GUIDE_PROXY_TARGET_MANUAL"}
	case strings.Contains(code, "HEADER") || strings.Contains(code, "CORS") || directive == "proxy_set_header" || directive == "add_header":
		return []string{"GUIDE_HEADER_POLICY_MANUAL"}
	case strings.Contains(code, "AUTH") || directive == "allow" || directive == "deny":
		return []string{"GUIDE_AUTH_MANUAL"}
	case strings.Contains(code, "LIMIT") || directive == "client_max_body_size" || directive == "limit_req" || directive == "limit_conn":
		return []string{"GUIDE_LIMITS_MANUAL"}
	case strings.Contains(code, "LOCATION") || strings.Contains(code, "REWRITE") || directive == "alias":
		return []string{"GUIDE_LOCATION_REVIEW"}
	default:
		return []string{"GUIDE_MANUAL_REVIEW"}
	}
}

func attachSyntheticRelationships(assessment *Assessment) {
	if assessment == nil {
		return
	}
	for i := range assessment.Results {
		result := &assessment.Results[i]
		if !result.Synthetic {
			continue
		}
		result.Scope = "global"
		var related []string
		for j := range assessment.Results {
			candidate := assessment.Results[j]
			if candidate.Synthetic || candidate.ID == "" {
				continue
			}
			if result.Line > 0 && candidate.Line == result.Line && (result.Directive == "" || result.Directive == candidate.Directive) {
				related = append(related, candidate.ID)
				if result.Provenance == nil && candidate.Provenance != nil {
					copy := *candidate.Provenance
					result.Provenance = &copy
				}
				continue
			}
			if strings.HasPrefix(result.Code, "NGX_REALIP") && isRealIPDirective(candidate.Directive) {
				related = append(related, candidate.ID)
			}
		}
		if len(related) > 0 {
			result.Scope = "derived"
			result.RelatedResultIDs = uniqueSortedStrings(related)
		}
		result.TargetMappings = targetMappingsForResult(*result)
		result.GuidanceCodes = guidanceCodesForResult(*result)
	}
}

func (assessment *Assessment) refreshGuidance() {
	if assessment == nil {
		return
	}
	used := map[string]struct{}{}
	for _, result := range assessment.Results {
		for _, code := range result.GuidanceCodes {
			if _, ok := lookupAssessmentGuidance(code); ok {
				used[code] = struct{}{}
			}
		}
	}
	codes := make([]string, 0, len(used))
	for code := range used {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	assessment.Guidance = assessment.Guidance[:0]
	for _, code := range codes {
		guidance, _ := lookupAssessmentGuidance(code)
		assessment.Guidance = append(assessment.Guidance, guidance)
	}
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
