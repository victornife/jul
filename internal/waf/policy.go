// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

// PolicyInfo is the bounded description of one compiled WAF policy, taken
// from the engine build that serves it rather than re-read from TOML (#440).
// It never carries a rule file path, rule text, or rule message.
type PolicyInfo struct {
	// Mode is the enforcement mode Jul appended last: "block" or "detect".
	Mode string
	// BlockStatus is the SecDefaultAction status Jul emitted. It applies to
	// non-CRS rules only; CRS anomaly blocks use the CRS setup's own status.
	BlockStatus int
	CRSEnabled  bool
	// CRSVersion is the embedded CRS version, set only when CRSEnabled.
	CRSVersion string
	// Paranoia is the CRS paranoia level Jul set, or the CRS default (1) when
	// ParanoiaDefault; 0 without CRS. A post-CRS rule file may still change it.
	Paranoia        int
	ParanoiaDefault bool
	// RequestBodyLimitBytes is authoritative: Jul applies it after the rule
	// directives, so no SecRequestBodyLimit in a rule file can change it.
	RequestBodyLimitBytes int64
	// ResponseBodyInspection reports response-body access. Its limit is left
	// to the engine and rule directives and is not reported.
	ResponseBodyInspection bool
	// RuleFilesConfigured is the number of directives_files entries.
	RuleFilesConfigured int
	// ExternalFiles is the number of distinct filesystem files the compile
	// read (rule files, their includes and operator data files), and
	// ExternalDigest an aggregate "sha256:" digest of the exact bytes read, in
	// read order. Empty when nothing external was read.
	ExternalFiles  int
	ExternalDigest string
	// InlineRules reports that inline_rules is non-empty.
	InlineRules bool
	Rules       RuleCounts
}

// RuleCounts classifies the compiled rules (SecRule, SecAction, SecMarker) by
// where they came from.
type RuleCounts struct {
	Total int
	// Embedded rules come from the CRS and Coraza configuration shipped in
	// the binary.
	Embedded int
	// External rules come from files on disk.
	External int
	// Inline rules come from inline_rules.
	Inline int
	// Generated rules are emitted by Jul itself (the CRS paranoia SecAction).
	Generated int
}
