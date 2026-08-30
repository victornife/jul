// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ngx "github.com/tufanbarisyildirim/gonginx/config"
	ngxparser "github.com/tufanbarisyildirim/gonginx/parser"
)

const (
	defaultMaxIncludeDepth       = 16
	defaultMaxIncludeFiles       = 256
	defaultMaxIncludeFileBytes   = 4 << 20
	defaultMaxIncludeTotalBytes  = 32 << 20
	defaultMaxIncludeGlobMatches = 1024
)

var errIncludeNotDirectory = errors.New("include path component is not a directory")

// IncludeLimits bounds every filesystem and parser resource consumed by one
// include traversal. Zero values select the conservative defaults.
type IncludeLimits struct {
	MaxDepth       int   `json:"max_depth"`
	MaxFiles       int   `json:"max_files"`
	MaxFileBytes   int64 `json:"max_file_bytes"`
	MaxTotalBytes  int64 `json:"max_total_bytes"`
	MaxGlobMatches int   `json:"max_glob_matches"`
}

// DefaultIncludeLimits returns the stable safe defaults used by the CLI.
func DefaultIncludeLimits() IncludeLimits {
	return IncludeLimits{
		MaxDepth:       defaultMaxIncludeDepth,
		MaxFiles:       defaultMaxIncludeFiles,
		MaxFileBytes:   defaultMaxIncludeFileBytes,
		MaxTotalBytes:  defaultMaxIncludeTotalBytes,
		MaxGlobMatches: defaultMaxIncludeGlobMatches,
	}
}

func normalizedIncludeLimits(in IncludeLimits) IncludeLimits {
	out := DefaultIncludeLimits()
	if in.MaxDepth > 0 {
		out.MaxDepth = in.MaxDepth
	}
	if in.MaxFiles > 0 {
		out.MaxFiles = in.MaxFiles
	}
	if in.MaxFileBytes > 0 {
		out.MaxFileBytes = in.MaxFileBytes
	}
	if in.MaxTotalBytes > 0 {
		out.MaxTotalBytes = in.MaxTotalBytes
	}
	if in.MaxGlobMatches > 0 {
		out.MaxGlobMatches = in.MaxGlobMatches
	}
	return out
}

// ImportOptions extends the assessment presentation options with an explicit,
// root-confined include traversal. The zero value preserves legacy behavior and
// reads only the root file.
type ImportOptions struct {
	Assessment     AssessmentOptions
	FollowIncludes bool
	IncludeRoot    string
	IncludeLimits  IncludeLimits
}

type includeResolution struct {
	Code      string
	Message   string
	SourceIDs []string
}

type resolvedSourceTree struct {
	root              *ngx.Config
	catalog           *sourceCatalog
	rootSource        AssessmentSource
	sourceData        map[string][]byte
	directiveSources  map[ngx.IDirective]AssessmentSource
	includeResolution map[ngx.IDirective]includeResolution
	limits            IncludeLimits
	followIncludes    bool
	complete          bool
	filesRead         int
	totalBytes        int64
	lexicalRoot       string
	evaluatedRoot     string
	active            map[string]struct{}
}

func resolveSourceTree(path string, options ImportOptions) (*resolvedSourceTree, error) {
	assessmentOptions := normalizedAssessmentOptions(options.Assessment)
	limits := normalizedIncludeLimits(options.IncludeLimits)
	lexicalRoot, evaluatedRoot, err := resolveIncludeRoots(path, options.IncludeRoot)
	if err != nil {
		return nil, err
	}
	catalog, err := newSourceCatalog(lexicalRoot, assessmentOptions.PathStyle)
	if err != nil {
		return nil, err
	}
	tree := &resolvedSourceTree{
		catalog:           catalog,
		sourceData:        make(map[string][]byte),
		directiveSources:  make(map[ngx.IDirective]AssessmentSource),
		includeResolution: make(map[ngx.IDirective]includeResolution),
		limits:            limits,
		followIncludes:    options.FollowIncludes,
		complete:          true,
		lexicalRoot:       lexicalRoot,
		evaluatedRoot:     evaluatedRoot,
		active:            make(map[string]struct{}),
	}

	lexicalPath, evaluatedPath, err := tree.confinedPath(path)
	if err != nil {
		return nil, err
	}
	cfg, source, err := tree.readParseRegister(lexicalPath, evaluatedPath, "", 0)
	if err != nil {
		return nil, err
	}
	tree.root = cfg
	tree.rootSource = source
	tree.active[evaluatedPath] = struct{}{}
	tree.resolveBlock(cfg.Block, nil, lexicalPath, source, 0)
	delete(tree.active, evaluatedPath)
	return tree, nil
}

