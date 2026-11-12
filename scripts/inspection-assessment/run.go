// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TargetSpec is one measured binary.
type TargetSpec struct {
	Label   string `json:"label"`
	Adapter string `json:"adapter"`
	Path    string `json:"path"`
}

// ParseTarget parses label:adapter:path.
func ParseTarget(s string) (TargetSpec, error) {
	parts := strings.SplitN(s, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" {
		return TargetSpec{}, fmt.Errorf("target %q: want label:adapter:path", s)
	}
	if _, ok := Adapters[parts[1]]; !ok {
		return TargetSpec{}, fmt.Errorf("target %q: unknown adapter %q", s, parts[1])
	}
	if strings.ContainsAny(parts[0], "/ ") {
		return TargetSpec{}, fmt.Errorf("target label %q must not contain '/' or spaces", parts[0])
	}
	return TargetSpec{Label: parts[0], Adapter: parts[1], Path: parts[2]}, nil
}

// RunOptions configure one run set.
type RunOptions struct {
	Out, Profile, Fault string
	Targets             []TargetSpec
	Cells               []Cell
	Reps                int
	Env                 Envelope
	Guest, WWW          string
	Cert, Key           string
	OriginCPUs          string
	KillJulAfter        time.Duration
	Watchdog            time.Duration
	Startup             time.Duration
	StopGrace           time.Duration // SIGTERM grace before SIGKILL (default 15s)
	SampleEvery         time.Duration
	Sys                 Sys
	Self                string // the harness executable, re-run as the origin
}

// PhaseResult is one measured window.
type PhaseResult struct {
	Name        string       `json:"name"`
	Classes     []ClassStats `json:"classes"`
	CPUFraction *float64     `json:"jul_cpu_fraction_of_envelope"`
	CPUSource   string       `json:"cpu_source,omitempty"`
	Saturated   *bool        `json:"saturated"`
}

// RunResult is the complete record of one repetition of one cell.
type RunResult struct {
	Scenario     string    `json:"scenario"`
	ScenarioHash string    `json:"scenario_sha256"`
	Comparison   string    `json:"comparison"`
	Variant      string    `json:"variant"`
	Target       string    `json:"target"`
	Rep          int       `json:"rep"`
	Profile      string    `json:"profile"`
	Started      time.Time `json:"started"`
	Ended        time.Time `json:"ended"`
	State        string    `json:"state"` // completed | failed
	Failure      string    `json:"failure,omitempty"`
	// Valid is instrument validity: the run measured what it claims.
	Valid   bool     `json:"valid"`
	Invalid []string `json:"invalid_reasons,omitempty"`
	// Correct reports no runtime finding; Findings are Jul's unexpected
	// outcomes, recorded as evidence.
	Correct        bool               `json:"correct"`
	Findings       []string           `json:"runtime_findings,omitempty"`
	ConfigSHA      string             `json:"config_sha256"`
	CheckOK        bool               `json:"config_check_ok"`
	Envelope       *Effective         `json:"effective_envelope,omitempty"`
	PreCanaries    []Canary           `json:"pre_canaries,omitempty"`
	PostCanaries   []Canary           `json:"post_canaries,omitempty"`
	Phases         []PhaseResult      `json:"phases,omitempty"`
	F4             []F4Result         `json:"f4,omitempty"`
	Reloads        []string           `json:"reloads,omitempty"`
	JulExit        string             `json:"jul_exit"`
	JulExitedEarly bool               `json:"jul_exited_during_run"`
	OOMKillDelta   *int64             `json:"oom_kill_delta"`
	OOMScope       string             `json:"oom_kill_scope,omitempty"`
	PeakCgMem      *int64             `json:"peak_cg_memory_bytes"`
	PeakSource     string             `json:"peak_source,omitempty"`
	PeakRSS        *int64             `json:"peak_rss_bytes"`
	HeapInuseGC    *float64           `json:"heap_inuse_after_two_gcs"`
	HeapAllocGC    *float64           `json:"heap_alloc_after_two_gcs"`
	RSSAfterGC     *int64             `json:"rss_after_two_gcs"`
	CounterDelta   Metrics            `json:"counter_deltas,omitempty"`
	Unavailable    []string           `json:"unavailable_metrics,omitempty"`
	Foundation     map[string]float64 `json:"inspection_foundation_series_after,omitempty"`
	Samples        int                `json:"samples"`
	Notes          []string           `json:"notes,omitempty"`
}

