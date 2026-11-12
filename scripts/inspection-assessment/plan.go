// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
)

// PlanVersion identifies the scenario semantics. Change it whenever a
// workload, phase, variant set, verification rule or interpretation rule
// changes; runs with different plan identities are never merged or compared.
const PlanVersion = "inspection-assessment-plan/v1"

// Variants are the four inspection configurations preserved from #695.
var Variants = []string{"none", "waf", "wasm", "both"}

// Dur is a time.Duration that serialises as Go duration text.
type Dur time.Duration

// D returns the duration.
func (d Dur) D() time.Duration { return time.Duration(d) }

// MarshalJSON writes the duration as text, for example "5s".
func (d Dur) MarshalJSON() ([]byte, error) { return json.Marshal(time.Duration(d).String()) }

// UnmarshalJSON reads duration text.
func (d *Dur) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	*d = Dur(v)
	return err
}

// Workload is one closed-loop request class.
type Workload struct {
	Name        string `json:"name"`
	Proto       string `json:"proto"` // h1 | h2 | h3
	Method      string `json:"method"`
	Path        string `json:"path"` // includes the query
	BodyBytes   int    `json:"body_bytes,omitempty"`
	BodyShape   string `json:"body_shape,omitempty"` // wide | items | fill
	RespBytes   int    `json:"resp_bytes"`
	Concurrency int    `json:"concurrency"`
	SlowBPS     int    `json:"slow_bytes_per_sec,omitempty"`
	CancelAfter Dur    `json:"cancel_after,omitempty"`
	Timeout     Dur    `json:"timeout"`
	UniqueKey   bool   `json:"unique_key,omitempty"`
	WantEcho    bool   `json:"want_origin_echo,omitempty"`
	// Connections > 0 pins the class to that many shared H2/H3 connections
	// and makes the connection count part of the verified outcome.
	Connections int `json:"connections,omitempty"`
	// Interval paces each worker between requests (the F3 probe).
	Interval Dur `json:"interval,omitempty"`
	// Expect lists outcome classes the workload deliberately provokes (for
	// example admission 503s); they are neither goodput nor findings.
	Expect []string `json:"expected_classes,omitempty"`
}

// Phase is one warm-up plus measurement window.
type Phase struct {
	Name      string     `json:"name"`
	Warmup    Dur        `json:"warmup"`
	Measure   Dur        `json:"measure"`
	Workloads []Workload `json:"workloads"`
}

// F4 configures the stalled/slow upload and sibling-stream instrument.
type F4 struct {
	Protos       []string `json:"protos"`
	Modes        []string `json:"modes"` // stall | slow
	BodyBytes    int      `json:"body_bytes"`
	ChunkBytes   int      `json:"chunk_bytes"`
	SlowInterval Dur      `json:"slow_interval"`
	SiblingAfter Dur      `json:"sibling_after"`
	ClientLimit  Dur      `json:"client_limit"` // the external watchdog
}

// Scenario is a frozen, named assessment case.
type Scenario struct {
	ID       string   `json:"id"`
	Source   string   `json:"source"`
	Kind     string   `json:"kind"` // load | reload | f4
	Purpose  string   `json:"purpose"`
	Variants []string `json:"variants"`
	Reps     int      `json:"repetitions"`
	Cache    bool     `json:"cache,omitempty"`
	// MaxPending overrides the limited pool's max_pending_requests (F3).
	MaxPending *int `json:"max_pending,omitempty"`
	// ReadTimeout sets the listener read_timeout ("" keeps the default).
	ReadTimeout string  `json:"read_timeout,omitempty"`
	Phases      []Phase `json:"phases,omitempty"`
	Recovery    Dur     `json:"recovery"`
	F4          *F4     `json:"f4,omitempty"`
	// Comparison is the summary rule: throughput (ADR A13-style median rule),
	// capacity (saturated reference, reported but never a pass), or record
	// (fidelity evidence without a comparison verdict).
	Comparison string   `json:"comparison"`
	Changes    []string `json:"changes_from_archive,omitempty"`
	// ConcurrencyByVariant replaces the first workload's concurrency per
	// variant (the calibrated unsaturated normal profile).
	ConcurrencyByVariant map[string]int `json:"concurrency_by_variant,omitempty"`
}

