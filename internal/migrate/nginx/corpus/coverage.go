// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package corpus

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	// CoverageSchemaVersion is the machine-readable category-coverage contract.
	CoverageSchemaVersion = 1
	coverageName          = "coverage.json"
	inventoryName         = "inventory.json"
)

var requiredCoverageCategories = []string{
	"cache-compression",
	"core-http-routing",
	"operations",
	"protocol-gateways",
	"security",
	"upstreams-resiliency",
}

// CoverageStatus records whether a minimum issue category is represented by
// executable fixtures or explicitly deferred in full.
type CoverageStatus string

const (
	CoverageRepresented CoverageStatus = "represented"
	CoverageDeferred    CoverageStatus = "deferred"
)

// CoverageMatrix is the reviewed closure contract for issue #154. It prevents
// prose-only category claims from drifting away from the executable corpus.
type CoverageMatrix struct {
	SchemaVersion int                `json:"schema_version"`
	Categories    []CoverageCategory `json:"categories"`
}

// CoverageCategory ties one minimum issue category to concrete fixture IDs and
// records any intentionally deferred dimensions without turning them into a
// compatibility claim.
type CoverageCategory struct {
	ID                 string         `json:"id"`
	Status             CoverageStatus `json:"status"`
	Fixtures           []string       `json:"fixtures,omitempty"`
	Evidence           []string       `json:"evidence,omitempty"`
	Rationale          string         `json:"rationale"`
	DeferredDimensions []string       `json:"deferred_dimensions,omitempty"`
	RevisitTrigger     string         `json:"revisit_trigger,omitempty"`
}

// NamedCount is one deterministic aggregate inventory entry.
type NamedCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// InventoryCoverage is the compact coverage projection embedded in the
// aggregate report. Full rationale remains in coverage.json.
type InventoryCoverage struct {
	ID                 string         `json:"id"`
	Status             CoverageStatus `json:"status"`
	Fixtures           []string       `json:"fixtures,omitempty"`
	DeferredDimensions []string       `json:"deferred_dimensions,omitempty"`
}

// Inventory is a deterministic, non-scoring summary of corpus evidence. Counts
// are facts about the checked-in fixtures, never a universal compatibility
// percentage.
type Inventory struct {
	SchemaVersion        int                 `json:"schema_version"`
	FixtureSchemaVersion int                 `json:"fixture_schema_version"`
	FixtureCount         int                 `json:"fixture_count"`
	CoreFixtures         int                 `json:"core_fixtures"`
	FullFixtures         int                 `json:"full_fixtures"`
	Categories           []NamedCount        `json:"categories"`
	Classes              []NamedCount        `json:"classes"`
	Risks                []NamedCount        `json:"risks"`
	Codes                []NamedCount        `json:"codes"`
	Verdicts             []NamedCount        `json:"verdicts"`
	Coverage             []InventoryCoverage `json:"coverage"`
}

// DecodeCoverage decodes exactly one strict coverage matrix.
func DecodeCoverage(r io.Reader) (CoverageMatrix, error) {
	var matrix CoverageMatrix
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&matrix); err != nil {
		return CoverageMatrix{}, fmt.Errorf("decode corpus coverage: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return CoverageMatrix{}, fmt.Errorf("decode corpus coverage: multiple JSON values")
		}
		return CoverageMatrix{}, fmt.Errorf("decode corpus coverage: %w", err)
	}
	return matrix, nil
}

// LoadCoverage reads the corpus-level category closure contract.
func LoadCoverage(root string) (CoverageMatrix, error) {
	path := filepath.Join(root, coverageName)
	file, err := os.Open(path)
	if err != nil {
		return CoverageMatrix{}, fmt.Errorf("open %s: %w", path, err)
	}
	matrix, decodeErr := DecodeCoverage(file)
	closeErr := file.Close()
	if decodeErr != nil {
		return CoverageMatrix{}, decodeErr
	}
	if closeErr != nil {
		return CoverageMatrix{}, fmt.Errorf("close %s: %w", path, closeErr)
	}
	return matrix, nil
}

// Validate proves that every minimum category is present exactly once and that
// each referenced fixture exists in the executable corpus.
func (m CoverageMatrix) Validate(fixtures []Fixture) error {
	var errs []error
	if m.SchemaVersion != CoverageSchemaVersion {
		errs = append(errs, fmt.Errorf("schema_version must be %d", CoverageSchemaVersion))
	}
	if !sort.SliceIsSorted(m.Categories, func(i, j int) bool { return m.Categories[i].ID < m.Categories[j].ID }) {
		errs = append(errs, fmt.Errorf("categories must be sorted by id"))
	}

	fixtureIDs := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		fixtureIDs[fixture.Manifest.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(m.Categories))
	for i, category := range m.Categories {
		prefix := fmt.Sprintf("categories[%d]", i)
		if !fixtureIDPattern.MatchString(category.ID) {
			errs = append(errs, fmt.Errorf("%s.id must match %s", prefix, fixtureIDPattern.String()))
		}
		if _, exists := seen[category.ID]; exists {
			errs = append(errs, fmt.Errorf("%s.id %q is duplicated", prefix, category.ID))
		}
		seen[category.ID] = struct{}{}
		if err := validateCoverageCategory(prefix, category, fixtureIDs); err != nil {
			errs = append(errs, err)
		}
	}
	for _, required := range requiredCoverageCategories {
		if _, ok := seen[required]; !ok {
			errs = append(errs, fmt.Errorf("required category %q is missing", required))
		}
	}
	for id := range seen {
		if !containsString(requiredCoverageCategories, id) {
			errs = append(errs, fmt.Errorf("unknown minimum category %q", id))
		}
	}
	return errors.Join(errs...)
}

