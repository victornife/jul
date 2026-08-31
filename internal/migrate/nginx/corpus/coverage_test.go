// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRepositoryCoverageMatrixAndInventoryGolden(t *testing.T) {
	root := repositoryCoverageRoot(t)
	fixtures, err := Discover(root)
	if err != nil {
		t.Fatalf("discover corpus: %v", err)
	}
	matrix, err := LoadCoverage(root)
	if err != nil {
		t.Fatalf("load coverage: %v", err)
	}
	if err := matrix.Validate(fixtures); err != nil {
		t.Fatalf("validate coverage: %v", err)
	}
	actual, err := BuildInventory(fixtures, matrix).JSON()
	if err != nil {
		t.Fatalf("marshal inventory: %v", err)
	}
	expected, err := os.ReadFile(InventoryPath(root))
	if err != nil {
		t.Fatalf("read inventory golden: %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("inventory golden drift; run `go run -tags importer scripts/nginx-corpus-report.go -write` and review the diff\n--- want\n%s\n--- got\n%s", expected, actual)
	}
}

func TestCoverageDecodeStrictness(t *testing.T) {
	matrix := validCoverageMatrix()
	data, err := json.Marshal(matrix)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeCoverage(bytes.NewReader(data))
	if err != nil || len(decoded.Categories) != len(requiredCoverageCategories) {
		t.Fatalf("decode coverage = %+v, %v", decoded, err)
	}
	for name, raw := range map[string]string{
		"malformed":       `{`,
		"unknown field":   `{"schema_version":1,"categories":[],"extra":true}`,
		"multiple values": string(data) + ` {}`,
		"trailing broken": string(data) + ` {`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCoverage(strings.NewReader(raw)); err == nil {
				t.Fatal("invalid coverage unexpectedly decoded")
			}
		})
	}
}

func TestCoverageValidationFailures(t *testing.T) {
	fixtures := []Fixture{{Manifest: Manifest{ID: "fixture-one"}}}
	if err := validCoverageMatrix().Validate(fixtures); err != nil {
		t.Fatalf("valid coverage rejected: %v", err)
	}

	mutations := map[string]func(*CoverageMatrix){
		"schema": func(m *CoverageMatrix) { m.SchemaVersion = 0 },
		"unsorted": func(m *CoverageMatrix) {
			m.Categories[0], m.Categories[1] = m.Categories[1], m.Categories[0]
		},
		"duplicate": func(m *CoverageMatrix) { m.Categories[1].ID = m.Categories[0].ID },
		"missing":   func(m *CoverageMatrix) { m.Categories = m.Categories[1:] },
		"unknown category": func(m *CoverageMatrix) {
			m.Categories[0].ID = "unknown-category"
		},
		"bad id": func(m *CoverageMatrix) { m.Categories[0].ID = "Bad ID" },
		"unknown status": func(m *CoverageMatrix) {
			m.Categories[0].Status = "unknown"
		},
		"represented empty": func(m *CoverageMatrix) {
			m.Categories[0].Fixtures = nil
			m.Categories[0].Evidence = nil
		},
		"deferred claims fixture": func(m *CoverageMatrix) {
			m.Categories[0].Status = CoverageDeferred
		},
		"deferred no dimensions": func(m *CoverageMatrix) {
			m.Categories[0].Status = CoverageDeferred
			m.Categories[0].Fixtures = nil
			m.Categories[0].DeferredDimensions = nil
			m.Categories[0].RevisitTrigger = ""
		},
		"empty rationale": func(m *CoverageMatrix) { m.Categories[0].Rationale = "" },
		"unsorted fixtures": func(m *CoverageMatrix) {
			m.Categories[0].Fixtures = []string{"z", "a"}
		},
		"duplicate evidence": func(m *CoverageMatrix) {
			m.Categories[0].Evidence = []string{"same", "same"}
		},
		"unsorted deferred": func(m *CoverageMatrix) {
			m.Categories[0].DeferredDimensions = []string{"z", "a"}
		},
		"unknown fixture": func(m *CoverageMatrix) {
			m.Categories[0].Fixtures = []string{"missing-fixture"}
		},
		"missing revisit trigger": func(m *CoverageMatrix) {
			m.Categories[0].RevisitTrigger = ""
		},
		"orphan revisit trigger": func(m *CoverageMatrix) {
			m.Categories[0].DeferredDimensions = nil
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			matrix := validCoverageMatrix()
			mutate(&matrix)
			if err := matrix.Validate(fixtures); err == nil {
				t.Fatal("invalid coverage unexpectedly validated")
			}
		})
	}
}

func TestLoadCoverageMissingFile(t *testing.T) {
	if _, err := LoadCoverage(t.TempDir()); err == nil {
		t.Fatal("missing coverage file unexpectedly loaded")
	}
}

func TestInventoryCountsAndDeterminism(t *testing.T) {
	fixtures := []Fixture{
		{Manifest: Manifest{
			ID:         "a",
			Tier:       TierCore,
			Categories: []string{"core-http"},
			Assessment: ExpectedAssessment{Results: []ExpectedResult{
				{Code: "A", Class: "supported", Risk: "routing"},
				{Code: "B", Class: "blocking", Risk: "security", Count: 2},
			}},
			Scenarios: []Scenario{{ExpectedVerdict: VerdictEquivalent}},
		}},
		{Manifest: Manifest{
			ID:         "b",
			Tier:       TierFull,
			Categories: []string{"core-http", "security"},
			Assessment: ExpectedAssessment{Results: []ExpectedResult{
				{Code: "A", Class: "supported", Risk: "routing"},
			}},
			Scenarios: []Scenario{{ExpectedVerdict: VerdictExpectedDifference}},
		}},
	}
	matrix := validCoverageMatrix()
	first := BuildInventory(fixtures, matrix)
	second := BuildInventory(fixtures, matrix)
	firstJSON, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("inventory is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	if first.FixtureCount != 2 || first.CoreFixtures != 1 || first.FullFixtures != 1 {
		t.Fatalf("fixture counts = %+v", first)
	}
	if got := countNamed(first.Classes, "blocking"); got != 2 {
		t.Fatalf("blocking count = %d, want 2", got)
	}
	if got := countNamed(first.Codes, "A"); got != 2 {
		t.Fatalf("code A count = %d, want 2", got)
	}
	if got := countNamed(first.Verdicts, string(VerdictExpectedDifference)); got != 1 {
		t.Fatalf("expected-difference count = %d, want 1", got)
	}
	if path := InventoryPath("root"); path != filepath.Join("root", inventoryName) {
		t.Fatalf("inventory path = %q", path)
	}
}

func repositoryCoverageRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", "..", "testdata", "nginx-corpus"))
}

func validCoverageMatrix() CoverageMatrix {
	matrix := CoverageMatrix{SchemaVersion: CoverageSchemaVersion}
	for _, id := range requiredCoverageCategories {
		matrix.Categories = append(matrix.Categories, CoverageCategory{
			ID:                 id,
			Status:             CoverageRepresented,
			Fixtures:           []string{"fixture-one"},
			Evidence:           []string{"reviewed evidence"},
			Rationale:          "The category is represented by a bounded synthetic fixture.",
			DeferredDimensions: []string{"future dimension"},
			RevisitTrigger:     "A selected migration use case requires it.",
		})
	}
	return matrix
}

func countNamed(values []NamedCount, name string) int {
	for _, value := range values {
		if value.Name == name {
			return value.Count
		}
	}
	return 0
}