// Profile scales the archived windows.
type Profile struct {
	Name               string `json:"name"`
	AcceptanceEligible bool   `json:"acceptance_eligible"`
	Warmup             Dur    `json:"warmup"`
	Measure            Dur    `json:"measure"`
	Recovery           Dur    `json:"recovery"`
	ReloadMeasure      Dur    `json:"reload_measure"`
	ReloadRecovery     Dur    `json:"reload_recovery"`
	StepWarmup         Dur    `json:"step_warmup"`
	F3Duration         Dur    `json:"f3_duration"`
	F4ClientLimit      Dur    `json:"f4_client_limit"`
	Description        string `json:"description"`
}

// Profiles are the archived qualification windows and the distinct smoke.
var Profiles = map[string]Profile{
	"qualification": {Name: "qualification", AcceptanceEligible: true,
		Warmup: Dur(5 * time.Second), Measure: Dur(20 * time.Second), Recovery: Dur(10 * time.Second),
		ReloadMeasure: Dur(40 * time.Second), ReloadRecovery: Dur(15 * time.Second), StepWarmup: Dur(2 * time.Second),
		F3Duration: Dur(20 * time.Second), F4ClientLimit: Dur(20 * time.Second),
		Description: "the archived #695/#700 windows; eligible as acceptance evidence only inside the verified envelope"},
	"quick": {Name: "quick", AcceptanceEligible: false,
		Warmup: Dur(time.Second), Measure: Dur(3 * time.Second), Recovery: Dur(2 * time.Second),
		ReloadMeasure: Dur(8 * time.Second), ReloadRecovery: Dur(2 * time.Second), StepWarmup: Dur(time.Second),
		F3Duration: Dur(4 * time.Second), F4ClientLimit: Dur(6 * time.Second),
		Description: "smoke only: exercises the instruments; never acceptance, comparison or capacity evidence"},
}

func intp(v int) *int { return &v }

func norm(name, proto string) Workload {
	return Workload{Name: name, Proto: proto, Method: "POST", Path: "/proxy/echo?resp=65536", BodyBytes: 16 << 10,
		BodyShape: "wide", RespBytes: 64 << 10, Concurrency: 16, Timeout: Dur(30 * time.Second), WantEcho: true}
}

// unsaturated is the calibrated normal-load concurrency: per protocol and
// variant, the calibration_rule applied to the calibration suite run on the
// ADR baseline (858590dc, go1.27.3) recorded in
// docs/reviews/2026-11-12-700-i5-assessment-tooling-baselines.md. It was set
// from that run before any campaign measurement and is frozen with the plan.
var unsaturated = map[string]map[string]int{
	"h1": {"none": 16, "waf": 1, "wasm": 2, "both": 1},
	"h2": {"none": 16, "waf": 1, "wasm": 2, "both": 1},
	"h3": {"none": 16, "both": 1},
}

