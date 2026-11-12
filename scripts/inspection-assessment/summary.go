// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
)

// Verdicts of the frozen comparison rules.
const (
	VerdictPass         = "pass"
	VerdictRegression   = "fail-regression"
	VerdictCorrectness  = "fail-correctness"
	VerdictInsufficient = "insufficient-repetitions"
	VerdictBaseInvalid  = "inconclusive-baseline-invalid"
	VerdictInstrument   = "inconclusive-instrument-invalid"
	VerdictSaturated    = "inconclusive-saturated"
	VerdictNoise        = "inconclusive-noise"
)

// Point is one repetition's primary goodput.
type Point struct {
	Run       string   `json:"run"`
	Rep       int      `json:"rep"`
	Goodput   float64  `json:"goodput_rps"`
	Valid     bool     `json:"valid"`
	Correct   bool     `json:"correct"`
	Saturated *bool    `json:"saturated"`
	CPU       *float64 `json:"cpu_fraction"`
	PeakMem   *int64   `json:"peak_cg_memory_bytes"`
	State     string   `json:"state"`
	Reasons   []string `json:"invalid_reasons,omitempty"`
	Findings  []string `json:"runtime_findings,omitempty"`
}

// Stats summarises a set of points (all of them; never a subset).
type Stats struct {
	N       int      `json:"n"`
	Valid   int      `json:"valid"`
	Correct int      `json:"correct"`
	Median  *float64 `json:"median_goodput_rps"`
	Min     *float64 `json:"min_goodput_rps"`
	Max     *float64 `json:"max_goodput_rps"`
	Spread  *float64 `json:"relative_spread"`
}

// Describe computes median and spread over every point.
func Describe(ps []Point) Stats {
	s := Stats{N: len(ps)}
	if len(ps) == 0 {
		return s
	}
	vals := make([]float64, 0, len(ps))
	for _, p := range ps {
		vals = append(vals, p.Goodput)
		if p.Valid {
			s.Valid++
		}
		if p.Correct {
			s.Correct++
		}
	}
	slices.Sort(vals)
	med := vals[len(vals)/2]
	if len(vals)%2 == 0 {
		med = (vals[len(vals)/2-1] + vals[len(vals)/2]) / 2
	}
	lo, hi := vals[0], vals[len(vals)-1]
	s.Median, s.Min, s.Max = ptr(round3(med)), ptr(lo), ptr(hi)
	if med > 0 {
		s.Spread = ptr(round3((hi - lo) / med))
	}
	return s
}

func ptr[T any](v T) *T { return &v }

// Compare applies the frozen rules to a baseline and candidate cell.
func Compare(base, cand []Point, rules Interpretation) (string, *float64) {
	if len(base) < rules.MinRepetitions || len(cand) < rules.MinRepetitions {
		return VerdictInsufficient, nil
	}
	for _, p := range append(slicesClone(base), cand...) {
		if !p.Valid {
			return VerdictInstrument, nil
		}
	}
	for _, p := range cand {
		if !p.Correct {
			return VerdictCorrectness, nil
		}
	}
	for _, p := range base {
		if !p.Correct {
			return VerdictBaseInvalid, nil
		}
	}
	for _, p := range append(slicesClone(base), cand...) {
		if p.Saturated == nil || *p.Saturated {
			return VerdictSaturated, nil
		}
	}
	b, c := Describe(base), Describe(cand)
	if *b.Median <= 0 {
		return VerdictBaseInvalid, nil
	}
	reg := round3(1 - *c.Median / *b.Median)
	if reg > rules.MaxRegression {
		return VerdictRegression, &reg
	}
	if (b.Spread != nil && *b.Spread > rules.MaxSpread) || (c.Spread == nil || *c.Spread > rules.MaxSpread) {
		return VerdictNoise, &reg
	}
	return VerdictPass, &reg
}

// LoadedRun is a verified run directory.
type LoadedRun struct {
	Dir      string
	Manifest Manifest
	Results  []RunResult
}