func resolveIncludeRoots(inputPath, configuredRoot string) (string, string, error) {
	root := strings.TrimSpace(configuredRoot)
	if root == "" {
		root = filepath.Dir(inputPath)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve include root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)
	info, err := os.Stat(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("inspect include root: %w", err)
	}
	if !info.IsDir() {
		return "", "", fmt.Errorf("include root is not a directory")
	}
	evaluatedRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", "", fmt.Errorf("evaluate include root: %w", err)
	}
	evaluatedRoot, err = filepath.Abs(evaluatedRoot)
	if err != nil {
		return "", "", fmt.Errorf("resolve evaluated include root: %w", err)
	}
	return absRoot, filepath.Clean(evaluatedRoot), nil
}

func (t *resolvedSourceTree) policy() AssessmentSourcePolicy {
	policy := t.catalog.policy(t.followIncludes)
	policy.Complete = t.complete
	policy.FilesRead = t.filesRead
	policy.TotalBytes = t.totalBytes
	if t.followIncludes {
		limits := t.limits
		policy.Limits = &limits
	}
	return policy
}

func (t *resolvedSourceTree) confinedPath(path string) (string, string, error) {
	lexicalPath, err := filepath.Abs(path)
	if err != nil {
		return "", "", fmt.Errorf("resolve source path: %w", err)
	}
	lexicalPath = filepath.Clean(lexicalPath)
	if !pathWithinRoot(t.lexicalRoot, lexicalPath) {
		return "", "", &includeTraversalError{
			Code:    "NGX_INCLUDE_ROOT_ESCAPE",
			Message: "included source escapes the configured assessment root",
		}
	}
	evaluatedPath, err := filepath.EvalSymlinks(lexicalPath)
	if err != nil {
		return lexicalPath, "", err
	}
	evaluatedPath, err = filepath.Abs(evaluatedPath)
	if err != nil {
		return lexicalPath, "", err
	}
	evaluatedPath = filepath.Clean(evaluatedPath)
	if !pathWithinRoot(t.evaluatedRoot, evaluatedPath) {
		return lexicalPath, evaluatedPath, &includeTraversalError{
			Code:    "NGX_INCLUDE_SYMLINK_ESCAPE",
			Message: "included source symlink escapes the configured assessment root",
		}
	}
	return lexicalPath, evaluatedPath, nil
}

func pathWithinRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// safeGlob expands one include pattern without allowing filepath.Glob to walk
// through an unchecked symlinked directory. Every directory component is
// confined and evaluated before its entries are read.
func (t *resolvedSourceTree) safeGlob(pattern string) ([]string, error) {
	if !pathWithinRoot(t.lexicalRoot, pattern) {
		return nil, &includeTraversalError{
			Code:    "NGX_INCLUDE_ROOT_ESCAPE",
			Message: "include path escapes the configured assessment root",
		}
	}
	rel, err := filepath.Rel(t.lexicalRoot, pattern)
	if err != nil || filepath.IsAbs(rel) {
		return nil, &includeTraversalError{
			Code:    "NGX_INCLUDE_ROOT_ESCAPE",
			Message: "include path escapes the configured assessment root",
		}
	}
	parts := strings.Split(filepath.Clean(rel), string(filepath.Separator))
	for _, part := range parts {
		if strings.ContainsAny(part, "*?[") {
			if _, err := filepath.Match(part, ""); err != nil {
				return nil, err
			}
		}
	}

	candidates := []string{t.lexicalRoot}
	for index, part := range parts {
		if part == "" || part == "." {
			continue
		}
		last := index == len(parts)-1
		hasMeta := strings.ContainsAny(part, "*?[")
		next := make([]string, 0)
		for _, base := range candidates {
			if !hasMeta {
				candidate := filepath.Join(base, part)
				if !last {
					if _, err := t.safeDirectory(candidate); err != nil {
						if errors.Is(err, os.ErrNotExist) || errors.Is(err, errIncludeNotDirectory) {
							continue
						}
						return nil, err
					}
				} else if _, err := os.Lstat(candidate); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						continue
					}
					return nil, err
				}
				next = append(next, candidate)
				continue
			}

			evaluatedBase, err := t.safeDirectory(base)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) || errors.Is(err, errIncludeNotDirectory) {
					continue
				}
				return nil, err
			}
			entries, err := os.ReadDir(evaluatedBase)
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".") && !strings.HasPrefix(part, ".") {
					continue
				}
				matched, err := filepath.Match(part, entry.Name())
				if err != nil {
					return nil, err
				}
				if !matched {
					continue
				}
				candidate := filepath.Join(base, entry.Name())
				if !last {
					if _, err := t.safeDirectory(candidate); err != nil {
						if errors.Is(err, os.ErrNotExist) || errors.Is(err, errIncludeNotDirectory) {
							continue
						}
						return nil, err
					}
				}
				next = append(next, candidate)
				if len(next) > t.limits.MaxGlobMatches {
					return nil, &includeTraversalError{
						Code:    "NGX_INCLUDE_FILE_LIMIT",
						Message: "include glob-match limit reached",
					}
				}
			}
		}
		candidates = next
		if len(candidates) == 0 {
			break
		}
	}
	sort.Strings(candidates)
	return candidates, nil
}