// BuildScenarios expands every frozen scenario for a profile.
func BuildScenarios(p Profile) []Scenario {
	warm, meas, rec := p.Warmup, p.Measure, p.Recovery
	one := func(name string, w ...Workload) []Phase {
		return []Phase{{Name: name, Warmup: warm, Measure: meas, Workloads: w}}
	}
	stress := Workload{Name: "load", Proto: "h2", Method: "POST", Path: "/proxy/echo?resp=1048576&chunked=1", BodyBytes: 256 << 10,
		BodyShape: "wide", RespBytes: 1 << 20, Concurrency: 32, Timeout: Dur(60 * time.Second), WantEcho: true}
	var ramp []Phase
	for _, c := range []int{16, 32, 64, 128, 192} {
		w := stress
		w.Concurrency = c
		ramp = append(ramp, Phase{Name: fmt.Sprintf("c%d", c), Warmup: p.StepWarmup, Measure: meas / 2, Workloads: []Workload{w}})
	}
	adm := Workload{Name: "load", Proto: "h2", Method: "POST", Path: "/admitted/echo?resp=65536&delay=500", BodyBytes: 256 << 10,
		BodyShape: "wide", RespBytes: 64 << 10, Concurrency: 128, Timeout: Dur(30 * time.Second), WantEcho: true, Expect: []string{"status:503"}}
	static := Workload{Name: "load", Proto: "h2", Method: "GET", Path: "/static/page.html?x=1", RespBytes: 256 << 10,
		Concurrency: 128, Timeout: Dur(30 * time.Second)}
	cold := Workload{Name: "cold", Proto: "h1", Method: "GET", Path: "/cached/echo?resp=262144", RespBytes: 256 << 10,
		Concurrency: 16, Timeout: Dur(30 * time.Second), UniqueKey: true}
	warmW := cold
	warmW.Name, warmW.UniqueKey, warmW.Path = "warm", false, "/cached/echo?resp=262144&k=warm"
	slow := Workload{Name: "slow", Proto: "h1", Method: "POST", Path: "/proxy/echo?resp=4096", BodyBytes: 64 << 10, BodyShape: "wide",
		RespBytes: 4096, Concurrency: 96, SlowBPS: 16 << 10, Timeout: Dur(60 * time.Second), WantEcho: true}
	slowCancel := slow
	slowCancel.Name, slowCancel.Concurrency, slowCancel.CancelAfter = "slow-cancel", 32, Dur(2*time.Second)
	slowCancel.Expect = []string{"client-cancel"}
	probe := Workload{Name: "probe", Proto: "h1", Method: "POST", Path: "/proxy/echo?resp=4096", BodyBytes: 2 << 10, BodyShape: "wide",
		RespBytes: 4096, Concurrency: 4, Timeout: Dur(30 * time.Second), WantEcho: true}
	life := norm("load", "h1")
	inflight := Workload{Name: "inflight", Proto: "h2", Method: "POST", Path: "/proxy/echo?resp=65536&delay=3000", BodyBytes: 16 << 10,
		BodyShape: "wide", RespBytes: 64 << 10, Concurrency: 16, Timeout: Dur(30 * time.Second), WantEcho: true}
	reloadInflight := inflight
	reloadInflight.Expect = []string{"error:stream-reset"}
	amp := func(cs ...int) []Phase {
		var out []Phase
		for _, c := range cs {
			w := norm("load", "h1")
			w.BodyShape, w.Concurrency, w.RespBytes, w.Path = "items", c, 4096, "/proxy/echo?resp=4096"
			out = append(out, Phase{Name: fmt.Sprintf("c%d", c), Warmup: p.StepWarmup, Measure: meas / 2, Workloads: []Workload{w}})
		}
		return out
	}
	slow16 := slow
	slow16.Concurrency = 16
	nobody := Workload{Name: "load", Proto: "h1", Method: "GET", Path: "/proxy/echo?resp=4096", RespBytes: 4096, Concurrency: 16,
		Timeout: Dur(30 * time.Second)}
	calibrate := func(proto string) []Phase {
		var out []Phase
		for _, c := range []int{1, 2, 4, 8, 16} {
			w := norm("load", proto)
			w.Concurrency = c
			out = append(out, Phase{Name: fmt.Sprintf("c%d", c), Warmup: p.StepWarmup, Measure: meas / 2, Workloads: []Workload{w}})
		}
		return out
	}
	unsat := func(id, proto, src string, variants []string) Scenario {
		return Scenario{ID: id, Source: src, Kind: "load", Purpose: "unsaturated normal throughput (A13 shape)", Variants: variants, Reps: 3,
			Phases: one("load", norm("load", proto)), Recovery: rec, Comparison: "throughput", ConcurrencyByVariant: unsaturated[proto],
			Changes: []string{"the calibrated concurrency replaces the archived C=16 for inspected variants (waf and both 1, wasm 2), so those cells are not comparable with #695 C=16 numbers; none stays at C=16, where Jul CPU is about 0.6 of the envelope but throughput flattens, so a limit outside Jul CPU may bound it"}}
	}
	f3 := func(id string, pending int) Scenario {
		adm := Workload{Name: "admission", Proto: "h2", Method: "POST", Path: "/admitted/echo?resp=65536&delay=500", BodyBytes: 256 << 10,
			BodyShape: "wide", RespBytes: 64 << 10, Concurrency: 128, Connections: 1, Timeout: Dur(30 * time.Second), WantEcho: true,
			Expect: []string{"status:503"}}
		pr := Workload{Name: "probe", Proto: "h2", Method: "POST", Path: "/proxy/echo?resp=4096", BodyBytes: 2 << 10, BodyShape: "wide",
			RespBytes: 4096, Concurrency: 1, Connections: 1, Interval: Dur(100 * time.Millisecond), Timeout: Dur(5 * time.Second), WantEcho: true}
		return Scenario{ID: id, Source: "#700 ADR F3", Kind: "load", Purpose: "single-connection H2 admission goodput with a separate-connection probe",
			Variants: []string{"none", "both"}, Reps: 1, MaxPending: intp(pending), Recovery: rec, Comparison: "record",
			Phases: []Phase{{Name: "load", Measure: p.F3Duration, Workloads: []Workload{adm, pr}}},
			Changes: []string{"responses are verified (status, length, content, origin echo) before counting goodput; the archived client counted any 200",
				"bodies are the wide JSON shape with Content-Type application/json instead of unlabelled 'x'/'p' fill so the both variant inspects ordinary JSON; the none variant is unaffected",
				"the worker and probe connection counts are measured and must each equal one",
				"the archived optional Retry-After pacing mode is not ported"}}
	}
	f4 := func(id, readTimeout string) Scenario {
		return Scenario{ID: id, Source: "#700 ADR F4", Kind: "f4", Purpose: "stalled/slow uploads with a sibling request on the same connection",
			Variants: []string{"none", "both"}, Reps: 1, ReadTimeout: readTimeout, Recovery: rec, Comparison: "record",
			F4: &F4{Protos: []string{"h1", "h2", "h3"}, Modes: []string{"stall", "slow"}, BodyBytes: 16 << 10, ChunkBytes: 1 << 10,
				SlowInterval: Dur(500 * time.Millisecond), SiblingAfter: Dur(time.Second), ClientLimit: p.F4ClientLimit},
			Changes: []string{"the upload is the 16 KiB wide JSON shape with Content-Type application/json instead of unlabelled 's' fill so the both variant inspects ordinary JSON",
				"the sibling response is verified (length, content, plugin headers) and its connection relationship is measured (one dialled connection for H2/H3) instead of assumed",
				"the external client limit is the watchdog for runtimes without an inspection body deadline"}}
	}
	all := Variants
	return []Scenario{
		{ID: "s1-normal-h1", Source: "#695 s1", Kind: "load", Purpose: "historical normal H1 shape (C=16; CPU-saturated for inspected variants)", Variants: all, Reps: 3,
			Phases: one("load", norm("load", "h1")), Recovery: rec, Comparison: "capacity"},
		{ID: "s2-normal-h2", Source: "#695 s2", Kind: "load", Purpose: "historical normal H2 shape (C=16; CPU-saturated for inspected variants)", Variants: all, Reps: 3,
			Phases: one("load", norm("load", "h2")), Recovery: rec, Comparison: "capacity"},
		{ID: "s3-large-h2", Source: "#695 s3", Kind: "load", Purpose: "large bodies and responses (destructive on uncapped runtimes)", Variants: all, Reps: 3,
			Phases: one("load", stress), Recovery: rec, Comparison: "capacity"},
		{ID: "s4-ramp-large-h2", Source: "#695 s4", Kind: "load", Purpose: "concurrency ramp of large inspected bodies (destructive)", Variants: []string{"none", "both"}, Reps: 1,
			Phases: ramp, Recovery: rec, Comparison: "capacity"},
		{ID: "s5-admission-h2", Source: "#695 s5", Kind: "load", Purpose: "ADR 0017 admission behind a slow origin", Variants: []string{"none", "both"}, Reps: 1,
			Phases: one("load", adm), Recovery: rec, Comparison: "capacity",
			Changes: []string{"body is 256 KiB as in the archived code; the #695 plan text said 64 KiB"}},
		{ID: "s6-static-h2", Source: "#695 s6", Kind: "load", Purpose: "no-upstream static route through the same layers", Variants: []string{"none", "both"}, Reps: 1,
			Phases: one("load", static), Recovery: rec, Comparison: "capacity"},
		{ID: "s7-cache-h1", Source: "#695 s7", Kind: "load", Purpose: "cache cold (unique keys) then warm (one key)", Variants: []string{"none", "both"}, Reps: 1, Cache: true,
			Phases:   []Phase{{Name: "cold", Warmup: warm, Measure: meas, Workloads: []Workload{cold}}, {Name: "warm", Warmup: warm, Measure: meas, Workloads: []Workload{warmW}}},
			Recovery: rec, Comparison: "capacity"},
		{ID: "s8-slow-clients-h1", Source: "#695 s8", Kind: "load", Purpose: "slow and cancelling uploads with fast probes", Variants: all, Reps: 1,
			Phases: one("load", slow, slowCancel, probe), Recovery: rec, Comparison: "record"},
		{ID: "s9-reload-churn", Source: "#695 s9", Kind: "reload", Purpose: "policy reload churn every 2s, reload 5 invalid, slow in-flight H2", Variants: []string{"both"}, Reps: 1,
			Phases: []Phase{{Name: "load", Warmup: warm, Measure: p.ReloadMeasure, Workloads: []Workload{life, reloadInflight}}}, Recovery: p.ReloadRecovery, Comparison: "record",
			Changes: []string{"in-flight requests on the policy-changed route are retired within 2s by design (#642/#562; WAF rules and plugin modules are access inputs), so the inflight class expects error:stream-reset; the archived #695 runtime already contained it (#653) but the archive counted every non-200 as an error and s9 was OOM-killed before per-class outcomes were captured"}},
		{ID: "s10-normal-h3", Source: "#695 s10", Kind: "load", Purpose: "historical normal H3 shape (C=16)", Variants: []string{"none", "both"}, Reps: 1,
			Phases: one("load", norm("load", "h3")), Recovery: rec, Comparison: "capacity"},
		{ID: "s11-arg-amplification-h1", Source: "#695 s11", Kind: "load", Purpose: "many-argument JSON at C=1..16 (destructive on uncapped runtimes)", Variants: []string{"none", "waf"}, Reps: 1,
			Phases: amp(1, 2, 4, 8, 16), Recovery: rec, Comparison: "record"},
		{ID: "s11l-arg-amplification-low-h1", Source: "#695 s11 (C=1,2 subset)", Kind: "load", Purpose: "non-destructive amplification smoke", Variants: []string{"none", "waf"}, Reps: 1,
			Phases: amp(1, 2), Recovery: rec, Comparison: "record",
			Changes: []string{"only the C=1 and C=2 steps; the full s11 ramp is the destructive I6 profile"}},
		{ID: "r1-slow-upload-c16-h1", Source: "#699 r1", Kind: "load", Purpose: "slow uploads below the instance cap with POST probes", Variants: []string{"wasm"}, Reps: 1,
			Phases: one("load", slow16, probe), Recovery: rec, Comparison: "record",
			Changes: []string{"the #699 review text calls the probes GET; the measured #699 patch used the 2 KiB POST probe kept here"}},
		{ID: "r2-fast-nobody-h1", Source: "#699 r2", Kind: "load", Purpose: "fast requests without a body", Variants: []string{"none", "wasm"}, Reps: 1,
			Phases: one("load", nobody), Recovery: rec, Comparison: "capacity"},
		{ID: "r3-fast-body-h1", Source: "#699 r3", Kind: "load", Purpose: "fast body requests (normal H1 shape)", Variants: []string{"wasm"}, Reps: 1,
			Phases: one("load", norm("load", "h1")), Recovery: rec, Comparison: "capacity"},
		{ID: "cal-normal-h1", Source: "I5 calibration of s1", Kind: "load", Purpose: "unsaturated-concurrency calibration", Variants: all, Reps: 1,
			Phases: calibrate("h1"), Recovery: rec, Comparison: "record"},
		{ID: "cal-normal-h2", Source: "I5 calibration of s2", Kind: "load", Purpose: "unsaturated-concurrency calibration", Variants: all, Reps: 1,
			Phases: calibrate("h2"), Recovery: rec, Comparison: "record"},
		{ID: "cal-normal-h3", Source: "I5 calibration of s10", Kind: "load", Purpose: "unsaturated-concurrency calibration", Variants: []string{"none", "both"}, Reps: 1,
			Phases: calibrate("h3"), Recovery: rec, Comparison: "record"},
		unsat("u1-normal-h1", "h1", "#695 s1 at calibrated concurrency", all),
		unsat("u2-normal-h2", "h2", "#695 s2 at calibrated concurrency", all),
		unsat("u10-normal-h3", "h3", "#695 s10 at calibrated concurrency", []string{"none", "both"}),
		f3("f3-admission-pending16", 16),
		f3("f3-admission-pending0", 0),
		f4("f4-stalled-body", ""),
		f4("f4-stalled-body-rt5s", "5s"),
	}
}

