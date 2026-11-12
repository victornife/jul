// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testCell returns a planned quick-profile cell with its windows shortened
// so the end-to-end tests stay fast; the scenario shape is otherwise intact.
func testCell(t *testing.T, id, variant string) Cell {
	t.Helper()
	cells, err := Select("quick", "", id, variant)
	if err != nil || len(cells) != 1 {
		t.Fatalf("select %s/%s: %v %d", id, variant, err, len(cells))
	}
	c := cells[0]
	sc := c.Scenario
	sc.Recovery = Dur(200 * time.Millisecond)
	phases := make([]Phase, len(sc.Phases))
	for i, ph := range sc.Phases {
		ph.Warmup, ph.Measure = Dur(300*time.Millisecond), Dur(time.Second)
		ws := make([]Workload, len(ph.Workloads))
		for k, w := range ph.Workloads {
			w.Concurrency = min(w.Concurrency, 2)
			if w.Connections > 0 {
				w.Connections = 1
			}
			ws[k] = w
		}
		ph.Workloads = ws
		phases[i] = ph
	}
	sc.Phases = phases
	if sc.F4 != nil {
		f := *sc.F4
		f.ClientLimit, f.SiblingAfter, f.SlowInterval = Dur(time.Second), Dur(200*time.Millisecond), Dur(100*time.Millisecond)
		f.Protos = []string{"h2"}
		sc.F4 = &f
	}
	c.Scenario = sc
	return c
}

func testOptions(t *testing.T, cells ...Cell) RunOptions {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	guest := filepath.Join(t.TempDir(), "guest.wasm")
	if err := os.WriteFile(guest, []byte("\x00asm-fake"), 0o600); err != nil {
		t.Fatal(err)
	}
	cert, _ := filepath.Abs("../../testdata/tls/localhost.crt")
	key, _ := filepath.Abs("../../testdata/tls/localhost.key")
	return RunOptions{
		Out: filepath.Join(t.TempDir(), "run"), Profile: "quick",
		Targets: []TargetSpec{{Label: "base", Adapter: "baseline-858590dc", Path: self}, {Label: "main", Adapter: "current", Path: self}},
		Cells:   cells, Reps: 1, Env: Envelope{Mode: "none", GOMAXPROCS: 2},
		Guest: guest, Cert: cert, Key: key, Startup: 10 * time.Second, StopGrace: 2 * time.Second,
		SampleEvery: 100 * time.Millisecond, Sys: hostSys, Self: self,
	}
}

