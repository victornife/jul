// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func pts(valid, correct bool, sat bool, gs ...float64) []Point {
	var out []Point
	for i, g := range gs {
		out = append(out, Point{Run: "r", Rep: i + 1, Goodput: g, Valid: valid, Correct: correct, Saturated: &sat})
	}
	return out
}

func TestCompareAppliesFrozenRules(t *testing.T) {
	ok := pts(true, true, false, 100, 101, 99)
	cases := []struct {
		name       string
		base, cand []Point
		want       string
	}{
		{"two reps", ok[:2], ok, VerdictInsufficient},
		{"pass", ok, pts(true, true, false, 98, 99, 100), VerdictPass},
		{"regression with overlapping ranges", pts(true, true, false, 100, 80, 120), pts(true, true, false, 85, 120, 80), VerdictRegression},
		{"noise", ok, pts(true, true, false, 95, 130, 80), VerdictNoise},
		{"instrument invalid", ok, pts(false, true, false, 100, 100, 100), VerdictInstrument},
		{"candidate incorrect", ok, pts(true, false, false, 100, 100, 100), VerdictCorrectness},
		{"baseline incorrect", pts(true, false, false, 100, 100, 100), ok, VerdictBaseInvalid},
		{"saturated", ok, pts(true, true, true, 100, 100, 100), VerdictSaturated},
		{"zero baseline", pts(true, true, false, 0, 0, 0), ok, VerdictBaseInvalid},
	}
	for _, tc := range cases {
		if got, _ := Compare(tc.base, tc.cand, Rules); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
	missing := pts(true, true, false, 100, 100, 100)
	missing[1].Saturated = nil
	if got, _ := Compare(ok, missing, Rules); got != VerdictSaturated {
		t.Errorf("unknown saturation must be inconclusive, got %s", got)
	}
	if _, reg := Compare(ok, pts(true, true, false, 89, 90, 91), Rules); reg == nil || *reg != 0.1 {
		t.Errorf("regression %v", reg)
	}
	if s := Describe(pts(true, true, false, 4, 1, 3, 2)); *s.Median != 2.5 || *s.Spread != 1.2 || s.N != 4 {
		t.Errorf("describe %+v", s)
	}
}

func TestClassStatsSeparatesInstrumentFromFindings(t *testing.T) {
	s := ClassStats{Attempts: 5, WantConns: 1, Dials: 2, Expect: []string{"status:503"},
		Classes: map[string]int{"ok": 1, "status:503": 1, "unverified:proto HTTP/1.1": 1, "status:500": 1, "error:eof": 1}}
	in, fi := s.Check()
	if len(in) != 2 || !strings.Contains(in[0], "dialled 2") || len(fi) != 2 {
		t.Errorf("instrument %v findings %v", in, fi)
	}
	if in, _ := (ClassStats{Classes: map[string]int{}}).Check(); len(in) != 1 {
		t.Errorf("empty window not instrument-invalid: %v", in)
	}
	f := F4Result{Proto: "h2", Mode: "stall", Warmup: "ok", SiblingClass: "error:client", Dials: 2, WantDials: 1}
	if in, fi := f.Check(); len(in) != 2 || len(fi) != 0 {
		t.Errorf("f4 %v %v", in, fi)
	}
	f = F4Result{Proto: "h3", Warmup: "status:500", SiblingClass: "error:timeout", Dials: 1, WantDials: 1}
	if in, fi := f.Check(); len(in) != 0 || len(fi) != 2 {
		t.Errorf("f4 %v %v", in, fi)
	}
}

func TestSummarizeAttributesByStartTime(t *testing.T) {
	from := time.Unix(100, 0)
	to := from.Add(10 * time.Second)
	at := func(s, e float64, class string) Outcome {
		return Outcome{Start: from.Add(time.Duration(s * float64(time.Second))), End: from.Add(time.Duration(e * float64(time.Second))), Class: class, Bytes: 10, Proto: "HTTP/1.1"}
	}
	outs := []Outcome{
		at(-1, -0.5, "ok"), // warm-up only: ignored
		at(-1, 0.5, "ok"),  // crossed in: never goodput
		at(1, 2, "ok"),     // good
		at(2, 3, "ok"),     // good
		at(3, 3.1, "status:503"),
		at(4, 4.1, "unverified:resp-len 3!=10"),
		at(9, 11, "ok"),    // outstanding at the window end
		at(10, 10.5, "ok"), // started after the end: ignored
		{Start: from.Add(5 * time.Second), End: from.Add(6 * time.Second), Class: "error:eof", Err: "EOF"},
	}
	s := Summarize(Workload{Name: "w", Proto: "h1"}, outs, from, to, 3)
	if s.Attempts != 6 || s.Verified != 3 || s.GoodN != 2 || s.CrossedIn != 1 || s.Outstanding != 1 {
		t.Fatalf("%+v", s)
	}
	if s.GoodputRPS != 0.2 || s.GoodputBPS != 2 || *s.P50ms != 1000 || s.ErrorSamples["error:eof"] != "EOF" {
		t.Errorf("%+v", s)
	}
}

func TestVerifyRejectsIncompleteOrWrongResponses(t *testing.T) {
	w := Workload{RespBytes: 8, WantEcho: true}
	good := Filler(8, "")
	mk := func(major int, h map[string]string) *http.Response {
		r := &http.Response{ProtoMajor: major, Proto: "HTTP/" + strconv.Itoa(major), Header: http.Header{}}
		for k, v := range h {
			r.Header.Set(k, v)
		}
		return r
	}
	echo := map[string]string{"X-Origin-Req-Len": "5"}
	wasm := map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "8", "X-Assess-b-Resp": "8", "X-Echo-B-Req-Len": "5"}
	cases := []struct {
		resp *http.Response
		body []byte
		wasm bool
		want string
	}{
		{mk(1, echo), good, false, ""},
		{mk(2, echo), good, false, "proto"},
		{mk(1, echo), good[:7], false, "resp-len"},
		{mk(1, echo), append(good[:7:7], 'X'), false, "resp-content"},
		{mk(1, nil), good, false, "origin-req-len"},
		{mk(1, wasm), good, true, ""},
		{mk(1, echo), good, true, "missing a-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "8", "X-Assess-b-Resp": "7"}), good, true, "b-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "8", "X-Assess-b-Resp": "8"}), good, true, "b-req-len"},
	}
	for i, tc := range cases {
		got := Verify(tc.resp, tc.body, w, 5, tc.wasm, "h1")
		if (tc.want == "") != (got == "") || !strings.HasPrefix(got, tc.want) {
			t.Errorf("case %d: %q, want %q", i, got, tc.want)
		}
	}
}

