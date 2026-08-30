// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// AssessmentPathStyle controls how source paths are rendered in shareable
// assessment output. Relative paths are the safe default; absolute paths are
// emitted only after an explicit CLI choice.
type AssessmentPathStyle string

const (
	AssessmentPathRelative AssessmentPathStyle = "relative"
	AssessmentPathAbsolute AssessmentPathStyle = "absolute"
)

// ParseAssessmentPathStyle validates the bounded CLI/JSON path-style value.
func ParseAssessmentPathStyle(raw string) (AssessmentPathStyle, error) {
	switch AssessmentPathStyle(strings.ToLower(strings.TrimSpace(raw))) {
	case "", AssessmentPathRelative:
		return AssessmentPathRelative, nil
	case AssessmentPathAbsolute:
		return AssessmentPathAbsolute, nil
	default:
		return "", fmt.Errorf("path style must be relative or absolute")
	}
}

// AssessmentPosition is a one-based source coordinate. A zero column means the
// parser supplied a line but the lightweight source index could not recover an
// exact column safely.
type AssessmentPosition struct {
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// AssessmentSpan identifies the source interval responsible for a result.
type AssessmentSpan struct {
	SourceID string             `json:"source_id"`
	Start    AssessmentPosition `json:"start"`
	End      AssessmentPosition `json:"end,omitempty"`
}

// AssessmentSource is one deterministic source-file instance in include
// traversal order. Repeated includes intentionally receive separate IDs so the
// report can retain their distinct ancestry and expansion points.
type AssessmentSource struct {
	ID          string `json:"id"`
	DisplayPath string `json:"display_path"`
	Digest      string `json:"digest,omitempty"`
	ParentID    string `json:"parent_id,omitempty"`
	IncludeLine int    `json:"include_line,omitempty"`
}

// AssessmentSourcePolicy records the source-discovery trust boundary without
// exposing a host-specific absolute root in the default relative mode.
type AssessmentSourcePolicy struct {
	PathStyle     AssessmentPathStyle `json:"path_style"`
	Root          string              `json:"root"`
	FollowInclude bool                `json:"follow_includes"`
	Complete      bool                `json:"complete"`
	FilesRead     int                 `json:"files_read"`
	TotalBytes    int64               `json:"total_bytes"`
	Limits        *IncludeLimits      `json:"limits,omitempty"`
}

// AssessmentTargetMapping describes how one source result relates to generated
// Jul configuration paths.
type AssessmentTargetMapping struct {
	Relation string   `json:"relation,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

// AssessmentGuidance is a stable remediation catalogue entry. Finding codes and
// guidance codes are independent so prose can improve without changing the
// machine classification contract.
type AssessmentGuidance struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Action      string `json:"action"`
	Consequence string `json:"consequence,omitempty"`
	Docs        string `json:"docs,omitempty"`
	Blocking    bool   `json:"blocking"`
}

type sourceCatalog struct {
	root   string
	style  AssessmentPathStyle
	nextID int
	items  []AssessmentSource
}

func newSourceCatalog(root string, style AssessmentPathStyle) (*sourceCatalog, error) {
	parsed, err := ParseAssessmentPathStyle(string(style))
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve assessment root: %w", err)
	}
	return &sourceCatalog{root: filepath.Clean(absRoot), style: parsed}, nil
}

func (c *sourceCatalog) policy(follow bool) AssessmentSourcePolicy {
	root := "."
	if c.style == AssessmentPathAbsolute {
		root = filepath.ToSlash(c.root)
	}
	return AssessmentSourcePolicy{
		PathStyle:     c.style,
		Root:          root,
		FollowInclude: follow,
	}
}

func (c *sourceCatalog) register(path, parentID string, includeLine int, data []byte) (AssessmentSource, error) {
	if c == nil {
		return AssessmentSource{}, fmt.Errorf("nil source catalog")
	}
	display, err := assessmentDisplayPath(c.root, path, c.style)
	if err != nil {
		return AssessmentSource{}, err
	}
	c.nextID++
	source := AssessmentSource{
		ID:          fmt.Sprintf("source-%04d", c.nextID),
		DisplayPath: display,
		Digest:      assessmentDigest(data),
		ParentID:    parentID,
		IncludeLine: includeLine,
	}
	c.items = append(c.items, source)
	return source, nil
}

func (c *sourceCatalog) sources() []AssessmentSource {
	if c == nil {
		return nil
	}
	return append([]AssessmentSource(nil), c.items...)
}

func assessmentDisplayPath(root, path string, style AssessmentPathStyle) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve source path: %w", err)
	}
	absPath = filepath.Clean(absPath)
	if style == AssessmentPathAbsolute {
		return filepath.ToSlash(absPath), nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve source root: %w", err)
	}
	rel, err := filepath.Rel(filepath.Clean(absRoot), absPath)
	if err != nil {
		return "", fmt.Errorf("make source path relative: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("source path escapes assessment root")
	}
	if rel == "." {
		rel = filepath.Base(absPath)
	}
	return filepath.ToSlash(filepath.Clean(rel)), nil
}

func assessmentDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