// Suites name frozen scenario/variant selections.
var Suites = map[string][]string{
	"campaign-normal": {"u1-normal-h1", "u2-normal-h2", "u10-normal-h3"},
	"calibration":     {"cal-normal-h1", "cal-normal-h2", "cal-normal-h3"},
	"fidelity":        {"r1-slow-upload-c16-h1", "s8-slow-clients-h1", "s11l-arg-amplification-low-h1", "s9-reload-churn", "f3-admission-pending16", "f3-admission-pending0", "f4-stalled-body", "s6-static-h2", "s7-cache-h1", "r2-fast-nobody-h1"},
	"historical-695":  {"s1-normal-h1", "s2-normal-h2", "s3-large-h2", "s4-ramp-large-h2", "s5-admission-h2", "s6-static-h2", "s7-cache-h1", "s8-slow-clients-h1", "s9-reload-churn", "s10-normal-h3", "s11-arg-amplification-h1"},
	"historical-699":  {"r1-slow-upload-c16-h1", "r2-fast-nobody-h1", "r3-fast-body-h1"},
	"destructive":     {"s3-large-h2", "s4-ramp-large-h2", "s11-arg-amplification-h1"},
	"smoke":           {"u1-normal-h1", "u2-normal-h2", "u10-normal-h3", "s8-slow-clients-h1", "s9-reload-churn", "f3-admission-pending0", "f4-stalled-body"},
}