// LoadRuns verifies and loads run directories, rejecting combinations whose
// plan, scenario semantics, adapters, binaries or envelope differ.
func LoadRuns(dirs []string) ([]LoadedRun, error) {
	var out []LoadedRun
	targets := map[string]TargetIdentity{}
	scen := map[string]string{}
	var first *Manifest
	for _, d := range dirs {
		if err := VerifyRun(d); err != nil {
			return nil, fmt.Errorf("%s: %w", d, err)
		}
		m, _ := ReadManifest(d)
		if first == nil {
			first = &m
		}
		switch {
		case m.PlanVersion != first.PlanVersion || m.RulesSHA != first.RulesSHA:
			return nil, fmt.Errorf("%s: plan %s/%s is incompatible with %s/%s", d, m.PlanVersion, m.RulesSHA, first.PlanVersion, first.RulesSHA)
		case m.Profile.Name != first.Profile.Name:
			return nil, fmt.Errorf("%s: profile %s is incompatible with %s", d, m.Profile.Name, first.Profile.Name)
		case m.Envelope != first.Envelope:
			return nil, fmt.Errorf("%s: envelope %+v is incompatible with %+v", d, m.Envelope, first.Envelope)
		case m.Fault != "" || m.KillJulAfter != "":
			return nil, fmt.Errorf("%s: controlled-fault runs are runner validation, not measurement", d)
		}
		if m.Guest != nil && first.Guest != nil && m.Guest.SHA256 != first.Guest.SHA256 {
			return nil, fmt.Errorf("%s: guest %s differs from %s", d, m.Guest.SHA256, first.Guest.SHA256)
		}
		for _, t := range m.Targets {
			if prev, ok := targets[t.Label]; ok && (prev.Binary.SHA256 != t.Binary.SHA256 || prev.Adapter != t.Adapter) {
				return nil, fmt.Errorf("%s: target %s binary/adapter differs between runs", d, t.Label)
			}
			targets[t.Label] = t
		}
		for k, v := range m.ScenarioSHAs {
			if prev, ok := scen[k]; ok && prev != v {
				return nil, fmt.Errorf("%s: scenario %s definition differs between runs", d, k)
			}
			scen[k] = v
		}
		lr := LoadedRun{Dir: d, Manifest: m}
		for _, rel := range m.Results {
			var r RunResult
			if err := readJSON(filepath.Join(d, rel), &r); err != nil {
				return nil, err
			}
			lr.Results = append(lr.Results, r)
		}
		out = append(out, lr)
	}
	return out, nil
}

// PointOf extracts a repetition's primary-class goodput.
func PointOf(run string, r RunResult) Point {
	p := Point{Run: run, Rep: r.Rep, Valid: r.Valid, Correct: r.Correct, State: r.State, PeakMem: r.PeakCgMem, Reasons: r.Invalid, Findings: r.Findings}
	if r.Failure != "" {
		p.Reasons = append([]string{r.Failure}, p.Reasons...)
	}
	if len(r.Phases) > 0 && len(r.Phases[0].Classes) > 0 {
		p.Goodput = r.Phases[0].Classes[0].GoodputRPS
		p.Saturated, p.CPU = r.Phases[0].Saturated, r.Phases[0].CPUFraction
	}
	return p
}

// CellSummary is one scenario × variant × target.
type CellSummary struct {
	Scenario   string  `json:"scenario"`
	Variant    string  `json:"variant"`
	Target     string  `json:"target"`
	Comparison string  `json:"comparison"`
	Stats      Stats   `json:"stats"`
	Points     []Point `json:"points"`
}

// ComparisonSummary is one baseline/candidate verdict.
type ComparisonSummary struct {
	Scenario   string   `json:"scenario"`
	Variant    string   `json:"variant"`
	Baseline   string   `json:"baseline"`
	Candidate  string   `json:"candidate"`
	Verdict    string   `json:"verdict"`
	Regression *float64 `json:"median_regression"`
}

