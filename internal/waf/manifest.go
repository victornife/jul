// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package waf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"jul/internal/config"
	"jul/internal/versionid"
)

// PolicyManifestSchemaVersion is the schema version for WAFPolicyManifest (#630).
const PolicyManifestSchemaVersion = 1

// ExternalAssetRecord describes one external SecLang rule, include, or dataset
// file read during WAF policy compilation (#630, #584). It is retained only for
// local manifest/recovery verification and config:raw callers, never in
// status:read projections (#649).
type ExternalAssetRecord struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// PolicyManifest is the reproducible identity and external-asset inventory for
// a compiled WAF policy (#630, #584, ADR 0031 §3).
type PolicyManifest struct {
	SchemaVersion             int                   `json:"schema_version"`
	GeneratedAt               string                `json:"generated_at"`
	Scope                     string                `json:"scope"`
	WAFCompiled               bool                  `json:"waf_compiled"`
	EngineVersion             string                `json:"engine_version,omitempty"`
	EmbeddedCRSVersion        string                `json:"embedded_crs_version,omitempty"`
	Mode                      string                `json:"mode"`
	BlockStatus               int                   `json:"block_status"`
	CRSEnabled                bool                  `json:"crs_enabled"`
	Paranoia                  int                   `json:"paranoia,omitempty"`
	BlockingParanoia          int                   `json:"blocking_paranoia,omitempty"`
	DetectionParanoia         int                   `json:"detection_paranoia,omitempty"`
	ExceptionsCount           int                   `json:"exceptions_count,omitempty"`
	ExceptionsReviewDue       int                   `json:"exceptions_review_due,omitempty"`
	HasCustomTuningDirectives bool                  `json:"has_custom_tuning_directives,omitempty"`
	RequestBodyLimitBytes     int64                 `json:"request_body_limit_bytes,omitempty"`
	ResponseBodyInspection    bool                  `json:"response_body_inspection"`
	PolicyDigest              string                `json:"policy_digest,omitempty"`
	ExternalDigest            string                `json:"external_digest,omitempty"`
	RawPolicySHA256           string                `json:"raw_policy_sha256,omitempty"`
	RawExternalSHA256         string                `json:"raw_external_sha256,omitempty"`
	InlineRulesSHA256         string                `json:"inline_rules_sha256,omitempty"`
	Rules                     RuleCounts            `json:"rules"`
	ExternalAssets            []ExternalAssetRecord `json:"external_assets,omitempty"`
}

// AssetVerificationResult reports whether the current on-disk external files,
// WAF configuration, and binary embedded engine/CRS match a saved
// PolicyManifest (#630, #584).
type AssetVerificationResult struct {
	Verified            bool     `json:"verified"`
	Status              string   `json:"status"` // "verified", "package_mismatch", "asset_missing", "asset_changed", "policy_mismatch", "compile_failed"
	Scope               string   `json:"scope"`
	EngineMatch         bool     `json:"engine_match"`
	CRSMatch            bool     `json:"crs_match"`
	PolicyMatch         bool     `json:"policy_match"`
	ExternalAssetsMatch bool     `json:"external_assets_match"`
	Mismatches          []string `json:"mismatches,omitempty"`
}