func TestErrClass(t *testing.T) {
	for err, want := range map[error]string{
		context.Canceled:         "error:cancelled",
		context.DeadlineExceeded: "error:timeout",
		errors.New("stream error: stream ID 3; INTERNAL_ERROR"): "error:stream-reset",
		errors.New("read: connection reset by peer"):            "error:reset",
		io.EOF:                                 "error:eof",
		errors.New("dial: connection refused"): "error:refused",
		io.ErrUnexpectedEOF:                    "error:eof",
		errors.New("short write"):              "error:short",
		errors.New("tls: bad"):                 "error:other",
	} {
		if got := errClass(err, false); got != want {
			t.Errorf("%v: %s, want %s", err, got, want)
		}
	}
	if errClass(context.DeadlineExceeded, true) != "client-cancel" {
		t.Error("planned cancellation not classified")
	}
}

func TestBodiesKeepArchivedShapes(t *testing.T) {
	for _, shape := range []string{"wide", "items", "fill", ""} {
		b := BodyFor(Workload{BodyBytes: 4096, BodyShape: shape})
		if len(b) > 4096 || len(b) < 4096-2100 {
			t.Errorf("%s: %d bytes", shape, len(b))
		}
	}
	if BodyFor(Workload{}) != nil {
		t.Error("bodyless workload got a body")
	}
	if b := ItemsBody(4096, wafReqMark); !bytes.Contains(b, []byte(wafReqMark)) || !bytes.HasPrefix(b, []byte("{")) {
		t.Errorf("items body %q", b[:40])
	}
	r := &slowReader{ctx: context.Background(), data: []byte("abcdef"), bps: 1 << 20}
	if got, _ := io.ReadAll(r); string(got) != "abcdef" {
		t.Errorf("slow reader %q", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := io.ReadAll(&slowReader{ctx: ctx, data: []byte("abc"), bps: 1}); err == nil {
		t.Error("cancelled slow reader kept writing")
	}
}

func TestOriginFaults(t *testing.T) {
	get := func(o *Origin, path string) (*http.Response, []byte) {
		rec := httptest.NewRecorder()
		o.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader("12345")))
		res := rec.Result()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	res, b := get(&Origin{}, "/proxy/echo?resp=100")
	if res.StatusCode != 200 || !bytes.Equal(b, Filler(100, "")) || res.Header.Get("X-Origin-Req-Len") != "5" {
		t.Errorf("clean origin %d %d %v", res.StatusCode, len(b), res.Header)
	}
	if _, b := get(&Origin{}, "/proxy/echo?resp=100&canary=waf"); !bytes.Contains(b, []byte(wafRespMark)) {
		t.Error("waf response canary missing")
	}
	if _, b := get(&Origin{Fault: "corrupt"}, "/proxy/echo?resp=100"); bytes.Equal(b, Filler(100, "")) || len(b) != 100 {
		t.Error("corrupt fault did not corrupt")
	}
	if _, b := get(&Origin{Fault: "truncate"}, "/proxy/echo?resp=100"); len(b) >= 100 {
		t.Errorf("truncate fault wrote %d bytes", len(b))
	}
}

func TestMetricsParsing(t *testing.T) {
	m, err := ParseExposition(strings.NewReader("# HELP x\nx_total{a=\"1\",outcome=\"success\"} 3\nx_total{outcome=\"failure\"} 2\nbad\ny 1e3\n"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := Sum(m, "x_total"); !ok || v != 5 {
		t.Errorf("sum %v %v", v, ok)
	}
	if v, _ := Sum(m, "x_total", `outcome="success"`); v != 3 {
		t.Errorf("filtered %v", v)
	}
	if _, ok := Sum(m, "absent"); ok {
		t.Error("absent metric reported available")
	}
	d, missing := deltas(map[string]float64{}, m)
	if len(missing) == 0 || len(d) != len(CounterMetrics)-len(missing) {
		t.Errorf("unavailable metrics must be listed, not zeroed: %v %v", d, missing)
	}
	if f := Foundation(map[string]float64{"jul_inspection_x": 1, "go_y": 2}); len(f) != 1 {
		t.Errorf("foundation %v", f)
	}
}

// TestCounterFiltersMatchJulLabels guards against a filter on a label value
// Jul never emits, which would read as a false zero. Jul exposes a labelled
// series only after observing it, so each value is checked against the
// enumeration in the metric's Help text instead of a scrape.
func TestCounterFiltersMatchJulLabels(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "internal", "observability", "metrics.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, c := range CounterMetrics {
		i := strings.Index(text, `Name: "`+c.Metric+`"`)
		if i < 0 {
			t.Errorf("%s: metric %s not defined by Jul", c.Key, c.Metric)
			continue
		}
		help := text[i:]
		help = help[:strings.Index(help, "\n\t\t}")]
		for _, f := range c.Filters {
			label, value, _ := strings.Cut(f, "=")
			value = strings.Trim(value, `"`)
			if label == "pool" { // the harness names this upstream in its own configuration
				continue
			}
			if !regexp.MustCompile(`[(/]` + regexp.QuoteMeta(value) + `[/)]`).MatchString(help) {
				t.Errorf("%s: %s is not a documented %s value", c.Key, f, c.Metric)
			}
		}
	}
}

// fakeSys builds /proc and cgroup trees for VerifyEnvelope.
func fakeSys(t *testing.T, mem, swap, cpu, cpus, gomax string) Sys {
	t.Helper()
	root := t.TempDir()
	s := Sys{Proc: filepath.Join(root, "proc"), Cgroup: filepath.Join(root, "cg")}
	cg := filepath.Join(s.Cgroup, "user.slice", "app.slice", "jul.scope")
	write := func(p, v string) {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(v), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(s.Proc, "42", "cgroup"), "0::/user.slice/app.slice/jul.scope\n")
	write(filepath.Join(s.Proc, "42", "status"), "Name:\tjul\nCpus_allowed_list:\t"+cpus+"\nVmRSS:\t 2048 kB\n")
	write(filepath.Join(s.Proc, "42", "environ"), "A=1\x00GOMAXPROCS="+gomax+"\x00")
	write(filepath.Join(cg, "memory.max"), mem+"\n")
	write(filepath.Join(cg, "memory.swap.max"), swap+"\n")
	write(filepath.Join(cg, "cpu.max"), cpu+"\n")
	write(filepath.Join(s.Cgroup, "user.slice", "app.slice", "memory.events"), "low 0\noom_kill 3\n")
	return s
}

func TestVerifyEnvelopeChecksEffectiveLimits(t *testing.T) {
	e := fakeSys(t, "805306368", "0", "200000 100000", "0-1", "2").VerifyEnvelope(42, DefaultEnvelope)
	if !e.Verified || e.CPUQuotaCPUs != 2 || *e.ParentOOMKill != 3 {
		t.Fatalf("%+v", e)
	}
	e = fakeSys(t, "max", "max", "max 100000", "0-11", "").VerifyEnvelope(42, DefaultEnvelope)
	if e.Verified || len(e.Problems) != 5 {
		t.Errorf("unconstrained process verified: %+v", e.Problems)
	}
	e = fakeSys(t, "805306368", "0", "200000 100000", "0-1", "2").VerifyEnvelope(42, Envelope{Mode: "none"})
	if e.Verified {
		t.Error("an unrequested envelope must never be reported verified")
	}
	if e := (Sys{Proc: t.TempDir()}).VerifyEnvelope(7, DefaultEnvelope); e.Verified || e.MemoryMax != nil {
		t.Errorf("missing process verified: %+v", e)
	}
	for in, want := range map[string]string{"0,1": "0-1", "0-1": "0-1", "0,2,3,4": "0,2-4", "5": "5"} {
		if got := expandCPUs(in); got != want {
			t.Errorf("expandCPUs(%q)=%q", in, got)
		}
	}
	for in, want := range map[string]int64{"768M": 768 << 20, "1G": 1 << 30, "4K": 4096, "10": 10} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q)=%d %v", in, got, err)
		}
	}
	if fmtp(nil) != "unavailable" {
		t.Error("nil must be unavailable, not zero")
	}
}