func (t *resolvedSourceTree) safeDirectory(path string) (string, error) {
	_, evaluatedPath, err := t.confinedPath(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(evaluatedPath)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errIncludeNotDirectory
	}
	return evaluatedPath, nil
}

func (t *resolvedSourceTree) readParseRegister(lexicalPath, evaluatedPath, parentID string, includeLine int) (*ngx.Config, AssessmentSource, error) {
	if t.filesRead >= t.limits.MaxFiles {
		return nil, AssessmentSource{}, &includeTraversalError{Code: "NGX_INCLUDE_FILE_LIMIT", Message: "include file-count limit reached"}
	}
	data, err := readBoundedSource(evaluatedPath, t.limits.MaxFileBytes)
	if err != nil {
		return nil, AssessmentSource{}, err
	}
	if t.totalBytes+int64(len(data)) > t.limits.MaxTotalBytes {
		return nil, AssessmentSource{}, &includeTraversalError{Code: "NGX_INCLUDE_BYTE_LIMIT", Message: "include total-byte limit reached"}
	}
	source, err := t.catalog.register(lexicalPath, parentID, includeLine, data)
	if err != nil {
		return nil, AssessmentSource{}, err
	}
	t.filesRead++
	t.totalBytes += int64(len(data))
	t.sourceData[source.ID] = append([]byte(nil), data...)

	cfg, err := parseSourceBytes(data, lexicalPath)
	if err != nil {
		return nil, source, err
	}
	return cfg, source, nil
}

func readBoundedSource(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("source is not a regular file")
	}
	if info.Size() > maxBytes {
		return nil, &includeTraversalError{Code: "NGX_INCLUDE_BYTE_LIMIT", Message: "include file-byte limit reached"}
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, &includeTraversalError{Code: "NGX_INCLUDE_BYTE_LIMIT", Message: "include file-byte limit reached"}
	}
	return data, nil
}

func parseSourceBytes(data []byte, path string) (cfg *ngx.Config, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cfg, err = nil, fmt.Errorf("nginx parse failed: %v", recovered)
		}
	}()
	cfg, err = ngxparser.NewStringParser(string(data), ngxparser.WithSkipValidDirectivesErr()).Parse()
	if cfg != nil {
		cfg.FilePath = path
	}
	return cfg, err
}

type includeTraversalError struct {
	Code    string
	Message string
}

func (e *includeTraversalError) Error() string {
	if e == nil {
		return "include traversal failed"
	}
	return e.Message
}

func (t *resolvedSourceTree) resolveBlock(block ngx.IBlock, parent ngx.IDirective, lexicalPath string, source AssessmentSource, depth int) {
	if block == nil {
		return
	}
	switch typed := block.(type) {
	case *ngx.HTTP:
		t.resolveHTTP(typed, lexicalPath, source, depth)
	case *ngx.Upstream:
		t.resolveUpstream(typed, lexicalPath, source, depth)
	case *ngx.Block:
		typed.Directives = t.resolveDirectives(typed.Directives, parent, lexicalPath, source, depth)
	default:
		for _, directive := range block.GetDirectives() {
			t.bindAndResolveDirective(directive, parent, lexicalPath, source, depth)
		}
	}
}

func (t *resolvedSourceTree) resolveHTTP(http *ngx.HTTP, lexicalPath string, source AssessmentSource, depth int) {
	combined := make([]ngx.IDirective, 0, len(http.Directives)+len(http.Servers))
	combined = append(combined, http.Directives...)
	for _, server := range http.Servers {
		combined = append(combined, server)
	}
	combined = t.resolveDirectives(orderedDirectives(combined), http, lexicalPath, source, depth)
	http.Directives = http.Directives[:0]
	http.Servers = http.Servers[:0]
	for _, directive := range combined {
		if server, ok := directive.(*ngx.Server); ok {
			http.Servers = append(http.Servers, server)
			continue
		}
		http.Directives = append(http.Directives, directive)
	}
}