// BuildPolicyManifest compiles cfg and returns its complete PolicyManifest and
// compiled Firewall (#630). All external files are read once through the
// compilation recorder; there is no hash-then-reread window.
func BuildPolicyManifest(ctx context.Context, scope string, cfg config.WAFConfig, versions *versionid.Context) (*PolicyManifest, *Firewall, error) {
	if scope == "" {
		scope = "global"
	}
	if versions == nil {
		versions = versionid.Process()
	}
	if cfg.Mode == "" {
		cfg.Mode = "block"
	}
	if cfg.BlockStatus == 0 {
		cfg.BlockStatus = 403
	}
	if cfg.RequestBodyLimit == 0 {
		cfg.RequestBodyLimit = config.Size(128 << 10)
	}
	fw, err := New(ctx, cfg, Options{Versions: versions})
	if err != nil {
		return nil, nil, err
	}
	info := fw.Info()
	var inlineSHA string
	if trimmed := strings.TrimSpace(cfg.InlineRules); trimmed != "" {
		sum := sha256.Sum256([]byte(trimmed))
		inlineSHA = hex.EncodeToString(sum[:])
	}
	m := &PolicyManifest{
		SchemaVersion:             PolicyManifestSchemaVersion,
		GeneratedAt:               time.Now().UTC().Format(time.RFC3339),
		Scope:                     scope,
		WAFCompiled:               Compiled,
		EngineVersion:             EngineVersion(),
		EmbeddedCRSVersion:        EmbeddedCRSVersion(),
		Mode:                      info.Mode,
		BlockStatus:               info.BlockStatus,
		CRSEnabled:                info.CRSEnabled,
		Paranoia:                  info.Paranoia,
		BlockingParanoia:          info.BlockingParanoia,
		DetectionParanoia:         info.DetectionParanoia,
		ExceptionsCount:           info.ExceptionsCount,
		ExceptionsReviewDue:       info.ExceptionsReviewDue,
		HasCustomTuningDirectives: info.HasCustomTuningDirectives,
		RequestBodyLimitBytes:     info.RequestBodyLimitBytes,
		ResponseBodyInspection:    info.ResponseBodyInspection,
		PolicyDigest:              info.PolicyDigest,
		ExternalDigest:            info.ExternalDigest,
		RawPolicySHA256:           fw.RawPolicyDigest(),
		RawExternalSHA256:         fw.SourceDigest(),
		InlineRulesSHA256:         inlineSHA,
		Rules:                     info.Rules,
		ExternalAssets:            fw.ExternalAssets(),
	}
	return m, fw, nil
}

// ParsePolicyManifest strictly decodes and validates a JSON PolicyManifest (#630).
func ParsePolicyManifest(data []byte) (*PolicyManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m PolicyManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("waf manifest: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("waf manifest: trailing data after JSON object")
	}
	if m.SchemaVersion != PolicyManifestSchemaVersion {
		return nil, fmt.Errorf("waf manifest: unsupported schema_version %d (want %d)", m.SchemaVersion, PolicyManifestSchemaVersion)
	}
	if strings.TrimSpace(m.Scope) == "" {
		return nil, errors.New("waf manifest: scope is required")
	}
	if m.Mode != "block" && m.Mode != "detect" {
		return nil, fmt.Errorf("waf manifest: invalid mode %q", m.Mode)
	}
	if m.RawPolicySHA256 == "" {
		return nil, errors.New("waf manifest: raw_policy_sha256 is required")
	}
	return &m, nil
}

