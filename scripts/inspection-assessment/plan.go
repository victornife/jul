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
const PlanVersion = "inspection-assessment-plan/v3"

// Variants are the four inspection configurations preserved from #695.
var Variants = []string{"none", "waf", "wasm", "both"}

// Plugin fixtures (plan v3). The empty fixture is the #695 inspect-load pair
// that reads the request body and subscribes to the response body.
const (
	// FixtureFull is the default fixture: inspect-a and inspect-b read the
	// request body and subscribe to (and b replaces) the response body.
	FixtureFull = "full"
	// FixtureRequestOnly is inspect-load reading only the request body: no
	// response subscription, so ungoverned response buffering (ADR 0032 I4)
	// cannot dominate a plugin-only request-path experiment.
	FixtureRequestOnly = "request-only"
	// FixtureNoBody is the inspect-nobody guest: no read_request_body import,
	// so request bodies stream; it holds 4 KiB of request state per plugin.
	FixtureNoBody = "nobody"
)

// Fixtures are the known plugin fixtures.
var Fixtures = []string{FixtureFull, FixtureRequestOnly, FixtureNoBody}

// FixtureOf returns the effective plugin fixture of a scenario.
func FixtureOf(s Scenario) string {
	if s.Fixture == "" {
		return FixtureFull
	}
	return s.Fixture
}

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
	// HonorRetryAfter makes a worker wait the advertised second after an
	// inspection overload response, as a compliant client does.
	HonorRetryAfter bool `json:"honor_retry_after,omitempty"`
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
	ReadTimeout string `json:"read_timeout,omitempty"`
	// BodyTimeout sets [global.inspection].body_timeout. Only adapters with
	// GlobalInspection can express it; others refuse the cell.
	BodyTimeout string `json:"body_timeout,omitempty"`
	// Fixture selects the plugin fixture for wasm/both variants (plan v3;
	// "" is FixtureFull).
	Fixture string `json:"fixture,omitempty"`
	// InspectionMode sets [global.inspection].mode ("" keeps enforce).
	InspectionMode string `json:"inspection_mode,omitempty"`
	// MaxBuffered sets [global.inspection].max_buffered_bytes for every
	// variant, replacing the adapter's WAF budget.
	MaxBuffered string `json:"max_buffered_bytes,omitempty"`
	// PluginTimeout sets the request-only plugins' per-invocation timeout
	// ("" keeps the 100ms default).
	PluginTimeout string `json:"plugin_timeout,omitempty"`
	// PluginMaxInstances caps each request-only plugin's live instances
	// (0 keeps the default of 64).
	PluginMaxInstances int `json:"plugin_max_instances,omitempty"`
	// GoMemLimit sets Jul's GOMEMLIMIT ("" leaves it unset).
	GoMemLimit string `json:"gomemlimit,omitempty"`
	// ReloadChurn selects the reload sequence: "" is the #695 s9 policy
	// churn; "budget" also cycles max_buffered_bytes and mode (plan v3).
	ReloadChurn string `json:"reload_churn,omitempty"`
	// ReloadEvery overrides the 2 s reload churn interval (plan v3).
	ReloadEvery Dur     `json:"reload_every,omitempty"`
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
	// Require lists what a run must show to have exercised its purpose
	// (plan v3).
	Require []Requirement `json:"exercise_requirements,omitempty"`
}

// Requirement is one plan v3 exercise check, over a counter delta key (see
// CounterMetrics) or an outcome class summed over every measured class. An
// unmet AtLeast means the run did not exercise its purpose: the instrument
// is invalid, never a clean pass. An exceeded AtMost is a runtime finding.
// A missing counter fails AtLeast and is reported for AtMost.
type Requirement struct {
	Counter string `json:"counter,omitempty"`
	Class   string `json:"class,omitempty"`
	AtLeast *int64 `json:"at_least,omitempty"`
	AtMost  *int64 `json:"at_most,omitempty"`
}