func TestLaunchAppliesEnvelope(t *testing.T) {
	c := Launch(DefaultEnvelope, "u1", "/bin/jul", "/c.toml", "/tmp/x")
	got := strings.Join(c.Args, " ")
	for _, want := range []string{"systemd-run --user --scope", "--unit=u1", "CPUQuota=200%", "MemoryMax=768M", "MemorySwapMax=0", "GOMAXPROCS=2", "taskset -c 0,1", "/bin/jul serve -config /c.toml"} {
		if !strings.Contains(got, want) {
			t.Errorf("launch %q lacks %q", got, want)
		}
	}
	if got := strings.Join(Launch(Envelope{Mode: "none", GOMAXPROCS: 2}, "u", "j", "c", "t").Args, " "); strings.Contains(got, "systemd-run") || strings.Contains(got, "taskset") {
		t.Errorf("unconstrained launch %q", got)
	}
	if err := EnvelopeSupported(Sys{Cgroup: t.TempDir()}); err == nil {
		t.Error("missing cgroup v2 hierarchy accepted")
	}
}

func TestPlanSelectionAndFrozenFile(t *testing.T) {
	if _, err := Select("nope", "", "u1-normal-h1", ""); err == nil {
		t.Error("unknown profile")
	}
	if _, err := Select("quick", "nope", "", ""); err == nil {
		t.Error("unknown suite")
	}
	if _, err := Select("quick", "", "", ""); err == nil {
		t.Error("empty selection")
	}
	if _, err := Select("quick", "", "s9-reload-churn", "none"); err == nil {
		t.Error("variant outside the scenario accepted")
	}
	if _, err := Select("quick", "", "nope", ""); err == nil {
		t.Error("unknown scenario")
	}
	cells, err := Select("qualification", "campaign-normal", "", "")
	if err != nil || len(cells) != 10 {
		t.Fatalf("%v %d", err, len(cells))
	}
	for _, c := range cells {
		if c.Scenario.Comparison != "throughput" || c.Scenario.Reps < Rules.MinRepetitions {
			t.Errorf("%s: %+v", c.Scenario.ID, c.Scenario)
		}
		if got := repsFor(RunOptions{Profile: "qualification", Reps: 1}, c.Scenario); got < 3 {
			t.Errorf("qualification lowered repetitions to %d", got)
		}
	}
	if repsFor(RunOptions{Profile: "quick", Reps: 1}, cells[0].Scenario) != 1 || repsFor(RunOptions{Profile: "qualification", Reps: 5}, cells[0].Scenario) != 5 {
		t.Error("repsFor")
	}
	// Variant resolution changes the hash; profile scaling changes the windows.
	a, _ := Select("quick", "", "u1-normal-h1", "waf")
	b, _ := Select("quick", "", "u1-normal-h1", "none")
	if ScenarioHash(a[0].Scenario) == ScenarioHash(b[0].Scenario) {
		t.Error("variant concurrency not part of the scenario identity")
	}
	frozen, err := os.ReadFile("plan.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frozen, PlanJSON()) {
		t.Error("plan.json differs from the generated plan; a plan change must regenerate and review plan.json")
	}
	var out bytes.Buffer
	if code := Main([]string{"plan", "-check", "plan.json"}, &out, io.Discard); code != 0 {
		t.Errorf("plan -check: %s", out.String())
	}
	bad := filepath.Join(t.TempDir(), "p.json")
	_ = os.WriteFile(bad, []byte("{}"), 0o600)
	if code := Main([]string{"plan", "-check", bad}, io.Discard, io.Discard); code == 0 {
		t.Error("altered plan accepted")
	}
}

