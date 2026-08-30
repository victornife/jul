// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"strings"
	"testing"
)

func TestDecodeManifestRejectsUnknownFields(t *testing.T) {
	_, err := DecodeManifest(strings.NewReader(`{"schema_version":1,"unknown":true}`))
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("error = %v, want unknown-field rejection", err)
	}
}

func TestManifestValidationRejectsUnsafeReplay(t *testing.T) {
	manifest := validManifest()
	manifest.Scenarios[0].Request.Method = "DELETE"
	manifest.Scenarios[0].Request.Headers = map[string][]string{"authorization": {"Bearer secret"}}
	manifest.Scenarios[0].Safe = false
	if err := manifest.Validate(); err == nil {
		t.Fatal("unsafe manifest unexpectedly validated")
	} else {
		for _, want := range []string{"safe must be true", "outside the safe replay allow-list", "secret-bearing"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("validation error missing %q: %v", want, err)
			}
		}
	}
}

func TestExpectedDifferenceRequiresExplicitContract(t *testing.T) {
	manifest := validManifest()
	scenario := &manifest.Scenarios[0]
	scenario.ExpectedVerdict = VerdictExpectedDifference
	scenario.ExpectedDifferenceCode = "NGX_LOCATION_ALIAS"
	scenario.Jul = &ObservationSpec{Status: 204}
	if err := manifest.Validate(); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("error = %v, want missing-difference rejection", err)
	}
	scenario.Jul.Status = 404
	if err := manifest.Validate(); err != nil {
		t.Fatalf("explicit expected difference rejected: %v", err)
	}
}

func validManifest() Manifest {
	return Manifest{
		SchemaVersion: SchemaVersion,
		ID:            "fixture-one",
		Tier:          TierCore,
		Description:   "A repository-authored deterministic fixture.",
		Origin: Origin{
			Kind:    OriginRepositoryAuthored,
			License: "AGPL-3.0-only",
			Source:  "victornife/jul#154",
		},
		Categories: []string{"core-http"},
		BuildTags:  []string{"importer"},
		Root:       "nginx/nginx.conf",
		Assessment: ExpectedAssessment{
			Status:    "ready_for_review",
			Ready:     true,
			Complete:  true,
			FilesRead: 1,
			Sources:   []string{"nginx.conf"},
			Results: []ExpectedResult{{
				Source: "nginx.conf", Code: "NGX_MAIN_HTTP", Class: "supported",
				Risk: "operational", Context: "main", Directive: "http",
			}},
		},
		Candidate: CandidateGolden{Required: true, Contains: []string{"listen = ':8080'"}},
		Scenarios: []Scenario{{
			ID:   "health",
			Safe: true,
			Request: RequestSpec{
				Method: "GET",
				Path:   "/health",
			},
			Assert:          []Dimension{DimensionStatus},
			Reference:       ObservationSpec{Status: 204},
			ExpectedVerdict: VerdictEquivalent,
		}},
	}
}