// ClassRow is one workload class of one phase of one repetition. Cells
// summarise only the primary class, so probes, slow and warm classes and
// later phases are reported here.
type ClassRow struct {
	Scenario    string   `json:"scenario"`
	Variant     string   `json:"variant"`
	Target      string   `json:"target"`
	Run         string   `json:"run"`
	Rep         int      `json:"rep"`
	Phase       string   `json:"phase"`
	Class       string   `json:"class"`
	Proto       string   `json:"proto"`
	Concurrency int      `json:"concurrency"`
	Attempts    int      `json:"attempts"`
	Good        int      `json:"verified_in_window"`
	Goodput     float64  `json:"goodput_rps"`
	CrossedIn   int      `json:"crossed_in_from_warmup"`
	Outstanding int      `json:"outstanding_at_window_end"`
	P99ms       *float64 `json:"p99_ms"`
	Outcomes    string   `json:"outcomes"`
}

// ProbeRow is one F4 probe of one repetition.
type ProbeRow struct {
	Scenario string `json:"scenario"`
	Variant  string `json:"variant"`
	Target   string `json:"target"`
	Run      string `json:"run"`
	Rep      int    `json:"rep"`
	F4Result
}

// Summary is the deterministic report.
type Summary struct {
	PlanVersion string              `json:"plan_version"`
	Profile     string              `json:"profile"`
	Rules       Interpretation      `json:"interpretation"`
	Runs        []string            `json:"runs"`
	Targets     []TargetIdentity    `json:"targets"`
	Cells       []CellSummary       `json:"cells"`
	Comparisons []ComparisonSummary `json:"comparisons"`
	Classes     []ClassRow          `json:"classes"`
	Probes      []ProbeRow          `json:"f4_probes,omitempty"`
	Failures    []string            `json:"failed_or_invalid_runs"`
	Boundary    string              `json:"acceptance_boundary"`
}