func TestPlanPreservesArchivedWorkloads(t *testing.T) {
	byID := map[string]Scenario{}
	for _, s := range BuildScenarios(Profiles["qualification"]) {
		byID[s.ID] = s
	}
	w := func(id string, phase, i int) Workload { return byID[id].Phases[phase].Workloads[i] }
	if r1 := w("r1-slow-upload-c16-h1", 0, 0); r1.SlowBPS == 0 || r1.Concurrency != 16 {
		t.Errorf("r1 is the slow-upload case: %+v", r1)
	}
	if r2 := w("r2-fast-nobody-h1", 0, 0); r2.BodyBytes != 0 || r2.SlowBPS != 0 {
		t.Errorf("r2 has no body: %+v", r2)
	}
	if r3 := w("r3-fast-body-h1", 0, 0); r3.BodyBytes == 0 || r3.SlowBPS != 0 {
		t.Errorf("r3 is the fast-body case: %+v", r3)
	}
	for _, id := range []string{"s11-arg-amplification-h1", "s11l-arg-amplification-low-h1"} {
		if a := w(id, 0, 0); a.BodyShape != "items" || a.Concurrency != 1 {
			t.Errorf("%s keeps the many-argument shape: %+v", id, a)
		}
	}
	for id, pending := range map[string]int{"f3-admission-pending0": 0, "f3-admission-pending16": 16} {
		s := byID[id]
		if s.MaxPending == nil || *s.MaxPending != pending || w(id, 0, 1).Connections != 1 || w(id, 0, 0).Connections != 1 {
			t.Errorf("%s: %+v", id, s)
		}
	}
	if f := byID["f4-stalled-body"].F4; f == nil || !slices.Equal(f.Protos, []string{"h1", "h2", "h3"}) || !slices.Equal(f.Modes, []string{"stall", "slow"}) {
		t.Errorf("f4 %+v", f)
	}
	if !byID["s7-cache-h1"].Cache || len(byID["s7-cache-h1"].Phases) != 2 || !strings.HasPrefix(byID["s6-static-h2"].Phases[0].Workloads[0].Path, "/static/page.html") {
		t.Error("cache and static scenarios lost their paths")
	}
	if s9 := byID["s9-reload-churn"]; s9.Kind != "reload" || !slices.Contains(s9.Phases[0].Workloads[1].Expect, "error:stream-reset") {
		t.Errorf("s9 %+v", s9)
	}
}