func validateCoverageCategory(prefix string, category CoverageCategory, fixtureIDs map[string]struct{}) error {
	var errs []error
	switch category.Status {
	case CoverageRepresented:
		if len(category.Fixtures) == 0 && len(category.Evidence) == 0 {
			errs = append(errs, fmt.Errorf("%s represented category needs fixtures or evidence", prefix))
		}
	case CoverageDeferred:
		if len(category.Fixtures) != 0 {
			errs = append(errs, fmt.Errorf("%s deferred category must not claim fixture coverage", prefix))
		}
		if len(category.DeferredDimensions) == 0 {
			errs = append(errs, fmt.Errorf("%s deferred category needs deferred_dimensions", prefix))
		}
	default:
		errs = append(errs, fmt.Errorf("%s.status must be %q or %q", prefix, CoverageRepresented, CoverageDeferred))
	}
	if err := validateBoundedText(prefix+".rationale", category.Rationale, 800, false); err != nil {
		errs = append(errs, err)
	}
	if err := validateSortedUniqueTokens(prefix+".fixtures", category.Fixtures, false); err != nil {
		errs = append(errs, err)
	}
	if err := validateSortedUniqueTokens(prefix+".evidence", category.Evidence, false); err != nil {
		errs = append(errs, err)
	}
	if err := validateSortedUniqueTokens(prefix+".deferred_dimensions", category.DeferredDimensions, false); err != nil {
		errs = append(errs, err)
	}
	for _, id := range category.Fixtures {
		if _, ok := fixtureIDs[id]; !ok {
			errs = append(errs, fmt.Errorf("%s.fixtures references unknown fixture %q", prefix, id))
		}
	}
	if len(category.DeferredDimensions) > 0 {
		if err := validateBoundedText(prefix+".revisit_trigger", category.RevisitTrigger, 500, false); err != nil {
			errs = append(errs, err)
		}
	} else if category.RevisitTrigger != "" {
		errs = append(errs, fmt.Errorf("%s.revisit_trigger requires deferred_dimensions", prefix))
	}
	return errors.Join(errs...)
}

// BuildInventory aggregates only stable manifest fields and the reviewed
// category matrix. It does not inspect source values or invent a score.
func BuildInventory(fixtures []Fixture, matrix CoverageMatrix) Inventory {
	inventory := Inventory{
		SchemaVersion:        1,
		FixtureSchemaVersion: SchemaVersion,
		FixtureCount:         len(fixtures),
	}
	categories := map[string]int{}
	classes := map[string]int{}
	risks := map[string]int{}
	codes := map[string]int{}
	verdicts := map[string]int{}

	for _, fixture := range fixtures {
		switch fixture.Manifest.Tier {
		case TierCore:
			inventory.CoreFixtures++
		case TierFull:
			inventory.FullFixtures++
		}
		for _, category := range fixture.Manifest.Categories {
			categories[category]++
		}
		for _, result := range fixture.Manifest.Assessment.Results {
			count := result.Count
			if count == 0 {
				count = 1
			}
			classes[result.Class] += count
			risks[result.Risk] += count
			codes[result.Code] += count
		}
		for _, scenario := range fixture.Manifest.Scenarios {
			verdicts[string(scenario.ExpectedVerdict)]++
		}
	}
	inventory.Categories = namedCounts(categories)
	inventory.Classes = namedCounts(classes)
	inventory.Risks = namedCounts(risks)
	inventory.Codes = namedCounts(codes)
	inventory.Verdicts = namedCounts(verdicts)
	for _, category := range matrix.Categories {
		inventory.Coverage = append(inventory.Coverage, InventoryCoverage{
			ID:                 category.ID,
			Status:             category.Status,
			Fixtures:           append([]string(nil), category.Fixtures...),
			DeferredDimensions: append([]string(nil), category.DeferredDimensions...),
		})
	}
	return inventory
}

// JSON returns deterministic indented inventory JSON with a trailing newline.
func (i Inventory) JSON() ([]byte, error) {
	out, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// InventoryPath returns the committed aggregate report path for a corpus root.
func InventoryPath(root string) string {
	return filepath.Join(root, inventoryName)
}

func namedCounts(values map[string]int) []NamedCount {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]NamedCount, 0, len(names))
	for _, name := range names {
		out = append(out, NamedCount{Name: name, Count: values[name]})
	}
	return out
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}
