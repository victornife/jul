// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Sys locates the kernel interfaces so tests can supply fixtures.
type Sys struct {
	Proc, Cgroup string
}

var hostSys = Sys{Proc: "/proc", Cgroup: "/sys/fs/cgroup"}

// Envelope is the requested resource envelope.
type Envelope struct {
	Mode       string `json:"mode"` // required | none
	CPUs       string `json:"jul_cpus"`
	CPUQuota   string `json:"cpu_quota"`
	MemoryMax  string `json:"memory_max"`
	GOMAXPROCS int    `json:"gomaxprocs"`
}

// DefaultEnvelope is the ADR 0032 assessment envelope.
var DefaultEnvelope = Envelope{Mode: "required", CPUs: "0,1", CPUQuota: "200%", MemoryMax: "768M", GOMAXPROCS: 2}

// Effective records what the kernel actually applied to the Jul process.
type Effective struct {
	Cgroup        string   `json:"cgroup,omitempty"`
	MemoryMax     *int64   `json:"memory_max_bytes"`
	SwapMax       *int64   `json:"memory_swap_max_bytes"`
	CPUMax        string   `json:"cpu_max,omitempty"`
	CPUQuotaCPUs  float64  `json:"cpu_quota_cpus,omitempty"`
	CPUsAllowed   string   `json:"cpus_allowed_list,omitempty"`
	GOMAXPROCS    string   `json:"gomaxprocs_env,omitempty"`
	Verified      bool     `json:"verified"`
	Problems      []string `json:"problems,omitempty"`
	OOMScope      string   `json:"oom_scope,omitempty"`
	ParentOOMKill *int64   `json:"parent_oom_kill_at_start"`
}

func readInt(path string) *int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := strings.TrimSpace(string(b))
	if s == "max" {
		v := int64(-1)
		return &v
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

func readKV(path string) map[string]int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	out := map[string]int64{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 {
			if v, err := strconv.ParseInt(f[1], 10, 64); err == nil {
				out[f[0]] = v
			}
		}
	}
	return out
}

func kv(m map[string]int64, k string) *int64 {
	v, ok := m[k]
	if !ok {
		return nil
	}
	return &v
}

// CgroupOf returns the cgroup v2 directory of pid.
func (s Sys) CgroupOf(pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			return filepath.Join(s.Cgroup, strings.TrimSpace(rest)), nil
		}
	}
	return "", errors.New("no cgroup v2 entry")
}

func (s Sys) status(pid int) map[string]string {
	b, err := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "status"))
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for line := range strings.SplitSeq(string(b), "\n") {
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = strings.TrimSpace(v)
		}
	}
	return out
}