func TestRenderConfigAdapters(t *testing.T) {
	p := ConfigParams{Adapter: Adapters["current"], WAF: true, WASM: true, Cache: true, Port: 1, TLSPort: 2, AdminPort: 3, Origin: "127.0.0.1:4",
		WWW: "/w", Plugin: "/p.wasm", Cert: "/c", Key: "/k", MaxPending: 0, ReadTimeout: "5s"}
	cur, err := RenderConfig(p)
	if err != nil {
		t.Fatal(err)
	}
	p.Adapter = Adapters["baseline-858590dc"]
	base, _ := RenderConfig(p)
	for _, want := range []string{"crs_enabled = true", "response_body_check = true", `request_body_limit = "1mb"`, "inspect-b", "max_pending_requests = 0", `read_timeout = "5s"`, "[cache]"} {
		if !bytes.Contains(cur, []byte(want)) || !bytes.Contains(base, []byte(want)) {
			t.Errorf("security/workload setting %q missing", want)
		}
	}
	if !bytes.Contains(cur, []byte(`max_buffered_bytes = "512m"`)) || bytes.Contains(base, []byte("[global.inspection]")) {
		t.Error("adapter difference wrong")
	}
	p.Adapter, p.WAF = Adapters["current"], false
	if nowaf, _ := RenderConfig(p); bytes.Contains(nowaf, []byte("[global.inspection]")) {
		t.Error("non-WAF variants keep the 256 MiB default")
	}
	p.BrokenWAF, p.WAF = true, true
	if broken, _ := RenderConfig(p); !bytes.Contains(broken, []byte("THIS_IS_NOT_A_VARIABLE")) {
		t.Error("reload 5 must carry the invalid directive")
	}
	if _, err := RenderConfig(ConfigParams{}); err == nil {
		t.Error("missing adapter accepted")
	}
	if _, err := ParseTarget("a:nope:/x"); err == nil {
		t.Error("unknown adapter accepted")
	}
	if _, err := ParseTarget("a/b:current:/x"); err == nil {
		t.Error("label with slash accepted")
	}
}