var portMu sync.Mutex

// freePorts reserves n loopback ports by binding TCP and UDP briefly.
func freePorts(n int) ([]int, error) {
	portMu.Lock()
	defer portMu.Unlock()
	var out []int
	for len(out) < n {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		p := l.Addr().(*net.TCPAddr).Port
		u, uerr := net.ListenPacket("udp", fmt.Sprintf("127.0.0.1:%d", p))
		_ = l.Close()
		if uerr != nil {
			continue
		}
		_ = u.Close()
		out = append(out, p)
	}
	return out, nil
}

func indentJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	return append(b, '\n'), err
}

func writeJSON(path string, v any) error {
	b, err := indentJSON(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644) //nolint:gosec // evidence is world-readable
}

// Runner executes cells and writes evidence.
type Runner struct {
	Opt    RunOptions
	Origin string
	keys   map[string]string
	keyDir string
}

func (r *Runner) cellDir(c Cell, t TargetSpec, rep int) string {
	return filepath.Join(r.Opt.Out, "cells", c.Scenario.ID, c.Variant, t.Label, fmt.Sprintf("rep%d", rep))
}

// versionKey creates one effective-version key per target outside the run
// directory so no key material enters the evidence.
func (r *Runner) versionKey(ctx context.Context, t TargetSpec) (string, error) {
	if k, ok := r.keys[t.Label]; ok {
		return k, nil
	}
	k := filepath.Join(r.keyDir, t.Label+".key")
	out, err := exec.CommandContext(ctx, t.Path, "version-key", "init", "--file", k).CombinedOutput() //nolint:gosec // operator-selected binary
	if err != nil {
		return "", fmt.Errorf("version-key init: %v: %s", err, strings.TrimSpace(string(out)))
	}
	r.keys[t.Label] = k
	return k, nil
}