func outcomes(m map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s ×%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// BuildSummary builds the report; pairs are baseline:candidate labels.
func BuildSummary(runs []LoadedRun, pairs [][2]string) Summary {
	s := Summary{Rules: Rules, Boundary: Rules.AcceptanceBoundary}
	cells := map[string]*CellSummary{}
	seenT := map[string]bool{}
	for _, lr := range runs {
		s.PlanVersion, s.Profile = lr.Manifest.PlanVersion, lr.Manifest.Profile.Name
		s.Runs = append(s.Runs, lr.Manifest.RunID)
		for _, t := range lr.Manifest.Targets {
			if !seenT[t.Label] {
				seenT[t.Label] = true
				s.Targets = append(s.Targets, t)
			}
		}
		for _, r := range lr.Results {
			key := r.Scenario + "\x00" + r.Variant + "\x00" + r.Target
			c, ok := cells[key]
			if !ok {
				c = &CellSummary{Scenario: r.Scenario, Variant: r.Variant, Target: r.Target, Comparison: r.Comparison}
				cells[key] = c
			}
			c.Points = append(c.Points, PointOf(lr.Manifest.RunID, r))
			for _, ph := range r.Phases {
				for _, cs := range ph.Classes {
					s.Classes = append(s.Classes, ClassRow{Scenario: r.Scenario, Variant: r.Variant, Target: r.Target, Run: lr.Manifest.RunID, Rep: r.Rep,
						Phase: ph.Name, Class: cs.Workload, Proto: cs.Proto, Concurrency: cs.Concurrency, Attempts: cs.Attempts, Good: cs.GoodN,
						Goodput: cs.GoodputRPS, CrossedIn: cs.CrossedIn, Outstanding: cs.Outstanding, P99ms: cs.P99ms, Outcomes: outcomes(cs.Classes)})
				}
			}
			for _, f := range r.F4 {
				s.Probes = append(s.Probes, ProbeRow{Scenario: r.Scenario, Variant: r.Variant, Target: r.Target, Run: lr.Manifest.RunID, Rep: r.Rep, F4Result: f})
			}
			id := fmt.Sprintf("%s %s/%s/%s rep%d", lr.Manifest.RunID, r.Scenario, r.Variant, r.Target, r.Rep)
			if !r.Valid {
				s.Failures = append(s.Failures, id+": instrument-invalid: "+strings.Join(PointOf("", r).Reasons, "; "))
			}
			if !r.Correct {
				s.Failures = append(s.Failures, id+": runtime-finding: "+strings.Join(r.Findings, "; "))
			}
		}
	}
	slices.Sort(s.Runs)
	slices.SortFunc(s.Targets, func(a, b TargetIdentity) int { return strings.Compare(a.Label, b.Label) })
	slices.Sort(s.Failures)
	cellOrder := func(sc, v, t, run string, rep int) string {
		return fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%09d", sc, v, t, run, rep)
	}
	slices.SortStableFunc(s.Classes, func(a, b ClassRow) int {
		return strings.Compare(cellOrder(a.Scenario, a.Variant, a.Target, a.Run, a.Rep), cellOrder(b.Scenario, b.Variant, b.Target, b.Run, b.Rep))
	})
	slices.SortStableFunc(s.Probes, func(a, b ProbeRow) int {
		return strings.Compare(cellOrder(a.Scenario, a.Variant, a.Target, a.Run, a.Rep), cellOrder(b.Scenario, b.Variant, b.Target, b.Run, b.Rep))
	})
	for _, k := range sortedKeys(cells) {
		c := cells[k]
		slices.SortFunc(c.Points, func(a, b Point) int {
			if x := strings.Compare(a.Run, b.Run); x != 0 {
				return x
			}
			return a.Rep - b.Rep
		})
		c.Stats = Describe(c.Points)
		s.Cells = append(s.Cells, *c)
	}
	for _, pr := range pairs {
		for _, c := range s.Cells {
			if c.Target != pr[0] || c.Comparison != "throughput" {
				continue
			}
			cand, ok := cells[c.Scenario+"\x00"+c.Variant+"\x00"+pr[1]]
			if !ok {
				continue
			}
			v, reg := Compare(c.Points, cand.Points, Rules)
			s.Comparisons = append(s.Comparisons, ComparisonSummary{Scenario: c.Scenario, Variant: c.Variant, Baseline: pr[0], Candidate: pr[1], Verdict: v, Regression: reg})
		}
	}
	return s
}

func fnum(p *float64, prec int) string {
	if p == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.*f", prec, *p)
}

func fmib(p *int64) string {
	if p == nil {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f", float64(*p)/(1<<20))
}

// Markdown renders the summary deterministically.
func (s Summary) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Inspection assessment summary\n\nPlan `%s`, profile `%s`. Runs: %s.\n\n", s.PlanVersion, s.Profile, strings.Join(s.Runs, ", "))
	fmt.Fprintf(&b, "> %s\n\n", s.Boundary)
	b.WriteString("## Targets\n\n| Label | Adapter | SHA-256 | Go | Revision | Modified |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, t := range s.Targets {
		fmt.Fprintf(&b, "| %s | %s | `%s` | %s | `%s` | %s |\n", t.Label, t.Adapter.Name, t.Binary.SHA256, t.Binary.GoVersion,
			t.Binary.Settings["vcs.revision"], t.Binary.Settings["vcs.modified"])
	}
	b.WriteString("\n## Cells\n\nGoodput counts verified responses only. Spread is (max − min) / median over every repetition.\n\n")
	b.WriteString("| Scenario | Variant | Target | Rule | n | instrument-valid | correct | median rps | min | max | spread | CPU fractions | peak cgroup MiB |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- | --- |\n")
	for _, c := range s.Cells {
		var cpus, mems []string
		for _, p := range c.Points {
			cpus = append(cpus, fnum(p.CPU, 2))
			mems = append(mems, fmib(p.PeakMem))
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %d | %d | %s | %s | %s | %s | %s | %s |\n", c.Scenario, c.Variant, c.Target, c.Comparison,
			c.Stats.N, c.Stats.Valid, c.Stats.Correct, fnum(c.Stats.Median, 1), fnum(c.Stats.Min, 1), fnum(c.Stats.Max, 1), fnum(c.Stats.Spread, 3),
			strings.Join(cpus, " "), strings.Join(mems, " "))
	}
	if len(s.Comparisons) > 0 {
		b.WriteString("\n## Comparisons (informational; not A13 acceptance)\n\n| Scenario | Variant | Baseline | Candidate | Median regression | Verdict |\n| --- | --- | --- | --- | ---: | --- |\n")
		for _, c := range s.Comparisons {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", c.Scenario, c.Variant, c.Baseline, c.Candidate, fnum(c.Regression, 3), c.Verdict)
		}
	}
	b.WriteString("\n## Phases and classes\n\nOne row per repetition, phase and workload class. Goodput counts verified responses that started and finished in the window; requests crossing in from warm-up and those outstanding at the window end are excluded from it.\n\n")
	b.WriteString("| Scenario | Variant | Target | Run | Rep | Phase | Class | Proto | C | Attempts | Verified in window | Goodput rps | Crossed in | Outstanding | p99 ms | Outcomes |\n")
	b.WriteString("| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |\n")
	for _, c := range s.Classes {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %s | %s | %d | %d | %d | %.1f | %d | %d | %s | %s |\n", c.Scenario, c.Variant, c.Target, c.Run, c.Rep,
			c.Phase, c.Class, c.Proto, c.Concurrency, c.Attempts, c.Good, c.Goodput, c.CrossedIn, c.Outstanding, fnum(c.P99ms, 1), c.Outcomes)
	}
	if len(s.Probes) > 0 {
		b.WriteString("\n## F4 probes\n\n| Scenario | Variant | Target | Run | Rep | Proto | Mode | Upload | Upload s | Client watchdog ended it | Sibling | Sibling ms | Same connection | Dials/expected |\n")
		b.WriteString("| --- | --- | --- | --- | ---: | --- | --- | --- | ---: | --- | --- | ---: | --- | --- |\n")
		for _, p := range s.Probes {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %d | %s | %s | %s | %.1f | %t | %s | %.0f | %t | %d/%d |\n", p.Scenario, p.Variant, p.Target, p.Run, p.Rep,
				p.Proto, p.Mode, p.UploadClass, p.UploadSeconds, p.WatchdogFired, p.SiblingClass, p.SiblingMs, p.SameConnection, p.Dials, p.WantDials)
		}
	}
	b.WriteString("\n## Failed, invalid or inconclusive runs\n\n")
	if len(s.Failures) == 0 {
		b.WriteString("None.\n")
	}
	for _, f := range s.Failures {
		fmt.Fprintf(&b, "- %s\n", f)
	}
	return b.String()
}

