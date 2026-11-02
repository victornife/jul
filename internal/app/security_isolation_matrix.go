// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// SecurityIsolationMatrixVersion is the supported schema version for
// docs/security-isolation-matrix.json (#634).
const SecurityIsolationMatrixVersion = 1

var validIsolationProperties = []string{
	"public_private_isolation",
	"trusted_identity",
	"cache_isolation",
	"policy_lifecycle",
	"protocol_applicability",
}

var validIsolationBuildProfiles = []string{
	"lean",
	"full",
	"lean_and_full",
}

var validIsolationResultStatuses = []string{
	"pass",
	"fail",
	"skipped",
	"unavailable",
	"unsupported",
}

// SecurityIsolationManifest is the versioned cross-route and protocol security
// regression matrix declaration (#634).
type SecurityIsolationManifest struct {
	SchemaVersion        int                     `json:"schema_version"`
	MatrixID             string                  `json:"matrix_id"`
	Owner                string                  `json:"owner"`
	RevalidationTriggers []string                `json:"revalidation_triggers"`
	Cases                []SecurityIsolationCase `json:"cases"`
}

// SecurityIsolationCase declares one bounded isolation property and its owning
// test in the regression matrix (#634).
type SecurityIsolationCase struct {
	ID               string   `json:"id"`
	Property         string   `json:"property"`
	Scenario         string   `json:"scenario"`
	RouteScope       string   `json:"route_scope"`
	Protocols        []string `json:"protocols"`
	BuildProfile     string   `json:"build_profile"`
	ExpectedBoundary string   `json:"expected_boundary"`
	OwnerTest        string   `json:"owner_test"`
	Required         bool     `json:"required"`
	Limitations      string   `json:"limitations"`
}

// SecurityIsolationCaseResult records the observed outcome of one matrix case
// against a specific source SHA and build profile (#634).
type SecurityIsolationCaseResult struct {
	CaseID           string `json:"case_id"`
	Property         string `json:"property"`
	Status           string `json:"status"`
	ObservedBoundary string `json:"observed_boundary"`
	OriginHits       int64  `json:"origin_hits"`
	Notes            string `json:"notes,omitempty"`
}

// SecurityIsolationReport aggregates the execution results of a
// SecurityIsolationManifest for a specific source SHA and build profile (#634).
type SecurityIsolationReport struct {
	SchemaVersion int                           `json:"schema_version"`
	MatrixID      string                        `json:"matrix_id"`
	SourceSHA     string                        `json:"source_sha"`
	BuildProfile  string                        `json:"build_profile"`
	WAFCompiled   bool                          `json:"waf_compiled"`
	Results       []SecurityIsolationCaseResult `json:"results"`
}

// LoadSecurityIsolationManifest reads and strictly validates a security
// isolation matrix manifest file (#634). Unknown JSON fields are rejected.
func LoadSecurityIsolationManifest(path string) (*SecurityIsolationManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("security isolation manifest: read %s: %w", path, err)
	}
	return ParseSecurityIsolationManifest(data)
}