// VerifyPolicyAssets verifies that cfg, its on-disk external files/includes/data
// files, and the running binary's linked Coraza/CRS versions match manifest (#630, #584).
func VerifyPolicyAssets(ctx context.Context, cfg config.WAFConfig, manifest *PolicyManifest, versions *versionid.Context) (*AssetVerificationResult, error) {
	if manifest == nil {
		return nil, errors.New("waf manifest is nil")
	}
	if manifest.SchemaVersion != PolicyManifestSchemaVersion {
		return nil, fmt.Errorf("waf manifest: unsupported schema_version %d", manifest.SchemaVersion)
	}
	res := &AssetVerificationResult{
		Scope:               manifest.Scope,
		EngineMatch:         true,
		CRSMatch:            true,
		PolicyMatch:         true,
		ExternalAssetsMatch: true,
	}

	// 1. Check binary package (engine and embedded CRS) compatibility.
	curEngine := EngineVersion()
	if manifest.EngineVersion != "" && curEngine != "" && manifest.EngineVersion != curEngine {
		res.EngineMatch = false
		res.Mismatches = append(res.Mismatches, fmt.Sprintf("engine_version mismatch: binary=%s manifest=%s", curEngine, manifest.EngineVersion))
	}
	curCRS := EmbeddedCRSVersion()
	if manifest.CRSEnabled && manifest.EmbeddedCRSVersion != "" && curCRS != manifest.EmbeddedCRSVersion {
		res.CRSMatch = false
		res.Mismatches = append(res.Mismatches, fmt.Sprintf("embedded_crs_version mismatch: binary=%s manifest=%s", curCRS, manifest.EmbeddedCRSVersion))
	}

	// 2. Check that every recorded external asset exists before compilation.
	missingAsset := false
	for _, wantAsset := range manifest.ExternalAssets {
		st, err := os.Stat(wantAsset.Path)
		if err != nil {
			missingAsset = true
			res.ExternalAssetsMatch = false
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("external asset %q is missing or unreadable: %v", wantAsset.Path, err))
			continue
		}
		if st.IsDir() {
			missingAsset = true
			res.ExternalAssetsMatch = false
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("external asset %q is a directory, want regular file", wantAsset.Path))
		}
	}
	if missingAsset {
		res.Status = "asset_missing"
		return res, nil
	}

	// 3. Compile the candidate policy through the exact-read recorder.
	curManifest, fw, err := BuildPolicyManifest(ctx, manifest.Scope, cfg, versions)
	if fw != nil {
		defer func() { _ = fw.Close() }()
	}
	if err != nil {
		res.PolicyMatch = false
		res.ExternalAssetsMatch = false
		res.Status = "compile_failed"
		res.Mismatches = append(res.Mismatches, fmt.Sprintf("compilation failed: %v", err))
		return res, nil
	}

	// 4. Compare per-file external assets and aggregate raw external digest.
	if curManifest.RawExternalSHA256 != manifest.RawExternalSHA256 || len(curManifest.ExternalAssets) != len(manifest.ExternalAssets) {
		res.ExternalAssetsMatch = false
		wantByPath := make(map[string]ExternalAssetRecord, len(manifest.ExternalAssets))
		for _, a := range manifest.ExternalAssets {
			wantByPath[a.Path] = a
		}
		for _, got := range curManifest.ExternalAssets {
			want, ok := wantByPath[got.Path]
			if !ok {
				res.Mismatches = append(res.Mismatches, fmt.Sprintf("unexpected included external asset %q", got.Path))
				continue
			}
			if got.SHA256 != want.SHA256 || got.SizeBytes != want.SizeBytes {
				res.Mismatches = append(res.Mismatches, fmt.Sprintf("external asset %q content changed (size %d->%d, sha256 %s->%s)", got.Path, want.SizeBytes, got.SizeBytes, shortHex(want.SHA256), shortHex(got.SHA256)))
			}
			delete(wantByPath, got.Path)
		}
		for path := range wantByPath {
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("expected external asset %q was not loaded by current policy", path))
		}
		if len(res.Mismatches) == 0 {
			res.Mismatches = append(res.Mismatches, "external asset read order or aggregate digest changed")
		}
	}

	// 5. Compare complete compiled policy digest.
	if curManifest.RawPolicySHA256 != manifest.RawPolicySHA256 {
		res.PolicyMatch = false
		if res.ExternalAssetsMatch && res.EngineMatch && res.CRSMatch {
			res.Mismatches = append(res.Mismatches, fmt.Sprintf("compiled policy settings or inline_rules changed (raw_policy_sha256 %s->%s)", shortHex(manifest.RawPolicySHA256), shortHex(curManifest.RawPolicySHA256)))
		}
	}

	switch {
	case !res.EngineMatch || !res.CRSMatch:
		res.Status = "package_mismatch"
	case !res.ExternalAssetsMatch:
		res.Status = "asset_changed"
	case !res.PolicyMatch:
		res.Status = "policy_mismatch"
	default:
		res.Verified = true
		res.Status = "verified"
	}
	return res, nil
}

func shortHex(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}