// JSON renders the summary deterministically.
func (s Summary) JSON() []byte {
	b, _ := json.MarshalIndent(s, "", "  ")
	return append(b, '\n')
}

// Calibration is the chosen unsaturated concurrency per protocol/variant.
type Calibration struct {
	Scenario string            `json:"scenario"`
	Variant  string            `json:"variant"`
	Target   string            `json:"target"`
	Chosen   int               `json:"chosen_concurrency"`
	Steps    []CalibrationStep `json:"steps"`
}

// CalibrationStep is one concurrency step.
type CalibrationStep struct {
	Concurrency int      `json:"concurrency"`
	CPU         *float64 `json:"cpu_fraction"`
	Verified    bool     `json:"all_verified"`
	Goodput     float64  `json:"goodput_rps"`
}

// Calibrate applies the frozen calibration rule to calibration results.
func Calibrate(runs []LoadedRun, target string) []Calibration {
	var out []Calibration
	for _, lr := range runs {
		for _, r := range lr.Results {
			if r.Target != target || !strings.HasPrefix(r.Scenario, "cal-") {
				continue
			}
			c := Calibration{Scenario: r.Scenario, Variant: r.Variant, Target: r.Target}
			for _, ph := range r.Phases {
				if len(ph.Classes) == 0 {
					continue
				}
				cs := ph.Classes[0]
				in, fi := cs.Check()
				st := CalibrationStep{Concurrency: cs.Concurrency, CPU: ph.CPUFraction, Verified: len(in) == 0 && len(fi) == 0, Goodput: cs.GoodputRPS}
				c.Steps = append(c.Steps, st)
				if r.State == "completed" && st.Verified && st.CPU != nil && *st.CPU <= Rules.CalibrationCPU && st.Concurrency > c.Chosen {
					c.Chosen = st.Concurrency
				}
			}
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Calibration) int { return strings.Compare(a.Scenario+a.Variant, b.Scenario+b.Variant) })
	return out
}