// Interpretation holds the frozen summary rules.
type Interpretation struct {
	MinRepetitions     int     `json:"min_repetitions"`
	MaxRegression      float64 `json:"max_median_regression"`
	MaxSpread          float64 `json:"max_relative_spread"`
	SaturatedCPU       float64 `json:"saturated_cpu_fraction"`
	CalibrationCPU     float64 `json:"calibration_cpu_fraction"`
	EnvelopeCPUs       float64 `json:"envelope_cpus"`
	EnvelopeMemory     int64   `json:"envelope_memory_bytes"`
	TargetPeak         int64   `json:"target_peak_bytes"`
	SpreadDefinition   string  `json:"spread_definition"`
	RegressionRule     string  `json:"regression_rule"`
	SaturationRule     string  `json:"saturation_rule"`
	CalibrationRule    string  `json:"calibration_rule"`
	ValidityRule       string  `json:"validity_rule"`
	NoSubsetSelection  string  `json:"no_subset_selection"`
	AcceptanceBoundary string  `json:"acceptance_boundary"`
}

// Rules are the interpretation rules frozen with the plan.
var Rules = Interpretation{
	MinRepetitions: 3, MaxRegression: 0.10, MaxSpread: 0.10, SaturatedCPU: 0.85, CalibrationCPU: 0.70,
	EnvelopeCPUs: 2, EnvelopeMemory: 768 << 20, TargetPeak: 576 << 20,
	SpreadDefinition:   "(max - min) / median of a side's useful-goodput values",
	RegressionRule:     "regression = 1 - median(candidate)/median(baseline); above max_median_regression fails even when ranges overlap",
	SaturationRule:     "a run whose Jul CPU use over the measurement window is at least saturated_cpu_fraction of envelope_cpus is saturated; a throughput cell containing a saturated run is inconclusive",
	CalibrationRule:    "per protocol and variant, the largest calibration concurrency whose measured Jul CPU fraction on the ADR baseline is at most calibration_cpu_fraction with every response verified",
	ValidityRule:       "instrument validity: the run completed, canaries ran, the envelope was verified when required, every class started attempts inside its window using the planned protocol and connection count, and the client could build its requests. Runtime findings (unexpected status or error, unverified content, failed canary, Jul exit, OOM-kill delta) are recorded separately: a valid run with findings is evidence of incorrect behaviour and fails correctness in a comparison; it is never counted as goodput or dropped",
	NoSubsetSelection:  "every attempted repetition is reported; an invalid, failed or inconclusive run is never dropped or replaced; added repetitions supplement it",
	AcceptanceBoundary: "an I5 result shows that the instrument captured a runtime; it is not A1-A18 acceptance. Throughput verdicts between the ADR baseline and post-I1 main are informational because I1 has no request-path enforcement",
}