func readResult(t *testing.T, dir, rel string) RunResult {
	t.Helper()
	var r RunResult
	if err := readJSON(filepath.Join(dir, rel), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExecuteEndToEndVerifiesAndSummarises(t *testing.T) {
	opt := testOptions(t, testCell(t, "u1-normal-h1", "none"), testCell(t, "u1-normal-h1", "both"),
		testCell(t, "u2-normal-h2", "wasm"), testCell(t, "u10-normal-h3", "none"), testCell(t, "f4-stalled-body", "none"))
	var log bytes.Buffer
	m, err := Execute(context.Background(), opt, "../../examples/plugins/inspect-load", &log)
	if err != nil {
		t.Fatal(err)
	}
	if m.State != "complete" || len(m.Results) != 10 {
		t.Fatalf("state %s results %d\n%s", m.State, len(m.Results), log.String())
	}
	for _, rel := range m.Results {
		r := readResult(t, opt.Out, rel)
		if !r.Valid || !r.Correct || r.State != "completed" {
			t.Errorf("%s: valid=%v correct=%v state=%s invalid=%v findings=%v failure=%s", rel, r.Valid, r.Correct, r.State, r.Invalid, r.Findings, r.Failure)
		}
		for _, ph := range r.Phases {
			for _, c := range ph.Classes {
				if c.GoodN == 0 || len(c.Protos) != 1 {
					t.Errorf("%s: class %s had no verified %s goodput: %+v", rel, c.Workload, c.Proto, c)
				}
			}
		}
		if r.Scenario == "f4-stalled-body" && (len(r.F4) != 2 || !r.F4[0].WatchdogFired) {
			t.Errorf("f4 result: %+v", r.F4)
		}
	}
	if m.Guest == nil || m.Guest.SourceSHA == "" || m.Targets[0].Adapter.Name != "baseline-858590dc" {
		t.Errorf("manifest provenance incomplete: %+v", m)
	}
	cfg, _ := os.ReadFile(filepath.Join(opt.Out, "cells/u1-normal-h1/both/main/rep1/config.toml"))
	baseCfg, _ := os.ReadFile(filepath.Join(opt.Out, "cells/u1-normal-h1/both/base/rep1/config.toml"))
	if !bytes.Contains(cfg, []byte("[global.inspection]")) || bytes.Contains(baseCfg, []byte("[global.inspection]")) {
		t.Error("adapters did not keep the budget to the current target only")
	}
	if err := VerifyRun(opt.Out); err != nil {
		t.Fatal(err)
	}
	runs, err := LoadRuns([]string{opt.Out})
	if err != nil {
		t.Fatal(err)
	}
	s := BuildSummary(runs, [][2]string{{"base", "main"}})
	if len(s.Comparisons) != 4 {
		t.Errorf("comparisons: %+v", s.Comparisons)
	}
	wantRows, wantProbes := 0, 0
	for _, r := range runs[0].Results {
		for _, ph := range r.Phases {
			wantRows += len(ph.Classes)
		}
		wantProbes += len(r.F4)
	}
	if len(s.Classes) != wantRows || len(s.Probes) != wantProbes || wantProbes == 0 {
		t.Errorf("summary rows: %d classes (want %d), %d probes (want %d)", len(s.Classes), wantRows, len(s.Probes), wantProbes)
	}
	if md := s.Markdown(); !strings.Contains(md, "## Phases and classes") || !strings.Contains(md, "## F4 probes") {
		t.Error("summary omits class or probe detail")
	}
	for _, c := range s.Comparisons {
		if c.Verdict != VerdictInsufficient {
			t.Errorf("one repetition must be insufficient, got %s", c.Verdict)
		}
	}
	again := BuildSummary(runs, [][2]string{{"base", "main"}})
	if s.Markdown() != again.Markdown() || !bytes.Equal(s.JSON(), again.JSON()) {
		t.Error("summary is not deterministic")
	}

	// Tampering, deletion and additions are all rejected.
	res := filepath.Join(opt.Out, m.Results[0])
	orig, _ := os.ReadFile(res)
	_ = os.WriteFile(res, append(orig, ' '), 0o600)
	if err := VerifyRun(opt.Out); err == nil || !strings.Contains(err.Error(), "modified") {
		t.Errorf("modified result accepted: %v", err)
	}
	_ = os.WriteFile(res, orig, 0o600)
	_ = os.WriteFile(filepath.Join(opt.Out, "extra.txt"), nil, 0o600)
	if err := VerifyRun(opt.Out); err == nil || !strings.Contains(err.Error(), "unlisted") {
		t.Errorf("extra file accepted: %v", err)
	}
	_ = os.Remove(filepath.Join(opt.Out, "extra.txt"))
	_ = os.Remove(res)
	if err := VerifyRun(opt.Out); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Errorf("missing result accepted: %v", err)
	}
}

func TestExecuteControlledFaultsAreNeverGoodput(t *testing.T) {
	for _, fault := range []string{"truncate", "corrupt"} {
		t.Run(fault, func(t *testing.T) {
			opt := testOptions(t, testCell(t, "u1-normal-h1", "none"))
			opt.Targets, opt.Fault = opt.Targets[:1], fault
			m, err := Execute(context.Background(), opt, "", &bytes.Buffer{})
			if err != nil || m.State != "complete" {
				t.Fatalf("%v %s", err, m.State)
			}
			r := readResult(t, opt.Out, m.Results[0])
			if r.Correct || r.Phases[0].Classes[0].GoodN != 0 || r.Phases[0].Classes[0].GoodputRPS != 0 {
				t.Errorf("faulty responses counted as good: %+v", r.Phases[0].Classes[0])
			}
			if _, err := LoadRuns([]string{opt.Out}); err == nil {
				t.Error("controlled-fault run accepted as measurement")
			}
		})
	}
}

func TestExecuteJulFailures(t *testing.T) {
	cases := []struct {
		mode, wantState, wantFinding string
		kill                         time.Duration
		valid                        bool
	}{
		{mode: "exit-at-startup", wantState: "failed"},
		{mode: "never-ready", wantState: "failed"},
		{mode: "check-fails", wantState: "failed"},
		{mode: "crash-after-1s", wantState: "completed", wantFinding: "jul exited during the run", valid: true},
		{mode: "", kill: 600 * time.Millisecond, wantState: "completed", wantFinding: "jul exited during the run", valid: true},
		{mode: "ignore-term", wantState: "completed", valid: true},
		{mode: "http500", wantState: "completed", wantFinding: "canary clean-proxy", valid: true},
	}
	for _, tc := range cases {
		t.Run(tc.mode+tc.kill.String(), func(t *testing.T) {
			t.Setenv("FAKE_JUL_MODE", tc.mode)
			opt := testOptions(t, testCell(t, "u1-normal-h1", "none"))
			opt.Targets, opt.KillJulAfter, opt.Startup = opt.Targets[:1], tc.kill, 2*time.Second
			start := time.Now()
			m, err := Execute(context.Background(), opt, "", &bytes.Buffer{})
			if err != nil || m.State != "complete" {
				t.Fatalf("%v %s", err, m.State)
			}
			if time.Since(start) > 20*time.Second {
				t.Errorf("cell not bounded: %s", time.Since(start))
			}
			r := readResult(t, opt.Out, m.Results[0])
			if r.State != tc.wantState || r.Valid != tc.valid {
				t.Fatalf("state=%s valid=%v failure=%s invalid=%v", r.State, r.Valid, r.Failure, r.Invalid)
			}
			if tc.wantFinding != "" && (r.Correct || !strings.Contains(strings.Join(r.Findings, "|"), tc.wantFinding)) {
				t.Errorf("findings %v, want %q", r.Findings, tc.wantFinding)
			}
			if tc.mode == "ignore-term" && !strings.Contains(strings.Join(r.Notes, "|"), "killed") {
				t.Errorf("ignored SIGTERM was not escalated: %v", r.Notes)
			}
			if err := VerifyRun(opt.Out); err != nil {
				t.Errorf("failed-run evidence does not verify: %v", err)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(filepath.Join(opt.Out, m.Results[0])), "jul-output.txt")); err != nil && tc.mode != "check-fails" {
				t.Errorf("jul output not preserved: %v", err)
			}
		})
	}
}