func atLeast(n int64) *int64 { return &n }

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
		F3Duration: Dur(4 * time.Second), F4ClientLimit: Dur(12 * time.Second),
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
	"h3": {"none": 16, "wasm": H3WasmConcurrency, "both": 1},
}

// H3WasmConcurrency is the plan v3 calibration of cal-normal-h3/wasm on the
// ADR baseline (858590dc, go1.27.3); see
// docs/reviews/2026-11-12-700-i2-plugin-request-path.md section 3.
const H3WasmConcurrency = 2

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
	// Plan v3 (I2) plugin-only request-path shapes. Bodies stay below the
	// 1 MiB default plugin max_request_body. A known-length prepared body is
	// charged at about its length, so the saturation shape uses 960 KiB
	// bodies held for 5 s at the origin: 160 retained bodies need about
	// 150 MiB, above the 128 MiB budget, for the whole window.
	large := Workload{Name: "load", Proto: "h1", Method: "POST", Path: "/proxy/echo?resp=4096", BodyBytes: 768 << 10,
		BodyShape: "wide", RespBytes: 4096, Concurrency: 64, Timeout: Dur(60 * time.Second), WantEcho: true}
	satLoad := large
	satLoad.Path, satLoad.BodyBytes, satLoad.Concurrency, satLoad.Expect = "/proxy/echo?resp=4096&delay=5000", 960<<10, 160, []string{OverloadClass}
	satProbe := probe
	satProbe.Concurrency, satProbe.Expect = 2, []string{OverloadClass}
	// The hostile twin retries a rejected upload at once; the compliant
	// shapes wait the advertised Retry-After.
	hostileLoad := satLoad
	satLoad.HonorRetryAfter, satProbe.HonorRetryAfter = true, true
	obsLoad, obsProbe := satLoad, satProbe
	obsLoad.Expect, obsProbe.Expect = nil, nil
	zero := atLeast(0)
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
	// sized applies the recorded plugin-only static footprint: four live
	// instances per plugin and the ADR's GOMEMLIMIT recommendation.
	sized := func(sc Scenario) Scenario {
		sc.PluginMaxInstances, sc.PluginTimeout, sc.GoMemLimit = 4, "2s", "480MiB"
		return sc
	}
	plugin := func(id, purpose, fixture string, variants []string, phases []Phase, comparison string, changes ...string) Scenario {
		return Scenario{ID: id, Source: "#700 I2 (plan v3)", Kind: "load", Purpose: purpose, Variants: variants, Reps: 1,
			Fixture: fixture, Phases: phases, Recovery: rec, Comparison: comparison, Changes: changes}
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
			Variants: []string{"none", "wasm", "both"}, Reps: 1, ReadTimeout: readTimeout, Recovery: rec, Comparison: "record",
			F4: &F4{Protos: []string{"h1", "h2", "h3"}, Modes: []string{"stall", "slow"}, BodyBytes: 16 << 10, ChunkBytes: 1 << 10,
				SlowInterval: Dur(500 * time.Millisecond), SiblingAfter: Dur(time.Second), ClientLimit: p.F4ClientLimit},
			Changes: []string{"the upload is the 16 KiB wide JSON shape with Content-Type application/json instead of unlabelled 's' fill so the both variant inspects ordinary JSON",
				"the sibling response is verified (length, content, plugin headers) and its connection relationship is measured (one dialled connection for H2/H3) instead of assumed",
				"the external client limit is the watchdog for runtimes without an inspection body deadline",
				"plan v3 adds the plugin-only wasm variant"}}
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
		{ID: "s6-static-h2", Source: "#695 s6", Kind: "load", Purpose: "no-upstream static route through the same layers", Variants: []string{"none", "wasm", "both"}, Reps: 1,
			Phases: one("load", static), Recovery: rec, Comparison: "capacity", Changes: []string{"plan v3 adds the wasm variant"}},
		{ID: "s7-cache-h1", Source: "#695 s7", Kind: "load", Purpose: "cache cold (unique keys) then warm (one key)", Variants: []string{"none", "wasm", "both"}, Reps: 1, Cache: true,
			Changes:  []string{"plan v3 adds the wasm variant"},
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
		{ID: "cal-normal-h3", Source: "I5 calibration of s10", Kind: "load", Purpose: "unsaturated-concurrency calibration", Variants: []string{"none", "wasm", "both"}, Reps: 1,
			Phases: calibrate("h3"), Recovery: rec, Comparison: "record"},
		unsat("u1-normal-h1", "h1", "#695 s1 at calibrated concurrency", all),
		unsat("u2-normal-h2", "h2", "#695 s2 at calibrated concurrency", all),
		unsat("u10-normal-h3", "h3", "#695 s10 at calibrated concurrency", []string{"none", "wasm", "both"}),
		f3("f3-admission-pending16", 16),
		f3("f3-admission-pending0", 0),
		{ID: "u1b-normal-h1-none-c8", Source: "#695 s1 at C=8 (plan v3)", Kind: "load", Purpose: "stabilised uninspected H1 throughput (A13 shape)",
			Variants: []string{"none"}, Reps: 5, Phases: one("load", norm("load", "h1")), Recovery: rec, Comparison: "throughput",
			ConcurrencyByVariant: map[string]int{"none": 8},
			Changes:              []string{"added in plan v3 before any I2 measurement: I5 recorded u1/none (C=16) as inconclusive-noise (spread 0.119) with throughput bound partly outside Jul CPU; C=8 keeps the client and origin further from their limit and 5 repetitions are planned; u1/none at C=16 is still run and reported"}},
		sized(plugin("p1-large-body-plugin-h1", "bounded large-body plugin-only request path at the default 256 MiB budget", FixtureRequestOnly,
			[]string{"none", "wasm"}, one("load", large), "record",
			"static footprint sized per ADR 0032 §10: with default plugin pools (2 × 64 × 16 MiB) pre-freeze envelope smokes were OOM-killed on both the candidate and d8d0520b; max_instances 4 per plugin, a 2s plugin timeout (also bounding the finite instance wait) and GOMEMLIMIT=480MiB apply to both variants; a sized smoke with the 100ms default was dominated by pool-wait 503s and contained timeouts; the 256m budget stays at its default")),
		{ID: "p2-saturation-plugin-h1", Source: "#700 I2 (plan v3)", Kind: "load", Purpose: "deliberate enforce-mode byte saturation of the plugin request path (fail-fast 503 with probes)",
			Variants: []string{"wasm"}, Reps: 1, Fixture: FixtureRequestOnly, MaxBuffered: "128m", PluginTimeout: "2s", PluginMaxInstances: 4, GoMemLimit: "480MiB", Recovery: rec, Comparison: "record",
			Phases: one("load", satLoad, satProbe),
			Require: []Requirement{{Counter: "inspection_rejected_enforce", AtLeast: atLeast(1)}, {Class: OverloadClass, AtLeast: atLeast(1)},
				{Counter: "inspection_rejected_observe", AtMost: zero}},
			Changes: []string{"128m is above the 103825411 B compiled minimum of the two-plugin route (default plugin limits), so genuine validation passes; 160 concurrent 960 KiB bodies held 5 s at the origin need about 150 MiB of prepared capacity",
				"plugin timeout 2s: a pre-freeze smoke with the 100ms default and a 1 s hold turned CPU contention between up to 64 concurrent 1 MB guest scans per plugin into contained guest timeouts (500) before the byte budget filled; the longer timeout and hold isolate the byte dimension and change no governor limit",
				"static footprint sized per ADR 0032 §10 (not guaranteed: guest pools and GC headroom): with default pools (2 × 64 × 16 MiB = 2 GiB) a pre-freeze envelope smoke was OOM-killed at 593 MiB heap with 37 instances and 50 MiB governed; with 2 × 8 instances and no GOMEMLIMIT it was OOM-killed at 532 MiB heap with 126 MiB governed (GC headroom over a ~420 MiB live set)",
				"max_instances 4 per plugin (128 MiB guest pool) and GOMEMLIMIT=480MiB, the ADR's recommendation: about 128 MiB governed + 128 MiB pool + baseline stays well below the limit, and the limit is below the 576 MiB objective",
				"only a 503 with Retry-After: 1 and the inspection_overloaded body is the expected overload class; any other 503 is a finding",
				"load and probe workers wait the advertised Retry-After after an overload response; p2h keeps the no-backoff shape as a separate record"}},
		{ID: "p2h-saturation-hostile-h1", Source: "#700 I2 (plan v3)", Kind: "load", Purpose: "p2 with clients that ignore Retry-After: records connection-buffer exposure of fast rejection (ADR 0032 §10, not guaranteed)",
			Variants: []string{"wasm"}, Reps: 1, Fixture: FixtureRequestOnly, MaxBuffered: "128m", PluginTimeout: "2s", PluginMaxInstances: 4, GoMemLimit: "480MiB", Recovery: rec, Comparison: "record",
			Phases:  one("load", hostileLoad, satProbe),
			Require: []Requirement{{Counter: "inspection_rejected_enforce", AtLeast: atLeast(1)}, {Class: OverloadClass, AtLeast: atLeast(1)}},
			Changes: []string{"pre-freeze envelope smokes of the no-backoff shape were OOM-killed with governed bytes at 126 MiB: about 1300 rejected 960 KiB uploads per second each leave a connection in net/http's 500 ms close-write linger while the client keeps sending, and the unread bytes sit in kernel socket buffers charged to Jul's cgroup (memory.stat sock reached 286 MiB beside 393 MiB anon)",
				"connection and kernel buffers are outside the governor (ADR 0032 §10); this cell is a record for I6 sizing, never a protection claim, and an OOM here is preserved as a finding"}},
		{ID: "p2o-saturation-plugin-observe-h1", Source: "#700 I2 (plan v3)", Kind: "load", Purpose: "observe twin of p2: same load and budget, would-reject recorded, no governor rejection",
			Variants: []string{"wasm"}, Reps: 1, Fixture: FixtureRequestOnly, MaxBuffered: "128m", InspectionMode: "observe", PluginTimeout: "2s", PluginMaxInstances: 4, GoMemLimit: "480MiB", Recovery: rec, Comparison: "record",
			Phases: one("load", obsLoad, obsProbe),
			Require: []Requirement{{Counter: "inspection_rejected_observe", AtLeast: atLeast(1)}, {Counter: "inspection_rejected_enforce", AtMost: zero},
				{Class: OverloadClass, AtMost: zero}}},
		plugin("p3-nobody-plugin-h1", "non-body plugins: streamed bodies, request state and header-only subscription", FixtureNoBody,
			[]string{"none", "wasm"}, one("load", norm("load", "h1"), nobody), "capacity"),
		{ID: "p4-reload-budget-plugin", Source: "#700 I2 (plan v3)", Kind: "reload", Purpose: "plugin-only reload churn with budget decrease/increase, enforce/observe changes and an inadmissible reload, slow in-flight H2",
			Variants: []string{"wasm"}, Reps: 1, Fixture: FixtureRequestOnly, MaxBuffered: "256m", ReloadChurn: "budget", ReloadEvery: Dur(5 * time.Second), PluginMaxInstances: 4, PluginTimeout: "2s", GoMemLimit: "480MiB",
			Phases: []Phase{{Name: "load", Warmup: warm, Measure: p.ReloadMeasure, Workloads: []Workload{life, reloadInflight}}}, Recovery: p.ReloadRecovery, Comparison: "record",
			Changes: []string{"reload i cycles max_buffered_bytes 128m, mode observe, 512m, 256m with the inspect-a name; reload 5 sets 1m, below the route minimum, and must be refused with the previous limits kept",
				"each budget reload waits up to 10 s for its jul_reload_total sighup outcome and then reads the effective limits from GET /api/v1/status; a missing acknowledgement, an unexpected outcome or effective limits other than those expected is a runtime finding",
				"static footprint sized as p1 (max_instances 4, plugin timeout 2s, GOMEMLIMIT=480MiB): with default pools a pre-freeze envelope smoke was OOM-killed at 759 MiB with near-zero governed bytes, about 24 leases and at most 13 instances, heap growing across overlapping generations",
				"waits 5 s after each reload and its acknowledgement instead of 2 s (an inter-step delay, not a fixed cadence): pre-freeze smokes at 2 s were OOM-killed with sized pools on both the candidate (752 MiB) and main d8d0520b (714 MiB) after about 8 reloads with zero governed bytes; at 5 s main completed (peaks 650 to 687 MiB) but kept about 495 MiB of heap in use after two GCs, mostly wazero guest memory and decoded modules of retired generations with only 2 live instances; that pre-existing generation retention is a recorded finding outside I2, so the cell asserts reload acknowledgement and governor drain, not the 576 MiB peak objective",
				"fewer than 5 acknowledged budget reloads (so the inadmissible step 5 never ran) makes the run unexercised; after recovery every governor lease, bytes and evaluation gauge must be zero, a nonzero one is a runtime finding and missing gauges make the run unexercised"}},
		f4("f4-stalled-body", ""),
		f4("f4-stalled-body-rt5s", "5s"),
		{ID: "f4-body-deadline-5s", Source: "I5 review of #708 (ADR 0032 H6, F4)", Kind: "f4",
			Purpose:  "stalled uploads against [global.inspection].body_timeout = 5s, well inside the client watchdog; inspected variants expect 408 near 5 s, uninspected ones are outside the deadline's scope",
			Variants: []string{"none", "wasm", "both"}, Reps: 1, BodyTimeout: "5s", Recovery: rec, Comparison: "record",
			F4: &F4{Protos: []string{"h1", "h2", "h3"}, Modes: []string{"stall"}, BodyBytes: 16 << 10, ChunkBytes: 1 << 10,
				SlowInterval: Dur(500 * time.Millisecond), SiblingAfter: Dur(time.Second), ClientLimit: Dur(20 * time.Second)},
			Changes: []string{"added in plan v2: the 60 s default body_timeout outlasts the 20 s F4 watchdog, so the deadline is measured with an explicit 5 s value; no archived counterpart",
				"plan v3 adds the plugin-only wasm variant (I2 governs the plugin request path; a WAF-only body deadline is I3)"}},
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
	"i2-normal":       {"u1-normal-h1", "u1b-normal-h1-none-c8", "u2-normal-h2", "u10-normal-h3"},
	"i2-plugin":       {"r1-slow-upload-c16-h1", "s8-slow-clients-h1", "r2-fast-nobody-h1", "s6-static-h2", "s7-cache-h1", "p1-large-body-plugin-h1", "p2-saturation-plugin-h1", "p2o-saturation-plugin-observe-h1", "p2h-saturation-hostile-h1", "p3-nobody-plugin-h1", "p4-reload-budget-plugin"},
	"i2-deadline":     {"f4-stalled-body", "f4-body-deadline-5s"},
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
	ValidityRule:       "instrument validity: the run completed, canaries ran, the envelope was verified when required, every class started attempts inside its window using the planned protocol and connection count, and the client could build its requests. Runtime findings (unexpected status or error, unverified content, failed canary, Jul exit, OOM-kill delta, a nonzero governor lease/bytes/evaluation gauge after recovery (plan v3)) are recorded separately: a valid run with findings is evidence of incorrect behaviour and fails correctness in a comparison; it is never counted as goodput or dropped",
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