func (t *resolvedSourceTree) resolveUpstream(upstream *ngx.Upstream, lexicalPath string, source AssessmentSource, depth int) {
	combined := make([]ngx.IDirective, 0, len(upstream.Directives)+len(upstream.UpstreamServers))
	combined = append(combined, upstream.Directives...)
	for _, server := range upstream.UpstreamServers {
		combined = append(combined, server)
	}
	combined = t.resolveDirectives(orderedDirectives(combined), upstream, lexicalPath, source, depth)
	upstream.Directives = upstream.Directives[:0]
	upstream.UpstreamServers = upstream.UpstreamServers[:0]
	for _, directive := range combined {
		if server, ok := directive.(*ngx.UpstreamServer); ok {
			upstream.UpstreamServers = append(upstream.UpstreamServers, server)
			continue
		}
		upstream.Directives = append(upstream.Directives, directive)
	}
}

func (t *resolvedSourceTree) resolveDirectives(in []ngx.IDirective, parent ngx.IDirective, lexicalPath string, source AssessmentSource, depth int) []ngx.IDirective {
	out := make([]ngx.IDirective, 0, len(in))
	for _, directive := range in {
		if directive == nil {
			continue
		}
		directive.SetParent(parent)
		t.directiveSources[directive] = source
		include, isInclude := directive.(*ngx.Include)
		if !isInclude {
			t.resolveBlock(directive.GetBlock(), directive, lexicalPath, source, depth)
			out = append(out, directive)
			continue
		}

		out = append(out, include)
		children := t.resolveInclude(include, lexicalPath, source, depth)
		for _, child := range children {
			child.SetParent(parent)
			out = append(out, child)
		}
	}
	return out
}

func (t *resolvedSourceTree) bindAndResolveDirective(directive, parent ngx.IDirective, lexicalPath string, source AssessmentSource, depth int) {
	if directive == nil {
		return
	}
	directive.SetParent(parent)
	t.directiveSources[directive] = source
	if include, ok := directive.(*ngx.Include); ok {
		_ = t.resolveInclude(include, lexicalPath, source, depth)
		return
	}
	t.resolveBlock(directive.GetBlock(), directive, lexicalPath, source, depth)
}

func (t *resolvedSourceTree) resolveInclude(include *ngx.Include, includingPath string, source AssessmentSource, depth int) []ngx.IDirective {
	if include == nil {
		return nil
	}
	if !t.followIncludes {
		t.complete = false
		t.includeResolution[include] = includeResolution{Code: "NGX_INCLUDE_DISABLED", Message: "include traversal is disabled; rerun with --follow-includes"}
		return nil
	}
	if depth >= t.limits.MaxDepth {
		t.recordIncludeFailure(include, "NGX_INCLUDE_DEPTH_LIMIT", "include depth limit reached", nil)
		return nil
	}

	pattern := strings.TrimSpace(include.IncludePath)
	if pattern == "" {
		t.recordIncludeFailure(include, "NGX_INCLUDE_MISSING", "include path is empty", nil)
		return nil
	}
	if strings.Contains(pattern, "://") {
		t.recordIncludeFailure(include, "NGX_INCLUDE_ROOT_ESCAPE", "network include paths are not allowed", nil)
		return nil
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(filepath.Dir(includingPath), pattern)
	}
	pattern, err := filepath.Abs(pattern)
	if err != nil {
		t.recordIncludeFailure(include, "NGX_INCLUDE_GLOB_INVALID", "include path could not be resolved", nil)
		return nil
	}
	pattern = filepath.Clean(pattern)
	if !pathWithinRoot(t.lexicalRoot, pattern) {
		t.recordIncludeFailure(include, "NGX_INCLUDE_ROOT_ESCAPE", "include path escapes the configured assessment root", nil)
		return nil
	}

	matches, err := t.safeGlob(pattern)
	if err != nil {
		code, message := classifyIncludeGlobError(err)
		t.recordIncludeFailure(include, code, message, nil)
		return nil
	}
	if len(matches) == 0 {
		t.recordIncludeFailure(include, "NGX_INCLUDE_MISSING", "include matched no readable source file", nil)
		return nil
	}
	if len(matches) > t.limits.MaxGlobMatches {
		t.recordIncludeFailure(include, "NGX_INCLUDE_FILE_LIMIT", "include glob-match limit reached", nil)
		return nil
	}

	var inserted []ngx.IDirective
	var childIDs []string
	var firstFailure *includeTraversalError
	include.Configs = include.Configs[:0]
	for _, match := range matches {
		lexicalPath, evaluatedPath, err := t.confinedPath(match)
		if err != nil {
			code, message := classifyIncludeReadError(err)
			if firstFailure == nil {
				firstFailure = &includeTraversalError{Code: code, Message: message}
			}
			continue
		}
		if _, active := t.active[evaluatedPath]; active {
			if firstFailure == nil {
				firstFailure = &includeTraversalError{Code: "NGX_INCLUDE_CYCLE", Message: "include cycle detected"}
			}
			continue
		}

		cfg, childSource, err := t.readParseRegister(lexicalPath, evaluatedPath, source.ID, include.GetLine())
		if err != nil {
			code, message := classifyIncludeReadError(err)
			if firstFailure == nil {
				firstFailure = &includeTraversalError{Code: code, Message: message}
			}
			if childSource.ID != "" {
				childIDs = append(childIDs, childSource.ID)
			}
			continue
		}
		childIDs = append(childIDs, childSource.ID)
		t.active[evaluatedPath] = struct{}{}
		t.resolveBlock(cfg.Block, nil, lexicalPath, childSource, depth+1)
		delete(t.active, evaluatedPath)
		include.Configs = append(include.Configs, cfg)
		inserted = append(inserted, orderedDirectives(cfg.GetDirectives())...)
	}

	if firstFailure != nil {
		t.recordIncludeFailure(include, firstFailure.Code, firstFailure.Message, childIDs)
		return inserted
	}
	t.includeResolution[include] = includeResolution{
		Code:      "NGX_INCLUDE_RESOLVED",
		Message:   fmt.Sprintf("include resolved %d source file(s) inside the configured assessment root", len(childIDs)),
		SourceIDs: append([]string(nil), childIDs...),
	}
	return inserted
}