func (s Sys) environ(pid int, key string) string {
	b, _ := os.ReadFile(filepath.Join(s.Proc, strconv.Itoa(pid), "environ"))
	for kv := range strings.SplitSeq(string(b), "\x00") {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// VerifyEnvelope reads the limits the kernel applied to pid.
func (s Sys) VerifyEnvelope(pid int, want Envelope) Effective {
	var e Effective
	cg, err := s.CgroupOf(pid)
	if err != nil {
		e.Problems = append(e.Problems, "cgroup: "+err.Error())
	}
	e.Cgroup = cg
	e.MemoryMax = readInt(filepath.Join(cg, "memory.max"))
	e.SwapMax = readInt(filepath.Join(cg, "memory.swap.max"))
	if b, err := os.ReadFile(filepath.Join(cg, "cpu.max")); err == nil {
		e.CPUMax = strings.TrimSpace(string(b))
		if f := strings.Fields(e.CPUMax); len(f) == 2 && f[0] != "max" {
			q, _ := strconv.ParseFloat(f[0], 64)
			p, _ := strconv.ParseFloat(f[1], 64)
			if p > 0 {
				e.CPUQuotaCPUs = q / p
			}
		}
	}
	e.CPUsAllowed = s.status(pid)["Cpus_allowed_list"]
	e.GOMAXPROCS = s.environ(pid, "GOMAXPROCS")
	e.OOMScope = filepath.Dir(cg)
	e.ParentOOMKill = kv(readKV(filepath.Join(e.OOMScope, "memory.events")), "oom_kill")
	if want.Mode != "required" {
		e.Problems = append(e.Problems, "envelope not requested")
		return e
	}
	wantMem, _ := parseSize(want.MemoryMax)
	quota, _ := strconv.ParseFloat(strings.TrimSuffix(want.CPUQuota, "%"), 64)
	check := func(ok bool, msg string) {
		if !ok {
			e.Problems = append(e.Problems, msg)
		}
	}
	check(e.MemoryMax != nil && *e.MemoryMax == wantMem, fmt.Sprintf("memory.max is %s, want %d", fmtp(e.MemoryMax), wantMem))
	check(e.SwapMax != nil && *e.SwapMax == 0, "memory.swap.max is "+fmtp(e.SwapMax)+", want 0")
	check(e.CPUQuotaCPUs == quota/100, fmt.Sprintf("cpu.max is %q, want %.2f CPUs", e.CPUMax, quota/100))
	check(e.CPUsAllowed == expandCPUs(want.CPUs), fmt.Sprintf("Cpus_allowed_list is %q, want %q", e.CPUsAllowed, expandCPUs(want.CPUs)))
	check(e.GOMAXPROCS == strconv.Itoa(want.GOMAXPROCS), fmt.Sprintf("GOMAXPROCS is %q, want %d", e.GOMAXPROCS, want.GOMAXPROCS))
	e.Verified = len(e.Problems) == 0
	return e
}

// expandCPUs normalises "0,1" or "0-1" to the kernel's list form "0-1".
func expandCPUs(s string) string {
	var ids []int
	for _, part := range splitList(s) {
		lo, hi, isRange := strings.Cut(part, "-")
		a, _ := strconv.Atoi(lo)
		b := a
		if isRange {
			b, _ = strconv.Atoi(hi)
		}
		for i := a; i <= b; i++ {
			ids = append(ids, i)
		}
	}
	var out []string
	for i := 0; i < len(ids); {
		j := i
		for j+1 < len(ids) && ids[j+1] == ids[j]+1 {
			j++
		}
		if j > i {
			out = append(out, fmt.Sprintf("%d-%d", ids[i], ids[j]))
		} else {
			out = append(out, strconv.Itoa(ids[i]))
		}
		i = j + 1
	}
	return strings.Join(out, ",")
}

func fmtp(p *int64) string {
	if p == nil {
		return "unavailable"
	}
	return strconv.FormatInt(*p, 10)
}

func parseSize(s string) (int64, error) {
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, strings.TrimSuffix(s, "G")
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, strings.TrimSuffix(s, "M")
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, strings.TrimSuffix(s, "K")
	}
	v, err := strconv.ParseInt(s, 10, 64)
	return v * mult, err
}

// Launch builds the command that starts Jul inside the envelope.
func Launch(env Envelope, unit, jul, cfg, tmp string) *exec.Cmd {
	args := []string{"env", "GOMAXPROCS=" + strconv.Itoa(env.GOMAXPROCS), "TMPDIR=" + tmp}
	if env.Mode == "required" {
		args = append([]string{"systemd-run", "--user", "--scope", "--quiet", "--unit=" + unit,
			"-p", "CPUQuota=" + env.CPUQuota, "-p", "MemoryMax=" + env.MemoryMax, "-p", "MemorySwapMax=0", "--"}, args...)
		args = append(args, "taskset", "-c", env.CPUs)
	}
	args = append(args, jul, "serve", "-config", cfg)
	return exec.Command(args[0], args[1:]...) //nolint:gosec // operator-selected binaries
}

// EnvelopeSupported checks the prerequisites of the required envelope.
func EnvelopeSupported(s Sys) error {
	for _, tool := range []string{"systemd-run", "systemctl", "taskset"} {
		if _, err := exec.LookPath(tool); err != nil {
			return fmt.Errorf("%s not found: %w", tool, err)
		}
	}
	if _, err := os.Stat(filepath.Join(s.Cgroup, "cgroup.controllers")); err != nil {
		return fmt.Errorf("cgroup v2 unified hierarchy unavailable: %w", err)
	}
	out, err := exec.Command("systemd-run", "--user", "--scope", "--quiet", "-p", "MemoryMax=64M", "-p", "MemorySwapMax=0", "true").CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd user scope with memory limits unavailable: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Jul is one owned Jul process.
type Jul struct {
	Cmd      *exec.Cmd
	PID      int
	Unit     string
	Dir, Tmp string
	CfgPath  string
	Params   ConfigParams
	Env      Envelope
	sys      Sys
	log      *os.File
	exited   chan struct{}
	exitErr  error
	cgroup   string
	cgMu     sync.Mutex
}

// StartJul starts Jul on an already written and checked configuration and
// waits, bounded by startup, for /healthz.
func StartJul(sys Sys, env Envelope, unit, bin, dir, cfg, key string, p ConfigParams, startup time.Duration) (*Jul, error) {
	tmp, err := os.MkdirTemp("", "jul-inspection-spill-")
	if err != nil {
		return nil, err
	}
	logf, err := os.Create(filepath.Join(dir, "jul-output.txt"))
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	cmd := Launch(env, unit, bin, cfg, tmp)
	cmd.Env = append(os.Environ(), "JUL_EFFECTIVE_VERSION_KEY_FILE="+key)
	cmd.Stdout, cmd.Stderr = logf, logf
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	j := &Jul{Cmd: cmd, PID: cmd.Process.Pid, Unit: unit, Dir: dir, Tmp: tmp, CfgPath: cfg, Params: p, Env: env, sys: sys, log: logf, exited: make(chan struct{})}
	go func() { j.exitErr = cmd.Wait(); close(j.exited) }()
	deadline := time.Now().Add(startup)
	for time.Now().Before(deadline) {
		if j.Exited() {
			_ = j.Stop(time.Second)
			return nil, fmt.Errorf("jul exited during startup: %v", j.exitErr)
		}
		if probe204(fmt.Sprintf("http://127.0.0.1:%d/healthz", p.Port)) {
			return j, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	_ = j.Stop(5 * time.Second)
	return nil, fmt.Errorf("jul did not become ready within %s", startup)
}

func probe204(url string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

// Exited reports whether the process has ended.
func (j *Jul) Exited() bool {
	select {
	case <-j.exited:
		return true
	default:
		return false
	}
}

// ExitState describes how the process ended.
func (j *Jul) ExitState() string {
	if !j.Exited() {
		return "running"
	}
	if j.exitErr == nil {
		return "exit 0"
	}
	var ee *exec.ExitError
	if errors.As(j.exitErr, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return "signal " + ws.Signal().String()
		}
	}
	return j.exitErr.Error()
}

// Cgroup returns the process cgroup, cached once found.
func (j *Jul) Cgroup() string {
	j.cgMu.Lock()
	defer j.cgMu.Unlock()
	if j.cgroup == "" && !j.Exited() {
		if cg, err := j.sys.CgroupOf(j.PID); err == nil {
			j.cgroup = cg
		}
	}
	return j.cgroup
}

// Reload rewrites the configuration and sends SIGHUP.
func (j *Jul) Reload(cfg []byte) error {
	if err := os.WriteFile(j.CfgPath, cfg, 0o600); err != nil {
		return err
	}
	return j.Cmd.Process.Signal(syscall.SIGHUP)
}

// Stop terminates the process group with a bounded grace period and removes
// only the run's own spill directory. The scope unit is stopped as well.
func (j *Jul) Stop(grace time.Duration) error {
	var err error
	if !j.Exited() {
		_ = syscall.Kill(-j.PID, syscall.SIGTERM)
		select {
		case <-j.exited:
		case <-time.After(grace):
			err = fmt.Errorf("jul did not stop within %s; killed", grace)
			_ = syscall.Kill(-j.PID, syscall.SIGKILL)
			select {
			case <-j.exited:
			case <-time.After(10 * time.Second):
				err = errors.New("jul did not exit after SIGKILL")
			}
		}
	}
	if j.Env.Mode == "required" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = exec.CommandContext(ctx, "systemctl", "--user", "stop", j.Unit+".scope").Run()
		cancel()
	}
	_ = j.log.Close()
	_ = os.RemoveAll(j.Tmp)
	return err
}

// Sample is one 500 ms observation. Nil fields were unavailable.
type Sample struct {
	T          float64 `json:"t"`
	Phase      string  `json:"phase"`
	CgMem      *int64  `json:"cg_memory_current"`
	CgPeak     *int64  `json:"cg_memory_peak"`
	CgAnon     *int64  `json:"cg_anon"`
	CgShmem    *int64  `json:"cg_shmem"`
	CgFile     *int64  `json:"cg_file"`
	CPUUsec    *int64  `json:"cg_cpu_usage_usec"`
	OOMKill    *int64  `json:"cg_oom_kill"`
	RSS        *int64  `json:"rss_bytes"`
	Threads    *int64  `json:"threads"`
	FDs        *int64  `json:"fds"`
	SpillFiles int     `json:"spill_files"`
	SpillBytes int64   `json:"spill_bytes"`
	Metrics    Metrics `json:"metrics,omitempty"`
	ScrapeErr  string  `json:"scrape_error,omitempty"`
}

// Metrics maps selected series to values; absent keys were not exported.
type Metrics map[string]float64

// SampledMetrics are read every sample when exported by the target.
var SampledMetrics = []struct {
	Key, Metric string
	Filters     []string
}{
	{"goroutines", "go_goroutines", nil},
	{"heap_inuse", "go_memstats_heap_inuse_bytes", nil},
	{"heap_alloc", "go_memstats_heap_alloc_bytes", nil},
	{"gc_count", "go_gc_duration_seconds_count", nil},
	{"in_flight", "jul_http_requests_in_flight", nil},
	{"plugin_instances", "jul_plugin_instances", nil},
	{"upstream_active_limited", "jul_upstream_active_requests", []string{`pool="limited"`}},
	{"upstream_pending_limited", "jul_upstream_pending_requests", []string{`pool="limited"`}},
	{"inspection_bytes_foundation", "jul_inspection_bytes", nil},
	{"inspection_leases_foundation", "jul_inspection_leases", nil},
}

// CounterMetrics are compared before and after a run.
var CounterMetrics = []struct {
	Key, Metric string
	Filters     []string
}{
	{"waf_tx_pass", "jul_waf_transactions_total", []string{`outcome="pass"`}},
	{"waf_tx_all", "jul_waf_transactions_total", nil},
	{"waf_tx_body_limit", "jul_waf_transactions_total", []string{`outcome="rejected_body_limit"`}},
	{"plugin_invocations", "jul_plugin_invocations_total", nil},
	{"plugin_invocations_ok", "jul_plugin_invocations_total", []string{`result="ok"`}},
	{"plugin_resp_invocations", "jul_plugin_response_invocations_total", nil},
	{"plugin_resp_unavailable", "jul_plugin_response_body_unavailable_total", nil},
	{"plugin_waits_acquired", "jul_plugin_instance_waits_total", []string{`result="acquired"`}},
	{"plugin_waits_rejected", "jul_plugin_instance_waits_total", []string{`result="rejected"`}},
	{"admission_rejected", "jul_upstream_admission_rejected_total", []string{`pool="limited"`}},
	{"cache_hit", "jul_cache_events_total", []string{`state="hit"`}},
	{"cache_miss", "jul_cache_events_total", []string{`state="miss"`}},
	{"reload_success", "jul_reload_total", []string{`outcome="success"`}},
	{"reload_failure", "jul_reload_total", []string{`outcome="failure"`}},
	{"transport_retired", "jul_transport_retired_total", nil},
}

// Scrape returns the admin exposition as series -> value.
func Scrape(adminPort int) (map[string]float64, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/metrics", adminPort), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("metrics status %d", resp.StatusCode)
	}
	return ParseExposition(resp.Body)
}

// ParseExposition parses Prometheus text format into series -> value.
func ParseExposition(r io.Reader) (map[string]float64, error) {
	out := map[string]float64{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || line[0] == '#' {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		if v, err := strconv.ParseFloat(line[i+1:], 64); err == nil {
			out[line[:i]] = v
		}
	}
	return out, sc.Err()
}

// Sum adds matching series; ok is false when the metric is not exported.
func Sum(m map[string]float64, metric string, filters ...string) (float64, bool) {
	var t float64
	found := false
	for k, v := range m {
		name, labels, _ := strings.Cut(k, "{")
		if name != metric {
			continue
		}
		found = true
		match := true
		for _, f := range filters {
			if !strings.Contains(labels, f) {
				match = false
				break
			}
		}
		if match {
			t += v
		}
	}
	return t, found
}

func pick(m map[string]float64, set []struct {
	Key, Metric string
	Filters     []string
}) Metrics {
	out := Metrics{}
	for _, w := range set {
		if v, ok := Sum(m, w.Metric, w.Filters...); ok {
			out[w.Key] = v
		}
	}
	return out
}

// Sampler records Samples every interval until stopped or Jul exits.
type Sampler struct {
	j     *Jul
	start time.Time
	mu    sync.Mutex
	phase string
	out   []Sample
	stop  chan struct{}
	done  chan struct{}
}

// NewSampler starts sampling.
func NewSampler(j *Jul, every time.Duration) *Sampler {
	s := &Sampler{j: j, start: time.Now(), phase: "baseline", stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(s.done)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			sm := s.Take()
			s.mu.Lock()
			s.out = append(s.out, sm)
			s.mu.Unlock()
			select {
			case <-s.stop:
				return
			case <-j.exited:
				return
			case <-t.C:
			}
		}
	}()
	return s
}

// SetPhase labels later samples.
func (s *Sampler) SetPhase(p string) {
	s.mu.Lock()
	s.phase = p
	s.mu.Unlock()
}

// Take reads one sample.
func (s *Sampler) Take() Sample {
	j := s.j
	s.mu.Lock()
	sm := Sample{T: round3(time.Since(s.start).Seconds()), Phase: s.phase}
	s.mu.Unlock()
	if cg := j.Cgroup(); cg != "" && j.Env.Mode == "required" {
		ms := readKV(filepath.Join(cg, "memory.stat"))
		sm.CgMem = readInt(filepath.Join(cg, "memory.current"))
		sm.CgPeak = readInt(filepath.Join(cg, "memory.peak"))
		sm.CgAnon, sm.CgShmem, sm.CgFile = kv(ms, "anon"), kv(ms, "shmem"), kv(ms, "file")
		sm.CPUUsec = kv(readKV(filepath.Join(cg, "cpu.stat")), "usage_usec")
		sm.OOMKill = kv(readKV(filepath.Join(cg, "memory.events")), "oom_kill")
	}
	if st := j.sys.status(j.PID); st != nil {
		sm.RSS = kib(st["VmRSS"])
		sm.Threads = num(st["Threads"])
	}
	if fds, err := os.ReadDir(filepath.Join(j.sys.Proc, strconv.Itoa(j.PID), "fd")); err == nil {
		n := int64(len(fds))
		sm.FDs = &n
	}
	if ents, err := os.ReadDir(j.Tmp); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "body") {
				sm.SpillFiles++
				if fi, err := e.Info(); err == nil {
					sm.SpillBytes += fi.Size()
				}
			}
		}
	}
	if m, err := Scrape(j.Params.AdminPort); err != nil {
		sm.ScrapeErr = err.Error()
	} else {
		sm.Metrics = pick(m, SampledMetrics)
	}
	return sm
}