func TestExecuteInterruptedAndWatchdog(t *testing.T) {
	opt := testOptions(t, testCell(t, "u1-normal-h1", "none"), testCell(t, "u1-normal-h1", "both"))
	opt.Targets = opt.Targets[:1]
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(800*time.Millisecond, cancel)
	m, _ := Execute(ctx, opt, "", &bytes.Buffer{})
	if m.State != "interrupted" || !m.Interrupted {
		t.Errorf("state %s", m.State)
	}
	if err := VerifyRun(opt.Out); err != nil {
		t.Errorf("interrupted evidence does not verify: %v", err)
	}
	if _, err := LoadRuns([]string{opt.Out}); err != nil {
		t.Errorf("interrupted runs remain loadable evidence: %v", err)
	}

	opt = testOptions(t, testCell(t, "u1-normal-h1", "none"))
	opt.Targets, opt.Watchdog = opt.Targets[:1], 500*time.Millisecond
	m, err := Execute(context.Background(), opt, "", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	r := readResult(t, opt.Out, m.Results[0])
	if r.Valid || !strings.Contains(strings.Join(r.Invalid, "|"), "watchdog") {
		t.Errorf("watchdog not recorded: %+v", r.Invalid)
	}
}

func TestRunCmdRefusals(t *testing.T) {
	self, _ := os.Executable()
	tgt := "base:baseline-858590dc:" + self
	for _, args := range [][]string{
		{"-target", tgt},
		{"-out", t.TempDir(), "-target", tgt, "-scenario", "u1-normal-h1"},
		{"-out", filepath.Join(t.TempDir(), "x"), "-target", tgt, "-profile", "qualification", "-envelope", "none", "-scenario", "u1-normal-h1"},
		{"-out", filepath.Join(t.TempDir(), "x"), "-target", tgt, "-envelope", "bogus", "-scenario", "u1-normal-h1"},
		{"-out", filepath.Join(t.TempDir(), "x"), "-target", tgt, "-envelope", "none", "-scenario", "u1-normal-h1", "-variants", "wasm"},
		{"-out", filepath.Join(t.TempDir(), "x"), "-target", "bad", "-envelope", "none", "-scenario", "u1-normal-h1"},
		{"-out", filepath.Join(t.TempDir(), "x"), "-target", tgt, "-envelope", "none", "-profile", "nope"},
	} {
		if code := Main(append([]string{"run"}, args...), &bytes.Buffer{}, &bytes.Buffer{}); code == 0 {
			t.Errorf("run %v accepted", args)
		}
	}
}

func TestExecuteReloadChurnRecordsEveryReload(t *testing.T) {
	c := testCell(t, "s9-reload-churn", "both")
	c.Scenario.Phases[0].Measure = Dur(4500 * time.Millisecond)
	opt := testOptions(t, c)
	opt.Targets = opt.Targets[1:]
	m, err := Execute(context.Background(), opt, "", &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	r := readResult(t, opt.Out, m.Results[0])
	if len(r.Reloads) < 2 || strings.Contains(strings.Join(r.Reloads, "|"), "error=") {
		t.Errorf("reloads %v", r.Reloads)
	}
	if err := VerifyRun(opt.Out); err != nil {
		t.Error(err)
	}
}