func classifyIncludeGlobError(err error) (string, string) {
	if errors.Is(err, filepath.ErrBadPattern) {
		return "NGX_INCLUDE_GLOB_INVALID", "include glob is invalid"
	}
	return classifyIncludeReadError(err)
}

func classifyIncludeReadError(err error) (string, string) {
	var traversalErr *includeTraversalError
	if errors.As(err, &traversalErr) {
		return traversalErr.Code, traversalErr.Message
	}
	if errors.Is(err, os.ErrNotExist) {
		return "NGX_INCLUDE_MISSING", "included source does not exist"
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return "NGX_INCLUDE_UNREADABLE", "included source could not be read"
	}
	return "NGX_INCLUDE_PARSE_ERROR", "included source could not be parsed"
}

func (t *resolvedSourceTree) recordIncludeFailure(include ngx.IDirective, code, message string, childIDs []string) {
	t.complete = false
	t.includeResolution[include] = includeResolution{
		Code:      code,
		Message:   message,
		SourceIDs: append([]string(nil), childIDs...),
	}
}

type includeReportFailure struct {
	sourceID string
	line     int
	code     string
	message  string
}

func (t *resolvedSourceTree) applyTranslationReport(report *Report) {
	if t == nil || report == nil {
		return
	}
	filtered := report.Skipped[:0]
	for _, finding := range report.Skipped {
		if finding.Name == "include" && strings.Contains(finding.Reason, "include not followed") {
			continue
		}
		filtered = append(filtered, finding)
	}
	report.Skipped = filtered

	failures := make([]includeReportFailure, 0, len(t.includeResolution))
	for directive, outcome := range t.includeResolution {
		if outcome.Code == "NGX_INCLUDE_RESOLVED" {
			continue
		}
		sourceID := t.rootSource.ID
		if source, ok := t.directiveSources[directive]; ok && source.ID != "" {
			sourceID = source.ID
		}
		failures = append(failures, includeReportFailure{
			sourceID: sourceID,
			line:     directive.GetLine(),
			code:     outcome.Code,
			message:  outcome.Message,
		})
	}
	sort.SliceStable(failures, func(i, j int) bool {
		if failures[i].sourceID != failures[j].sourceID {
			return failures[i].sourceID < failures[j].sourceID
		}
		if failures[i].line != failures[j].line {
			return failures[i].line < failures[j].line
		}
		if failures[i].code != failures[j].code {
			return failures[i].code < failures[j].code
		}
		return failures[i].message < failures[j].message
	})
	for _, failure := range failures {
		report.skipNamed("include", failure.line, failure.message)
	}
}