// PlanDocument is the frozen, checked-in plan.
type PlanDocument struct {
	Version        string                `json:"version"`
	Rules          Interpretation        `json:"interpretation"`
	Suites         map[string][]string   `json:"suites"`
	Profiles       map[string]Profile    `json:"profiles"`
	Scenarios      map[string][]Scenario `json:"scenarios_by_profile"`
	ConfigTemplate string                `json:"config_template_sha256"`
}

// Plan builds the complete plan document.
func Plan() PlanDocument {
	doc := PlanDocument{Version: PlanVersion, Rules: Rules, Suites: Suites, Profiles: Profiles, Scenarios: map[string][]Scenario{},
		ConfigTemplate: sha256Hex([]byte(configTemplate))}
	for name, p := range Profiles {
		doc.Scenarios[name] = BuildScenarios(p)
	}
	return doc
}

// PlanJSON renders the canonical plan bytes.
func PlanJSON() []byte {
	b, err := json.MarshalIndent(Plan(), "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ScenarioHash identifies one scenario definition inside a profile.
func ScenarioHash(s Scenario) string {
	b, _ := json.Marshal(s)
	return sha256Hex(b)
}

// Cell is one scenario × variant selected for execution.
type Cell struct {
	Scenario Scenario
	Variant  string
}

// Select resolves suites/scenarios/variants for a profile.
func Select(profile, suite, scenarios, variants string) ([]Cell, error) {
	p, ok := Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}
	var ids []string
	if suite != "" {
		s, ok := Suites[suite]
		if !ok {
			return nil, fmt.Errorf("unknown suite %q", suite)
		}
		ids = append(ids, s...)
	}
	ids = append(ids, splitList(scenarios)...)
	if len(ids) == 0 {
		return nil, fmt.Errorf("select a -suite or -scenario")
	}
	byID := map[string]Scenario{}
	for _, s := range BuildScenarios(p) {
		byID[s.ID] = s
	}
	want := splitList(variants)
	for _, v := range want {
		if !slices.Contains(Variants, v) {
			return nil, fmt.Errorf("unknown variant %q", v)
		}
	}
	var out []Cell
	seen := map[string]bool{}
	for _, id := range ids {
		s, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("unknown scenario %q", id)
		}
		for _, v := range s.Variants {
			if (len(want) > 0 && !slices.Contains(want, v)) || seen[id+"/"+v] {
				continue
			}
			seen[id+"/"+v] = true
			out = append(out, Cell{Scenario: Resolve(s, v), Variant: v})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("selection matches no scenario variant")
	}
	return out, nil
}

// Resolve applies the per-variant concurrency of a scenario.
func Resolve(s Scenario, variant string) Scenario {
	c, ok := s.ConcurrencyByVariant[variant]
	if !ok {
		return s
	}
	phases := make([]Phase, len(s.Phases))
	for i, ph := range s.Phases {
		ph.Workloads = slices.Clone(ph.Workloads)
		ph.Workloads[0].Concurrency = c
		phases[i] = ph
	}
	s.Phases = phases
	s.ConcurrencyByVariant = map[string]int{variant: c}
	return s
}

func splitList(s string) []string {
	var out []string
	for _, f := range strings.Split(s, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
