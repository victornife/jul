// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package configcontract

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// ValueContract is the parsed docs/config-value-contract.json document: the
// audited source of enums, grammars, and numeric/duration/size dispositions
// (ADR 0019 §19/§21). It is consumed, never duplicated: this package resolves
// its paths against config.SchemaPaths() and renders its constraints where a
// sound mechanical translation exists, and exposes the rest as documentation.
type ValueContract struct {
	Version int
	Fields  []ValueContractField
}

// ValueContractField is one audited entry. RawPath may describe more than one
// canonical configuration path (see CanonicalPaths).
type ValueContractField struct {
	GoField       string
	RawPath       string
	Kind          string
	Constraint    string
	ZeroSemantics string
	ActiveWhen    string
	Allowed       []string
}

// wire shape of docs/config-value-contract.json.
type valueContractDoc struct {
	Version int                     `json:"version"`
	Purpose string                  `json:"purpose"`
	Fields  []valueContractFieldRaw `json:"fields"`
}

type valueContractFieldRaw struct {
	GoField       string `json:"go_field"`
	Path          string `json:"path"`
	Kind          string `json:"kind"`
	Constraint    string `json:"constraint"`
	ZeroSemantics string `json:"zero_semantics"`
	ActiveWhen    string `json:"active_when"`
	// Allowed is almost always a string enum, except kind "integer_enum"
	// (e.g. redirect_https: [0, 301, 308]), so it is decoded loosely here and
	// resolved by kind below rather than assuming one JSON type.
	Allowed []json.RawMessage `json:"allowed,omitempty"`
}

// LoadValueContract reads and parses the audited value-contract document at
// path (typically docs/config-value-contract.json).
func LoadValueContract(path string) (ValueContract, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ValueContract{}, fmt.Errorf("configcontract: read value contract: %w", err)
	}
	var doc valueContractDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return ValueContract{}, fmt.Errorf("configcontract: decode value contract: %w", err)
	}
	fields := make([]ValueContractField, 0, len(doc.Fields))
	for _, f := range doc.Fields {
		var allowed []string
		for _, raw := range f.Allowed {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil {
				allowed = append(allowed, s)
			}
			// Numeric entries (kind "integer_enum") are intentionally skipped
			// here; ParseIntegerEnum(f.Constraint) resolves that kind instead.
		}
		fields = append(fields, ValueContractField{
			GoField:       f.GoField,
			RawPath:       f.Path,
			Kind:          f.Kind,
			Constraint:    f.Constraint,
			ZeroSemantics: f.ZeroSemantics,
			ActiveWhen:    f.ActiveWhen,
			Allowed:       allowed,
		})
	}
	return ValueContract{Version: doc.Version, Fields: fields}, nil
}

// CanonicalPaths splits RawPath into the one or more canonical
// config.SchemaPaths()-shaped paths it describes. The audited document uses
// TOML-flavored notation ("servers[]", "plugins.<name>") and, for a handful of
// entries, " / " to join several canonical locations that share one audited
// constraint (e.g. a global default and its per-location override) — this is
// the one explicit adapter ADR 0019 §21 requires, kept in one place rather
// than scattered across renderers.
func (f ValueContractField) CanonicalPaths() []string {
	return canonicalizeValueContractPath(f.RawPath)
}

func canonicalizeValueContractPath(raw string) []string {
	parts := strings.Split(raw, " / ")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.ReplaceAll(p, "<name>", "*")
		p = strings.ReplaceAll(p, "[]", ".*")
		out = append(out, p)
	}
	return out
}

// NumericBound is a mechanically sound numeric range extracted from an audited
// constraint sentence. HasBound is false when no safe translation exists, in
// which case the constraint stays documentation-only rather than becoming a
// (possibly wrong) JSON Schema keyword.
type NumericBound struct {
	Min, Max *float64
	HasBound bool
}

func (b *NumericBound) setMin(v float64) { b.Min = &v; b.HasBound = true }
func (b *NumericBound) setMax(v float64) { b.Max = &v; b.HasBound = true }

func (b *NumericBound) setMinStr(s string) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		b.setMin(v)
	}
}

func (b *NumericBound) setMaxStr(s string) {
	if v, err := strconv.ParseFloat(s, 64); err == nil {
		b.setMax(v)
	}
}

var (
	reClauseSplit = regexp.MustCompile(`[;,]`)
	reRangeDotDot = regexp.MustCompile(`^(-?\d+(?:\.\d+)?)\.\.(-?\d+(?:\.\d+)?)(?: when set| effective)?$`)
	reRangeTo     = regexp.MustCompile(`^(-?\d+(?:\.\d+)?) to (-?\d+(?:\.\d+)?)$`)
	reAtLeast     = regexp.MustCompile(`^at least (\d+)(?: effective)?$`)
	reAtMost      = regexp.MustCompile(`^at most (\d+)$`)
	reDigits      = regexp.MustCompile(`\d+`)
)

// ParseNumericBound extracts a min/max range from an audited "constraint"
// sentence, for kind in {integer, ratio, http_status} only (duration/size are
// rendered as pattern-constrained strings, never numeric — see
// DurationPattern/SizePattern).
//
// Each clause (split on ";" or ",") is matched against a small closed set of
// templates, ANCHORED to the whole clause. This is deliberately conservative:
// a looser substring search on "0 or 1..4; non-zero requires CRS" (WAF
// paranoia) would find "1..4" and wrongly report minimum=1, silently
// rejecting the valid value 0. A clause that does not fully match a known
// template contributes no bound and is left as documentation, per ADR 0019
// §22.2 — an absent mechanical bound is safer than a wrong one.
func ParseNumericBound(constraint string) NumericBound {
	var out NumericBound
	for _, clause := range reClauseSplit.Split(constraint, -1) {
		clause = strings.TrimSpace(clause)
		switch clause {
		case "non-negative", "0 or greater":
			out.setMin(0)
			continue
		case "positive":
			out.setMin(1)
			continue
		}
		if m := reRangeDotDot.FindStringSubmatch(clause); m != nil {
			out.setMinStr(m[1])
			out.setMaxStr(m[2])
		} else if m := reRangeTo.FindStringSubmatch(clause); m != nil {
			out.setMinStr(m[1])
			out.setMaxStr(m[2])
		} else if m := reAtLeast.FindStringSubmatch(clause); m != nil {
			out.setMinStr(m[1])
		} else if m := reAtMost.FindStringSubmatch(clause); m != nil {
			out.setMaxStr(m[1])
		}
	}
	return out
}

// ParseIntegerEnum extracts the enumerated integer list from an
// "integer_enum" kind constraint (e.g. "0, 301, or 308"). The kind itself
// guarantees the shape, so extracting every integer token is a safe,
// single-purpose translation rather than heuristic prose parsing.
func ParseIntegerEnum(constraint string) []int64 {
	matches := reDigits.FindAllString(constraint, -1)
	out := make([]int64, 0, len(matches))
	for _, m := range matches {
		if n, err := strconv.ParseInt(m, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// DurationPattern and SizePattern constrain Jul's Duration/Size string
// encoding (internal/config.Duration/Size UnmarshalText), used as the JSON
// Schema `pattern` for every duration/size leaf. They describe lexical shape
// only; audited numeric bounds on a duration/size stay documentation (see
// ParseNumericBound's doc comment).
const (
	DurationPattern = `^-?([0-9]+(\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$`
	SizePattern     = `^[0-9]+([bBkKmMgG]|[kKmMgG][bB])?$`
)