// writeRun makes a minimal finished run directory for loader tests.
func writeRun(t *testing.T, mut func(*Manifest, *RunResult)) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "run")
	_ = os.MkdirAll(filepath.Join(dir, "cells"), 0o700)
	cells, _ := Select("quick", "", "cal-normal-h1", "none")
	sc := cells[0].Scenario
	plan := PlanJSON()
	_ = os.WriteFile(filepath.Join(dir, "plan.json"), plan, 0o600)
	now := time.Now().UTC()
	cpu := 0.5
	sat := false
	r := RunResult{Scenario: sc.ID, ScenarioHash: ScenarioHash(sc), Comparison: sc.Comparison, Variant: "none", Target: "base", Rep: 1, State: "completed", Valid: true, Correct: true}
	for i, c := range []int{1, 2, 4} {
		frac := cpu + float64(i)*0.15
		s := sat
		r.Phases = append(r.Phases, PhaseResult{Name: "c", CPUFraction: &frac, Saturated: &s,
			Classes: []ClassStats{{Concurrency: c, Attempts: 10, GoodN: 10, GoodputRPS: float64(10 * c), Classes: map[string]int{"ok": 10}}}})
	}
	m := Manifest{Schema: ManifestSchema, RunID: "run", Started: now, Ended: &now, State: "complete", PlanVersion: PlanVersion, PlanSHA: sha256Hex(plan),
		RulesSHA: "rules", Profile: Profiles["quick"], ScenarioSHAs: map[string]string{sc.ID + "/none": r.ScenarioHash},
		Targets: []TargetIdentity{{TargetSpec: TargetSpec{Label: "base", Adapter: "baseline-858590dc"}, Binary: BinaryIdentity{SHA256: "b"}}},
		Results: []string{"cells/result.json"}}
	if mut != nil {
		mut(&m, &r)
	}
	_ = writeJSON(filepath.Join(dir, "cells", "result.json"), r)
	_ = writeJSON(filepath.Join(dir, "manifest.json"), m)
	if err := WriteSums(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadRunsRejectsIncompatibleRuns(t *testing.T) {
	good := writeRun(t, nil)
	if _, err := LoadRuns([]string{good, writeRun(t, nil)}); err != nil {
		t.Fatalf("compatible runs rejected: %v", err)
	}
	for name, mut := range map[string]func(*Manifest, *RunResult){
		"plan":     func(m *Manifest, _ *RunResult) { m.RulesSHA = "other" },
		"profile":  func(m *Manifest, _ *RunResult) { m.Profile.Name = "qualification" },
		"envelope": func(m *Manifest, _ *RunResult) { m.Envelope.Mode = "required" },
		"fault":    func(m *Manifest, _ *RunResult) { m.Fault = "truncate" },
		"binary":   func(m *Manifest, _ *RunResult) { m.Targets[0].Binary.SHA256 = "other" },
		"scenario": func(m *Manifest, _ *RunResult) { m.ScenarioSHAs["cal-normal-h1/none"] = "other" },
		"guest":    func(m *Manifest, _ *RunResult) { m.Guest = &Artifact{SHA256: "other"} },
	} {
		other := writeRun(t, mut)
		first := good
		if name == "guest" {
			first = writeRun(t, func(m *Manifest, _ *RunResult) { m.Guest = &Artifact{SHA256: "g"} })
		}
		if _, err := LoadRuns([]string{first, other}); err == nil {
			t.Errorf("%s: incompatible run merged", name)
		}
	}
	for name, mut := range map[string]func(*Manifest, *RunResult){
		"unfinished":    func(m *Manifest, _ *RunResult) { m.State, m.Ended = "running", nil },
		"schema":        func(m *Manifest, _ *RunResult) { m.Schema = "x" },
		"result hash":   func(_ *Manifest, r *RunResult) { r.ScenarioHash = "x" },
		"plan identity": func(m *Manifest, _ *RunResult) { m.PlanSHA = "x" },
		"no targets":    func(m *Manifest, _ *RunResult) { m.Targets = nil },
	} {
		if err := VerifyRun(writeRun(t, mut)); err == nil {
			t.Errorf("%s: verified", name)
		}
	}
	if err := VerifyRun(t.TempDir()); err == nil {
		t.Error("directory without sums verified")
	}
	bad := writeRun(t, nil)
	_ = os.WriteFile(filepath.Join(bad, SumsFile), []byte("zz  manifest.json\n"), 0o600)
	if err := VerifyRun(bad); err == nil {
		t.Error("malformed sums verified")
	}
}

func TestCalibrateAndCommands(t *testing.T) {
	dir := writeRun(t, nil)
	runs, err := LoadRuns([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	cal := Calibrate(runs, "base")
	// CPU fractions 0.5, 0.65, 0.8: C=2 is the largest at or below 0.70.
	if len(cal) != 1 || cal[0].Chosen != 2 || len(cal[0].Steps) != 3 {
		t.Fatalf("%+v", cal)
	}
	var out bytes.Buffer
	if Main([]string{"calibrate", "-target", "base", dir}, &out, io.Discard) != 0 || !strings.Contains(out.String(), `"chosen_concurrency": 2`) {
		t.Errorf("calibrate: %s", out.String())
	}
	if Main([]string{"calibrate", "-target", "nope", dir}, io.Discard, io.Discard) == 0 {
		t.Error("calibrate without results succeeded")
	}
	if Main([]string{"verify", dir}, io.Discard, io.Discard) != 0 || Main([]string{"verify"}, io.Discard, io.Discard) == 0 {
		t.Error("verify command")
	}
	rep := filepath.Join(t.TempDir(), "report")
	if Main([]string{"summarize", "-out", rep, "-compare", "base:main", dir}, io.Discard, io.Discard) == 0 {
		t.Error("-compare accepted quick-profile runs without an envelope")
	}
	eligible := writeRun(t, func(m *Manifest, _ *RunResult) {
		m.Profile, m.Envelope = Profiles["qualification"], DefaultEnvelope
	})
	if Main([]string{"summarize", "-out", rep, "-compare", "base:main", eligible}, io.Discard, io.Discard) != 0 {
		t.Fatal("summarize")
	}
	md, _ := os.ReadFile(filepath.Join(rep, "summary.md"))
	out.Reset()
	if Main([]string{"summarize", eligible}, &out, io.Discard) != 0 || out.String() != string(md) {
		t.Error("summary markdown is not reproducible")
	}
	if Main([]string{"summarize", "-compare", "nocolon", eligible}, io.Discard, io.Discard) == 0 {
		t.Error("bad -compare accepted")
	}
	fx := t.TempDir()
	out.Reset()
	if Main([]string{"fixtures", "-out", fx, "-guest", filepath.Join(dir, "plan.json"), "-guest-source", "../../examples/plugins/inspect-load"}, &out, io.Discard) != 0 {
		t.Fatal("fixtures")
	}
	if page, _ := os.ReadFile(filepath.Join(fx, "www", "static", "page.html")); !bytes.Equal(page, StaticPage()) || strings.Count(out.String(), "sha256") != 3 {
		t.Errorf("fixtures output %q", out.String())
	}
	for _, args := range [][]string{{}, {"nope"}, {"origin", "-fault", "nope"}, {"fixtures", "-guest", "/nonexistent"}} {
		if Main(args, io.Discard, io.Discard) == 0 {
			t.Errorf("%v succeeded", args)
		}
	}
}

func TestOriginRoutes(t *testing.T) {
	o := &Origin{}
	srv := httptest.NewServer(o)
	defer srv.Close()
	get := func(path string) (*http.Response, []byte) {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res, b
	}
	if res, b := get("/cached/x?resp=200000&chunked=1"); len(b) != 200000 || res.Header.Get("Cache-Control") != "public, max-age=300" {
		t.Errorf("chunked cached response %d %v", len(b), res.Header)
	}
	if res, b := get("/proxy/x?delay=1"); len(b) != 1024 || res.Header.Get("Cache-Control") != "no-store" {
		t.Errorf("default response %d", len(b))
	}
	if _, b := get("/__count"); string(b) != "2" {
		t.Errorf("count %q", b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	stalled := httptest.NewServer(&Origin{Fault: "stall"})
	defer stalled.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, stalled.URL+"/proxy/x", nil)
	if res, err := http.DefaultClient.Do(req); err == nil {
		res.Body.Close()
		t.Error("stall fault answered")
	}
}

func TestMainRunWritesVerifiableEvidence(t *testing.T) {
	self, _ := os.Executable()
	out := filepath.Join(t.TempDir(), "run")
	var stdout bytes.Buffer
	code := Main([]string{"run", "-out", out, "-envelope", "none", "-origin-cpus", "", "-scenario", "u10-normal-h3", "-variants", "none", "-reps", "1",
		"-cert", "../../testdata/tls/localhost.crt", "-key", "../../testdata/tls/localhost.key", "-target", "main:current:" + self}, &stdout, io.Discard)
	if code != 0 || !strings.Contains(stdout.String(), "complete, 1 results") {
		t.Fatalf("run exited %d: %s", code, stdout.String())
	}
	if Main([]string{"verify", out}, io.Discard, io.Discard) != 0 {
		t.Error("run evidence does not verify")
	}
}

func TestSamplerReadsCgroupAndProcess(t *testing.T) {
	s := fakeSys(t, "805306368", "0", "200000 100000", "0-1", "2")
	cg, _ := s.CgroupOf(42)
	for name, v := range map[string]string{"memory.current": "1000", "memory.peak": "2000", "memory.stat": "anon 10\nfile 20\nshmem 0\n",
		"cpu.stat": "usage_usec 77\n", "memory.events": "oom_kill 0\n"} {
		_ = os.WriteFile(filepath.Join(cg, name), []byte(v), 0o600)
	}
	_ = os.MkdirAll(filepath.Join(s.Proc, "42", "fd"), 0o700)
	tmp := t.TempDir()
	_ = os.WriteFile(filepath.Join(tmp, "body-1"), []byte("abc"), 0o600)
	j := &Jul{PID: 42, sys: s, Env: DefaultEnvelope, Tmp: tmp, exited: make(chan struct{}), Params: ConfigParams{AdminPort: 1}}
	sm := (&Sampler{j: j, start: time.Now(), phase: "load"}).Take()
	if *sm.CgMem != 1000 || *sm.CgPeak != 2000 || *sm.CPUUsec != 77 || *sm.OOMKill != 0 || *sm.RSS != 2048*1024 || sm.SpillFiles != 1 || sm.SpillBytes != 3 || *sm.FDs != 0 {
		t.Errorf("%+v", sm)
	}
	if sm.Threads != nil || sm.ScrapeErr == "" || sm.Metrics != nil {
		t.Error("unavailable values must stay unavailable")
	}
	j.Env = Envelope{Mode: "none"}
	if sm := (&Sampler{j: j, start: time.Now()}).Take(); sm.CgMem != nil {
		t.Error("an unconstrained run must not report cgroup memory")
	}
	if kib("x") != nil || kib("") != nil || num("x") != nil {
		t.Error("malformed values parsed")
	}
}