// ParseSecurityIsolationManifest decodes and validates a manifest from raw JSON
// bytes, rejecting unknown fields or trailing data (#634).
func ParseSecurityIsolationManifest(data []byte) (*SecurityIsolationManifest, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m SecurityIsolationManifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("security isolation manifest: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("security isolation manifest: trailing data after JSON object")
	}
	if err := ValidateSecurityIsolationManifest(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// ValidateSecurityIsolationManifest enforces completeness and invariant checks
// on the declared security isolation matrix (#634).
func ValidateSecurityIsolationManifest(m *SecurityIsolationManifest) error {
	if m == nil {
		return errors.New("security isolation manifest is nil")
	}
	if m.SchemaVersion != SecurityIsolationMatrixVersion {
		return fmt.Errorf("security isolation manifest: unsupported schema_version %d (want %d)", m.SchemaVersion, SecurityIsolationMatrixVersion)
	}
	if strings.TrimSpace(m.MatrixID) == "" {
		return errors.New("security isolation manifest: matrix_id is required")
	}
	if strings.TrimSpace(m.Owner) == "" {
		return errors.New("security isolation manifest: owner is required")
	}
	if len(m.RevalidationTriggers) == 0 {
		return errors.New("security isolation manifest: at least one revalidation_trigger is required")
	}
	if len(m.Cases) == 0 {
		return errors.New("security isolation manifest: cases must not be empty")
	}

	seenIDs := make(map[string]struct{}, len(m.Cases))
	coveredProps := make(map[string]int, len(validIsolationProperties))
	for i, c := range m.Cases {
		id := strings.TrimSpace(c.ID)
		if id == "" {
			return fmt.Errorf("security isolation manifest: cases[%d].id is empty", i)
		}
		if _, dup := seenIDs[id]; dup {
			return fmt.Errorf("security isolation manifest: duplicate case id %q", id)
		}
		seenIDs[id] = struct{}{}

		if !slices.Contains(validIsolationProperties, c.Property) {
			return fmt.Errorf("security isolation manifest: case %q has invalid property %q", id, c.Property)
		}
		if strings.TrimSpace(c.Scenario) == "" {
			return fmt.Errorf("security isolation manifest: case %q scenario is required", id)
		}
		if strings.TrimSpace(c.RouteScope) == "" {
			return fmt.Errorf("security isolation manifest: case %q route_scope is required", id)
		}
		if len(c.Protocols) == 0 {
			return fmt.Errorf("security isolation manifest: case %q must declare at least one protocol", id)
		}
		if !slices.Contains(validIsolationBuildProfiles, c.BuildProfile) {
			return fmt.Errorf("security isolation manifest: case %q has invalid build_profile %q", id, c.BuildProfile)
		}
		if strings.TrimSpace(c.ExpectedBoundary) == "" {
			return fmt.Errorf("security isolation manifest: case %q expected_boundary is required", id)
		}
		if strings.TrimSpace(c.OwnerTest) == "" || !strings.Contains(c.OwnerTest, ":Test") {
			return fmt.Errorf("security isolation manifest: case %q owner_test %q must name a package:TestFunc owner", id, c.OwnerTest)
		}
		if strings.TrimSpace(c.Limitations) == "" {
			return fmt.Errorf("security isolation manifest: case %q limitations is required", id)
		}
		coveredProps[c.Property]++
	}

	for _, prop := range validIsolationProperties {
		if coveredProps[prop] == 0 {
			return fmt.Errorf("security isolation manifest: required property %q has zero declared cases", prop)
		}
	}
	return nil
}

// VerifySecurityIsolationReport validates an executed isolation report against
// its manifest and expected source/profile identity (#634). Missing test
// discovery, stale source SHAs, malformed results, or required skips/failures
// return a non-nil error so incomplete evidence cannot silently pass.
func VerifySecurityIsolationReport(m *SecurityIsolationManifest, r *SecurityIsolationReport, expectedSourceSHA string) error {
	if err := ValidateSecurityIsolationManifest(m); err != nil {
		return err
	}
	if r == nil {
		return errors.New("security isolation report is nil")
	}
	if r.SchemaVersion != SecurityIsolationMatrixVersion {
		return fmt.Errorf("security isolation report: unsupported schema_version %d", r.SchemaVersion)
	}
	if r.MatrixID != m.MatrixID {
		return fmt.Errorf("security isolation report: matrix_id %q does not match manifest %q", r.MatrixID, m.MatrixID)
	}
	if strings.TrimSpace(r.SourceSHA) == "" {
		return errors.New("security isolation report: source_sha is required")
	}
	if expectedSourceSHA != "" && r.SourceSHA != expectedSourceSHA {
		return fmt.Errorf("security isolation report: stale source_sha %q (want %q)", r.SourceSHA, expectedSourceSHA)
	}
	if r.BuildProfile != "lean" && r.BuildProfile != "full" {
		return fmt.Errorf("security isolation report: invalid build_profile %q (want lean or full)", r.BuildProfile)
	}
	if len(r.Results) == 0 {
		return errors.New("security isolation report: no test results discovered")
	}

	byID := make(map[string]SecurityIsolationCaseResult, len(r.Results))
	for _, res := range r.Results {
		if _, dup := byID[res.CaseID]; dup {
			return fmt.Errorf("security isolation report: duplicate result for case %q", res.CaseID)
		}
		if !slices.Contains(validIsolationResultStatuses, res.Status) {
			return fmt.Errorf("security isolation report: case %q has invalid status %q", res.CaseID, res.Status)
		}
		byID[res.CaseID] = res
	}

	for _, c := range m.Cases {
		res, ok := byID[c.ID]
		if !ok {
			return fmt.Errorf("security isolation report: missing result for declared case %q", c.ID)
		}
		if res.Property != c.Property {
			return fmt.Errorf("security isolation report: case %q property mismatch %q != %q", c.ID, res.Property, c.Property)
		}
		if strings.TrimSpace(res.ObservedBoundary) == "" {
			return fmt.Errorf("security isolation report: case %q observed_boundary is empty", c.ID)
		}
		if res.ObservedBoundary != c.ExpectedBoundary {
			return fmt.Errorf("security isolation report: case %q observed_boundary %q != expected_boundary %q", c.ID, res.ObservedBoundary, c.ExpectedBoundary)
		}
		// In a lean build, cases requiring the full profile may report
		// "unavailable" honestly, but only when r.BuildProfile == "lean".
		if c.BuildProfile == "full" && r.BuildProfile == "lean" && res.Status == "unavailable" {
			continue
		}
		if c.Required && res.Status != "pass" {
			return fmt.Errorf("security isolation report: required case %q did not pass (status=%s)", c.ID, res.Status)
		}
	}
	return nil
}