func kib(s string) *int64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return nil
	}
	v, err := strconv.ParseInt(f[0], 10, 64)
	if err != nil {
		return nil
	}
	v *= 1024
	return &v
}

func num(s string) *int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return nil
	}
	return &v
}

// Finish stops sampling and returns every sample.
func (s *Sampler) Finish() []Sample {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out
}

// HeapAfterGC forces two collections through pprof and reads the live heap.
// The values are nil when the target does not expose them.
func HeapAfterGC(adminPort int) (inuse, alloc *float64) {
	for range 2 {
		req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/heap?gc=1&debug=1", adminPort), nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		if resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	time.Sleep(300 * time.Millisecond)
	m, err := Scrape(adminPort)
	if err != nil {
		return nil, nil
	}
	if v, ok := Sum(m, "go_memstats_heap_inuse_bytes"); ok {
		inuse = &v
	}
	if v, ok := Sum(m, "go_memstats_heap_alloc_bytes"); ok {
		alloc = &v
	}
	return inuse, alloc
}

// Foundation returns every exported jul_inspection_* series. On post-I1
// binaries these are governor foundation counters, not request-path
// protection evidence.
func Foundation(m map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range m {
		if strings.HasPrefix(k, "jul_inspection_") {
			out[k] = v
		}
	}
	return out
}