// RunCell executes one repetition and always returns a result.
func (r *Runner) RunCell(ctx context.Context, c Cell, t TargetSpec, rep int) (res RunResult) {
	sc := c.Scenario
	res = RunResult{Scenario: sc.ID, ScenarioHash: ScenarioHash(sc), Comparison: sc.Comparison, Variant: c.Variant, Target: t.Label, Rep: rep,
		Profile: r.Opt.Profile, Started: time.Now().UTC(), State: "failed"}
	dir := r.cellDir(c, t, rep)
	defer func() {
		res.Ended = time.Now().UTC()
		if res.OOMKillDelta != nil && *res.OOMKillDelta > 0 {
			res.Findings = append(res.Findings, fmt.Sprintf("oom_kill delta %d in %s", *res.OOMKillDelta, res.OOMScope))
		}
		res.Valid = res.State == "completed" && len(res.Invalid) == 0
		res.Correct = len(res.Findings) == 0
		_ = writeJSON(filepath.Join(dir, "result.json"), res)
	}()
	fail := func(format string, a ...any) RunResult {
		res.Failure = fmt.Sprintf(format, a...)
		return res
	}
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // evidence directory
		return fail("mkdir: %v", err)
	}
	ports, err := freePorts(3)
	if err != nil {
		return fail("ports: %v", err)
	}
	waf, wasm := VariantFlags(c.Variant)
	pending := 16
	if sc.MaxPending != nil {
		pending = *sc.MaxPending
	}
	p := ConfigParams{Adapter: Adapters[t.Adapter], WAF: waf, WASM: wasm, Cache: sc.Cache, Port: ports[0], TLSPort: ports[1], AdminPort: ports[2],
		Origin: r.Origin, WWW: r.Opt.WWW, Plugin: r.Opt.Guest, Cert: r.Opt.Cert, Key: r.Opt.Key, ReadTimeout: sc.ReadTimeout, MaxPending: pending}
	cfg, err := RenderConfig(p)
	if err != nil {
		return fail("render: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil { //nolint:gosec // synthetic configuration
		return fail("write config: %v", err)
	}
	res.ConfigSHA = sha256Hex(cfg)
	cctx, cancel := context.WithTimeout(ctx, time.Minute)
	out, cerr := exec.CommandContext(cctx, t.Path, "check", "-config", cfgPath).CombinedOutput() //nolint:gosec // operator-selected binary
	cancel()
	_ = os.WriteFile(filepath.Join(dir, "check.txt"), out, 0o644) //nolint:gosec // evidence
	if cerr != nil {
		return fail("jul check rejected the generated configuration: %v", cerr)
	}
	res.CheckOK = true
	key, err := r.versionKey(ctx, t)
	if err != nil {
		return fail("%v", err)
	}
	unit := fmt.Sprintf("jul-ia-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9)
	j, err := StartJul(r.Opt.Sys, r.Opt.Env, unit, t.Path, dir, cfgPath, key, p, r.Opt.Startup)
	if err != nil {
		res.JulExit = "startup failed"
		return fail("start: %v", err)
	}
	defer func() {
		grace := r.Opt.StopGrace
		if grace == 0 {
			grace = 15 * time.Second
		}
		if err := j.Stop(grace); err != nil {
			res.Notes = append(res.Notes, err.Error())
		}
		if res.JulExit == "" {
			res.JulExit = j.ExitState()
		}
		if res.Envelope != nil && res.Envelope.OOMScope != "" {
			after := kv(readKV(filepath.Join(res.Envelope.OOMScope, "memory.events")), "oom_kill")
			if after != nil && res.Envelope.ParentOOMKill != nil {
				d := *after - *res.Envelope.ParentOOMKill
				res.OOMKillDelta, res.OOMScope = &d, res.Envelope.OOMScope+" (hierarchical memory.events oom_kill; includes sibling scopes)"
			}
		}
	}()
	if r.Opt.Env.Mode == "required" {
		eff := r.Opt.Sys.VerifyEnvelope(j.PID, r.Opt.Env)
		res.Envelope = &eff
		if !eff.Verified {
			res.Invalid = append(res.Invalid, "envelope: "+strings.Join(eff.Problems, "; "))
			return fail("envelope not verified")
		}
	}
	roots, err := TrustedRoots(r.Opt.Cert)
	if err != nil {
		res.Invalid = append(res.Invalid, "tls: "+err.Error())
		return fail("trusted certificate not loaded")
	}
	target := Target{Port: p.Port, TLSPort: p.TLSPort, WASM: wasm, Roots: roots}
	samp := NewSampler(j, r.Opt.SampleEvery)
	before, _ := Scrape(p.AdminPort)
	res.PreCanaries = RunCanaries(target, waf, wasm)
	wd := r.Opt.Watchdog
	if wd == 0 {
		wd = PlannedDuration(sc) + 3*time.Minute
	}
	wctx, wcancel := context.WithTimeout(ctx, wd)
	defer wcancel()
	if r.Opt.KillJulAfter > 0 {
		go func() {
			sleepCtx(wctx, r.Opt.KillJulAfter)
			if wctx.Err() == nil {
				_ = syscall.Kill(-j.PID, syscall.SIGKILL)
			}
		}()
	}
	switch sc.Kind {
	case "f4":
		samp.SetPhase("f4")
		for _, proto := range sc.F4.Protos {
			for _, mode := range sc.F4.Modes {
				res.F4 = append(res.F4, RunF4(wctx, target, *sc.F4, proto, mode))
			}
		}
	default:
		for _, ph := range sc.Phases {
			res.Phases = append(res.Phases, r.runPhase(wctx, j, samp, target, sc, ph, p, &res))
		}
	}
	if wctx.Err() != nil && ctx.Err() == nil {
		res.Invalid = append(res.Invalid, "watchdog: cell exceeded "+wd.String())
	}
	if ctx.Err() != nil {
		return fail("interrupted")
	}
	samp.SetPhase("recovery")
	sleepCtx(ctx, sc.Recovery.D())
	res.JulExitedEarly = j.Exited()
	if !res.JulExitedEarly {
		res.PostCanaries = RunCanaries(target, waf, wasm)
		res.HeapInuseGC, res.HeapAllocGC = HeapAfterGC(p.AdminPort)
		if st := j.sys.status(j.PID); st != nil {
			res.RSSAfterGC = kib(st["VmRSS"])
		}
		if after, err := Scrape(p.AdminPort); err == nil {
			res.CounterDelta, res.Unavailable = deltas(before, after)
			res.Foundation = Foundation(after)
		}
	}
	samples := samp.Finish()
	res.Samples = len(samples)
	res.PeakCgMem, res.PeakSource, res.PeakRSS = peaks(samples)
	if err := writeSamples(filepath.Join(dir, "samples.jsonl"), samples); err != nil {
		res.Notes = append(res.Notes, "samples: "+err.Error())
	}
	if res.JulExitedEarly {
		res.JulExit = j.ExitState()
	}
	in, fi := validity(res, r.Opt.Env)
	res.Invalid, res.Findings = append(res.Invalid, in...), append(res.Findings, fi...)
	res.State = "completed"
	return res
}

func deltas(before, after map[string]float64) (Metrics, []string) {
	b, a := pick(before, CounterMetrics), pick(after, CounterMetrics)
	out := Metrics{}
	var missing []string
	for _, w := range CounterMetrics {
		av, ok := a[w.Key]
		if !ok {
			missing = append(missing, w.Key)
			continue
		}
		out[w.Key] = av - b[w.Key]
	}
	return out, missing
}

func peaks(ss []Sample) (cg *int64, src string, rss *int64) {
	for _, s := range ss {
		for _, v := range []*int64{s.CgPeak, s.CgMem} {
			if v != nil && (cg == nil || *v > *cg) {
				x := *v
				cg = &x
			}
		}
		if s.CgPeak != nil {
			src = "cgroup memory.peak"
		} else if s.CgMem != nil && src == "" {
			src = "max sampled cgroup memory.current (500 ms)"
		}
		if s.RSS != nil && (rss == nil || *s.RSS > *rss) {
			x := *s.RSS
			rss = &x
		}
	}
	return cg, src, rss
}

func writeSamples(path string, ss []Sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, s := range ss {
		if err := enc.Encode(s); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}

// validity applies the frozen validity rule to a completed run.
// validity splits a completed run's problems into instrument reasons (the
// measurement is not trustworthy) and runtime findings (Jul misbehaved).
// An accurately captured finding is evidence, not an instrument failure.
func validity(res RunResult, env Envelope) (instrument, findings []string) {
	for _, c := range append(slicesClone(res.PreCanaries), res.PostCanaries...) {
		if !c.OK {
			findings = append(findings, fmt.Sprintf("canary %s: status %d want %d", c.Name, c.Status, c.Want))
		}
	}
	if len(res.PreCanaries) == 0 || (len(res.PostCanaries) == 0 && !res.JulExitedEarly) {
		instrument = append(instrument, "canaries missing")
	}
	if res.JulExitedEarly {
		findings = append(findings, "jul exited during the run: "+res.JulExit)
	}
	if env.Mode == "required" && (res.Envelope == nil || !res.Envelope.Verified) {
		instrument = append(instrument, "envelope not verified")
	}
	for _, ph := range res.Phases {
		for _, c := range ph.Classes {
			in, fi := c.Check()
			for _, m := range in {
				instrument = append(instrument, fmt.Sprintf("phase %s class %s: %s", ph.Name, c.Workload, m))
			}
			for _, m := range fi {
				findings = append(findings, fmt.Sprintf("phase %s class %s: %s", ph.Name, c.Workload, m))
			}
		}
	}
	for _, f := range res.F4 {
		in, fi := f.Check()
		instrument, findings = append(instrument, in...), append(findings, fi...)
	}
	return instrument, findings
}

func slicesClone[T any](s []T) []T { return append([]T(nil), s...) }

// PlannedDuration is the nominal length of a scenario excluding startup.
func PlannedDuration(sc Scenario) time.Duration {
	d := sc.Recovery.D()
	for _, ph := range sc.Phases {
		d += ph.Warmup.D() + ph.Measure.D()
	}
	if sc.F4 != nil {
		d += time.Duration(len(sc.F4.Protos)*len(sc.F4.Modes)) * (sc.F4.ClientLimit.D() + 5*time.Second)
	}
	return d
}

func (r *Runner) cpuUsec(j *Jul) (*int64, string) {
	if cg := j.Cgroup(); cg != "" && r.Opt.Env.Mode == "required" {
		if v := kv(readKV(filepath.Join(cg, "cpu.stat")), "usage_usec"); v != nil {
			return v, "cgroup cpu.stat usage_usec"
		}
	}
	b, err := os.ReadFile(filepath.Join(r.Opt.Sys.Proc, strconv.Itoa(j.PID), "stat"))
	if err != nil {
		return nil, ""
	}
	s := string(b)
	f := strings.Fields(s[strings.LastIndexByte(s, ')')+1:])
	if len(f) < 13 {
		return nil, ""
	}
	ut, _ := strconv.ParseInt(f[11], 10, 64)
	st, _ := strconv.ParseInt(f[12], 10, 64)
	v := (ut + st) * 10_000 // USER_HZ is 100 on Linux
	return &v, "/proc/pid/stat utime+stime (process only)"
}

func (r *Runner) runPhase(ctx context.Context, j *Jul, samp *Sampler, t Target, sc Scenario, ph Phase, p ConfigParams, res *RunResult) PhaseResult {
	out := PhaseResult{Name: ph.Name}
	start := time.Now()
	from := start.Add(ph.Warmup.D())
	end := from.Add(ph.Measure.D())
	samp.SetPhase("warmup:" + ph.Name)
	var cpuFrom, cpuTo *int64
	var cpuSrc string
	timerDone := make(chan struct{})
	go func() {
		defer close(timerDone)
		sleepCtx(ctx, time.Until(from))
		samp.SetPhase("measure:" + ph.Name)
		cpuFrom, cpuSrc = r.cpuUsec(j)
		sleepCtx(ctx, time.Until(end))
		samp.SetPhase("drain:" + ph.Name)
		cpuTo, _ = r.cpuUsec(j)
	}()
	rctx, rcancel := context.WithDeadline(ctx, end)
	defer rcancel()
	var reloadWG sync.WaitGroup
	if sc.Kind == "reload" {
		reloadWG.Go(func() { res.Reloads = reloadChurn(rctx, j, p) })
	}
	classes := make([]ClassStats, len(ph.Workloads))
	var wg sync.WaitGroup
	for i, w := range ph.Workloads {
		wg.Go(func() {
			cs, err := Drive(ctx, t, w, from, end)
			if err != nil {
				cs = ClassStats{Workload: w.Name, Classes: map[string]int{"error:" + err.Error(): 1}}
			}
			classes[i] = cs
		})
	}
	wg.Wait()
	<-timerDone
	reloadWG.Wait()
	out.Classes = classes
	if cpuFrom != nil && cpuTo != nil && ctx.Err() == nil {
		frac := round3(float64(*cpuTo-*cpuFrom) / (ph.Measure.D().Seconds() * 1e6) / Rules.EnvelopeCPUs)
		sat := frac >= Rules.SaturatedCPU
		out.CPUFraction, out.Saturated, out.CPUSource = &frac, &sat, cpuSrc
	}
	return out
}

// reloadChurn reloads every 2 s, alternating plugin and WAF policy, with
// reload 5 invalid, exactly as #695 s9.
func reloadChurn(ctx context.Context, j *Jul, base ConfigParams) []string {
	var notes []string
	start := time.Now()
	for i := 1; ; i++ {
		sleepCtx(ctx, 2*time.Second)
		if ctx.Err() != nil {
			return notes
		}
		p := base
		p.NameA = []string{"a1", "a2"}[i%2]
		p.WAFRuleID = 1695101 + i%2*10
		p.BrokenWAF = i == 5
		cfg, err := RenderConfig(p)
		if err == nil {
			err = j.Reload(cfg)
		}
		note := fmt.Sprintf("t+%.1fs reload %d nameA=%s rule=%d broken=%v", time.Since(start).Seconds(), i, p.NameA, p.WAFRuleID, p.BrokenWAF)
		if err != nil {
			note += " error=" + err.Error()
		}
		notes = append(notes, note)
		if p.BrokenWAF {
			base.NameA = p.NameA
		}
	}
}

// StartOrigin runs the harness's origin subcommand as an owned subprocess.
func StartOrigin(self, cpus, fault string) (*exec.Cmd, string, error) {
	ports, err := freePorts(1)
	if err != nil {
		return nil, "", err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", ports[0])
	args := []string{self, "origin", "-listen", addr, "-fault", fault}
	if cpus != "" {
		if _, err := exec.LookPath("taskset"); err == nil {
			args = append([]string{"taskset", "-c", cpus}, args...)
		}
	}
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // the harness itself
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}
	for range 100 {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return cmd, addr, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	return nil, "", errors.New("origin did not start")
}
