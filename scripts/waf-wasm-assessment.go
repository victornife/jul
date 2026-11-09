// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build ignore

// waf-wasm-assessment is the bounded #695 harness that measures aggregate
// WAF/WASM inspection resources and admission coverage for a full-profile Jul
// binary on loopback. The predeclared plan, envelope and thresholds are in
// docs/reviews/2026-11-09-695-waf-wasm-assessment-plan.md; results are in
// docs/reviews/2026-11-09-695-waf-wasm-resource-assessment.md.
//
// Build and run (Linux with systemd --user cgroup delegation):
//
//	make build-full
//	(cd examples/plugins && GOOS=wasip1 GOARCH=wasm go build -trimpath -buildvcs=false \
//	    -buildmode=c-shared -o /tmp/a695/inspect-load.wasm ./inspect-load)
//	go build -o /tmp/a695/harness scripts/waf-wasm-assessment.go
//	/tmp/a695/harness run -jul ./jul -plugin /tmp/a695/inspect-load.wasm -out /tmp/a695/out
//
// Jul runs inside its own transient systemd scope (CPUQuota, MemoryMax,
// MemorySwapMax=0) pinned to -jul-cpus; the origin and the load generator run
// as separate processes on other CPUs, so the scope's cgroup counters measure
// only Jul (including tmpfs spill files it writes). Only synthetic, non-secret
// payloads are sent, only to loopback listeners.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"text/template"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/net/http2"
)

const (
	adminToken  = "assessment-695-token-not-a-secret"
	wafReqMark  = "ASSESS-WAF-REQ-BLOCK"
	wafRespMark = "ASSESS-WAF-RESP-BLOCK"
	wasmReqMark = "ASSESS-WASM-REQ-BLOCK"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: waf-wasm-assessment origin|run [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "origin":
		originMain(os.Args[2:])
	case "run":
		runMain(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, "unknown mode", os.Args[1])
		os.Exit(2)
	}
}

// ---------------------------------------------------------------- origin

var originCalls atomic.Int64

func filler(n int, marker string) []byte {
	const unit = "<p>synthetic assessment response paragraph 0123456789</p>\n"
	var b bytes.Buffer
	b.Grow(n)
	b.WriteString(marker)
	for b.Len() < n {
		b.WriteString(unit)
	}
	return b.Bytes()[:n]
}

func originMain(args []string) {
	fs := flag.NewFlagSet("origin", flag.ExitOnError)
	listen := fs.String("listen", "127.0.0.1:19001", "listen address")
	_ = fs.Parse(args)
	mux := http.NewServeMux()
	mux.HandleFunc("/__count", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%d", originCalls.Load())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		originCalls.Add(1)
		n, _ := io.Copy(io.Discard, r.Body)
		q := r.URL.Query()
		size, _ := strconv.Atoi(q.Get("resp"))
		if size <= 0 {
			size = 1024
		}
		if d, _ := strconv.Atoi(q.Get("delay")); d > 0 {
			select {
			case <-time.After(time.Duration(d) * time.Millisecond):
			case <-r.Context().Done():
				return
			}
		}
		marker := ""
		if q.Get("canary") == "waf" {
			marker = wafRespMark
		}
		body := filler(size, marker)
		h := w.Header()
		h.Set("Content-Type", "text/html")
		h.Set("X-Origin-Req-Len", strconv.FormatInt(n, 10))
		h.Set("X-Echo-A-Req-Len", r.Header.Get("X-Assess-a-Req-Len"))
		h.Set("X-Echo-B-Req-Len", r.Header.Get("X-Assess-b-Req-Len"))
		if strings.HasPrefix(r.URL.Path, "/cached/") {
			h.Set("Cache-Control", "public, max-age=300")
		} else {
			h.Set("Cache-Control", "no-store")
		}
		if q.Get("chunked") == "1" {
			fl, _ := w.(http.Flusher)
			for off := 0; off < len(body); off += 64 << 10 {
				end := min(off+64<<10, len(body))
				if _, err := w.Write(body[off:end]); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
			return
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---------------------------------------------------------------- config

type cfgParams struct {
	WAF, WASM, Cache  bool
	Port, TLSPort     int
	AdminPort         int
	Origin            string
	WWW, Plugin, Cert string
	Key               string
	NameA             string // inspect-a evidence label; changing it is a real plugin policy change
	WAFRuleID         int    // request canary rule id; changing it is a real WAF policy change
	BrokenWAF         bool   // an invalid directive: preparation must fail
	ReadTimeout       string // listener read_timeout ("" = default 0/unbounded)
}

const cfgTemplate = `[global]
log_level = "warn"

[admin]
enabled = true
listen = "127.0.0.1:{{.AdminPort}}"
token = "` + adminToken + `"
pprof = true
console = false
{{if .Cache}}
[cache]
enabled = true
memory_max_size = "64MB"
max_entry_size = "4MB"
default_ttl = "5m"
{{end}}
[[upstreams]]
name = "origin"
strategy = "round_robin"
servers = [{ address = "{{.Origin}}" }]

[[upstreams]]
name = "limited"
strategy = "round_robin"
servers = [{ address = "{{.Origin}}" }]
  [upstreams.resilience]
  max_active_requests = 8
  max_pending_requests = 16
  pending_timeout = "2s"
{{if .WASM}}
[plugins.inspect-a]
path = "{{.Plugin}}"
abi = "jul-abi/v2"
config = { name = "{{.NameA}}", read_body = "true", subscribe = "true" }

[plugins.inspect-b]
path = "{{.Plugin}}"
abi = "jul-abi/v2"
config = { name = "b", read_body = "true", subscribe = "true", replace = "true" }
{{end}}
{{define "locs"}}
  [[servers.locations]]
  match = { type = "exact", path = "/healthz" }
  return = 204
{{range $l := .Locs}}
  [[servers.locations]]
  match = { type = "prefix", path = "{{$l.Path}}" }
  {{$l.Action}}
{{- if $.P.Cache}}{{if $l.Cache}}
  cache = true{{end}}{{end}}
{{- if $.P.WASM}}
  plugins = ["inspect-a", "inspect-b"]{{end}}
{{- if $.P.WAF}}
  [servers.locations.waf]
  enabled = true
  mode = "block"
  crs_enabled = true
  request_body_limit = "1mb"
  response_body_check = true
  inline_rules = '''
SecRule REQUEST_BODY|ARGS_POST "@contains ` + wafReqMark + `" "id:{{$.P.WAFRuleID}},phase:2,deny,status:403,log,msg:'assessment request canary'"
SecRule RESPONSE_BODY "@contains ` + wafRespMark + `" "id:1695102,phase:4,deny,status:403,log,msg:'assessment response canary'"
{{- if $.P.BrokenWAF}}
SecRule THIS_IS_NOT_A_VARIABLE "@rx (" "id:1695199,phase:1,deny"
{{- end}}
'''
{{- end}}
{{end}}{{end}}
[[servers]]
listen = "127.0.0.1:{{.Port}}"
{{- if .ReadTimeout}}
read_timeout = "{{.ReadTimeout}}"{{end}}
{{template "locs" (locs .)}}
[[servers]]
listen = "127.0.0.1:{{.TLSPort}}"
server_names = ["localhost"]
{{- if .ReadTimeout}}
read_timeout = "{{.ReadTimeout}}"{{end}}
  [servers.tls]
  enabled = true
  cert = "{{.Cert}}"
  key = "{{.Key}}"
  [servers.http3]
  enabled = true
{{template "locs" (locs .)}}`

type locSpec struct {
	Path, Action string
	Cache        bool
}

type locsData struct {
	P    cfgParams
	Locs []locSpec
}

func renderConfig(p cfgParams) ([]byte, error) {
	t, err := template.New("cfg").Funcs(template.FuncMap{
		"locs": func(p cfgParams) locsData {
			return locsData{P: p, Locs: []locSpec{
				{Path: "/proxy/", Action: `proxy_pass = "http://origin"`},
				{Path: "/admitted/", Action: `proxy_pass = "http://limited"`},
				{Path: "/cached/", Action: `proxy_pass = "http://origin"`, Cache: true},
				{Path: "/static/", Action: fmt.Sprintf(`root = %q`, p.WWW)},
			}}
		},
	}).Parse(cfgTemplate)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, p); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// ---------------------------------------------------------------- process + sampling

type julProc struct {
	cmd          *exec.Cmd
	pid          int
	cgroup       string
	cfgPath      string
	tmpDir       string
	p            cfgParams
	logf         *os.File
	exited       chan struct{}
	exitErr      error
	limitChecked bool
}

type env struct {
	jul, plugin, out, www, cert, key string
	julCPUs, loadCPUs, originCPUs    string
	cpuQuota, memMax                 string
	origin                           string
	originCmd                        *exec.Cmd
	harness                          string
	nextPort                         int
}

func (e *env) ports() (int, int, int) {
	e.nextPort += 10
	return e.nextPort, e.nextPort + 1, e.nextPort + 2
}

func (e *env) startJul(name string, p cfgParams) (*julProc, error) {
	dir := filepath.Join(e.out, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("/tmp", "a695-spill-")
	if err != nil {
		return nil, err
	}
	p.Port, p.TLSPort, p.AdminPort = e.ports()
	p.Origin, p.WWW, p.Plugin, p.Cert, p.Key = e.origin, e.www, e.plugin, e.cert, e.key
	if p.NameA == "" {
		p.NameA = "a"
	}
	if p.WAFRuleID == 0 {
		p.WAFRuleID = 1695101
	}
	cfg, err := renderConfig(p)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(dir, "jul.toml")
	if err := os.WriteFile(cfgPath, cfg, 0o644); err != nil {
		return nil, err
	}
	unit := fmt.Sprintf("a695-%s-%d", strings.ReplaceAll(name, "/", "-"), time.Now().UnixNano()%1e6)
	logf, err := os.Create(filepath.Join(dir, "jul.log"))
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("systemd-run", "--user", "--scope", "--quiet", "--unit="+unit,
		"-p", "CPUQuota="+e.cpuQuota, "-p", "MemoryMax="+e.memMax, "-p", "MemorySwapMax=0",
		"taskset", "-c", e.julCPUs, e.jul, "-config", cfgPath)
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "GOMAXPROCS=2")
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	j := &julProc{cmd: cmd, pid: cmd.Process.Pid, cfgPath: cfgPath, tmpDir: tmp, p: p, logf: logf, exited: make(chan struct{})}
	go func() { j.exitErr = cmd.Wait(); close(j.exited) }()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-j.exited:
			return nil, fmt.Errorf("jul exited during startup: %v (see %s)", j.exitErr, logf.Name())
		default:
		}
		if j.cgroup == "" {
			if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", j.pid)); err == nil {
				if i := strings.LastIndex(string(b), "::"); i >= 0 {
					cg := "/sys/fs/cgroup" + strings.TrimSpace(string(b)[i+2:])
					if strings.Contains(cg, unit) {
						j.cgroup = cg
					}
				}
			}
		}
		if j.cgroup != "" && !j.limitChecked {
			b, err := os.ReadFile(filepath.Join(j.cgroup, "memory.max"))
			if err != nil || strings.TrimSpace(string(b)) == "max" {
				j.stop()
				return nil, fmt.Errorf("memory scope %s has no memory.max limit (%q, %v); refusing to run", j.cgroup, strings.TrimSpace(string(b)), err)
			}
			j.limitChecked = true
		}
		if j.cgroup != "" && probeOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", p.Port)) {
			return j, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	j.stop()
	return nil, fmt.Errorf("jul did not become ready (see %s)", logf.Name())
}

func probeOK(url string) bool {
	c := &http.Client{Timeout: time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 204
}

func (j *julProc) stop() {
	if j.cmd.Process != nil {
		_ = j.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-j.exited:
		case <-time.After(15 * time.Second):
			_ = j.cmd.Process.Kill()
			<-j.exited
		}
	}
	j.logf.Close()
	os.RemoveAll(j.tmpDir)
}

func (j *julProc) reload(p cfgParams) error {
	p.Port, p.TLSPort, p.AdminPort = j.p.Port, j.p.TLSPort, j.p.AdminPort
	p.Origin, p.WWW, p.Plugin, p.Cert, p.Key = j.p.Origin, j.p.WWW, j.p.Plugin, j.p.Cert, j.p.Key
	cfg, err := renderConfig(p)
	if err != nil {
		return err
	}
	if err := os.WriteFile(j.cfgPath, cfg, 0o644); err != nil {
		return err
	}
	j.p = p
	return j.cmd.Process.Signal(syscall.SIGHUP)
}

func readInt(path string) int64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return -1
	}
	return v
}

func readKV(path string) map[string]int64 {
	out := map[string]int64{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			v, _ := strconv.ParseInt(f[1], 10, 64)
			out[f[0]] = v
		}
	}
	return out
}

func procStatus(pid int) map[string]int64 {
	out := map[string]int64{}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(v)
		if len(f) == 0 {
			continue
		}
		n, err := strconv.ParseInt(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			n *= 1024
		}
		out[k] = n
	}
	return out
}

// scrape returns the admin /metrics exposition as series -> value.
func scrape(adminPort int) (map[string]float64, error) {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/metrics", adminPort), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("metrics status %d", resp.StatusCode)
	}
	out := map[string]float64{}
	sc := bufio.NewScanner(resp.Body)
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
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			continue
		}
		out[line[:i]] = v
	}
	return out, sc.Err()
}

// sum adds every series of metric whose labels contain all of the filters.
func sum(m map[string]float64, metric string, filters ...string) float64 {
	var t float64
	for k, v := range m {
		name, labels, _ := strings.Cut(k, "{")
		if name != metric {
			continue
		}
		ok := true
		for _, f := range filters {
			if !strings.Contains(labels, f) {
				ok = false
				break
			}
		}
		if ok {
			t += v
		}
	}
	return t
}

type sample struct {
	T          float64 `json:"t"`
	Phase      string  `json:"phase"`
	CgMem      int64   `json:"cg_mem"`
	CgAnon     int64   `json:"cg_anon"`
	CgShmem    int64   `json:"cg_shmem"`
	CgFile     int64   `json:"cg_file"`
	CPUUsec    int64   `json:"cpu_usec"`
	RSS        int64   `json:"rss"`
	Threads    int64   `json:"threads"`
	FDs        int     `json:"fds"`
	SpillFiles int     `json:"spill_files"`
	SpillBytes int64   `json:"spill_bytes"`
	Goroutines float64 `json:"goroutines"`
	HeapInuse  float64 `json:"heap_inuse"`
	HeapSys    float64 `json:"heap_sys"`
	GCCount    float64 `json:"gc_count"`
	InFlight   float64 `json:"in_flight"`
	PluginInst float64 `json:"plugin_instances"`
	UpActive   float64 `json:"upstream_active_limited"`
	UpPending  float64 `json:"upstream_pending_limited"`
	ScrapeErr  string  `json:"scrape_err,omitempty"`
	OOMKill    int64   `json:"oom_kill"`
}

type sampler struct {
	j     *julProc
	start time.Time
	mu    sync.Mutex
	phase string
	out   []sample
	stop  chan struct{}
	done  chan struct{}
}

func newSampler(j *julProc) *sampler {
	s := &sampler{j: j, start: time.Now(), phase: "baseline", stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *sampler) setPhase(p string) { s.mu.Lock(); s.phase = p; s.mu.Unlock() }

func (s *sampler) take() sample {
	j := s.j
	ms := readKV(filepath.Join(j.cgroup, "memory.stat"))
	cs := readKV(filepath.Join(j.cgroup, "cpu.stat"))
	ev := readKV(filepath.Join(j.cgroup, "memory.events"))
	st := procStatus(j.pid)
	fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", j.pid))
	var spillN int
	var spillB int64
	if ents, err := os.ReadDir(j.tmpDir); err == nil {
		for _, e := range ents {
			if strings.HasPrefix(e.Name(), "body") {
				spillN++
				if fi, err := e.Info(); err == nil {
					spillB += fi.Size()
				}
			}
		}
	}
	s.mu.Lock()
	ph := s.phase
	s.mu.Unlock()
	sm := sample{
		T: time.Since(s.start).Seconds(), Phase: ph,
		CgMem: readInt(filepath.Join(j.cgroup, "memory.current")), CgAnon: ms["anon"], CgShmem: ms["shmem"], CgFile: ms["file"],
		CPUUsec: cs["usage_usec"], RSS: st["VmRSS"], Threads: st["Threads"], FDs: len(fds),
		SpillFiles: spillN, SpillBytes: spillB, OOMKill: ev["oom_kill"],
	}
	if m, err := scrape(j.p.AdminPort); err != nil {
		sm.ScrapeErr = err.Error()
	} else {
		sm.Goroutines = sum(m, "go_goroutines")
		sm.HeapInuse = sum(m, "go_memstats_heap_inuse_bytes")
		sm.HeapSys = sum(m, "go_memstats_heap_sys_bytes")
		sm.GCCount = sum(m, "go_gc_duration_seconds_count")
		sm.InFlight = sum(m, "jul_http_requests_in_flight")
		sm.PluginInst = sum(m, "jul_plugin_instances")
		sm.UpActive = sum(m, "jul_upstream_active_requests", `pool="limited"`)
		sm.UpPending = sum(m, "jul_upstream_pending_requests", `pool="limited"`)
	}
	return sm
}

func (s *sampler) loop() {
	defer close(s.done)
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		sm := s.take()
		s.mu.Lock()
		s.out = append(s.out, sm)
		s.mu.Unlock()
		select {
		case <-s.stop:
			return
		case <-s.j.exited:
			return
		case <-t.C:
		}
	}
}

func (s *sampler) finish() []sample {
	close(s.stop)
	<-s.done
	return s.out
}

// ---------------------------------------------------------------- load

type workload struct {
	Proto     string        `json:"proto"` // h1 | h2 | h3
	Method    string        `json:"method"`
	Path      string        `json:"path"` // includes query
	Body      int           `json:"body_bytes"`
	Resp      int           `json:"resp_bytes"`
	Conc      int           `json:"concurrency"`
	SlowBPS   int           `json:"slow_bytes_per_sec,omitempty"`
	CancelAt  time.Duration `json:"cancel_after,omitempty"`
	Timeout   time.Duration `json:"timeout"`
	UniqueKey bool          `json:"unique_key,omitempty"`
	// Shape selects the JSON body: "wide" (2 KiB string fields, about one
	// argument per 2 KiB) or "items" (about three arguments per 66 bytes).
	Shape    string `json:"body_shape,omitempty"`
	WantWASM bool   `json:"want_wasm_evidence"`
	WantEcho bool   `json:"want_origin_echo"`
}

type result struct {
	status  int
	lat     time.Duration
	err     string
	unverif string
}

type stats struct {
	Offered      int            `json:"offered"`
	Completed    int            `json:"completed_2xx_verified"`
	Unverified   int            `json:"completed_2xx_unverified"`
	Statuses     map[string]int `json:"statuses"`
	Errors       map[string]int `json:"errors"`
	Cancelled    int            `json:"cancelled_by_client"`
	Throughput   float64        `json:"verified_rps"`
	P50ms        float64        `json:"p50_ms"`
	P90ms        float64        `json:"p90_ms"`
	P99ms        float64        `json:"p99_ms"`
	Maxms        float64        `json:"max_ms"`
	UnverifiedEx []string       `json:"unverified_examples,omitempty"`
}

func newClient(proto string, port, tlsPort, conc int, timeout time.Duration) (*http.Client, string, func()) {
	tlsCfg := &tls.Config{InsecureSkipVerify: true, ServerName: "localhost"} // loopback test certificate
	switch proto {
	case "h2":
		tr := &http2.Transport{TLSClientConfig: tlsCfg, DisableCompression: true}
		return &http.Client{Transport: tr, Timeout: timeout}, fmt.Sprintf("https://localhost:%d", tlsPort), tr.CloseIdleConnections
	case "h3":
		tr := &http3.Transport{TLSClientConfig: tlsCfg}
		return &http.Client{Transport: tr, Timeout: timeout}, fmt.Sprintf("https://localhost:%d", tlsPort), func() { tr.Close() }
	default:
		tr := &http.Transport{MaxConnsPerHost: conc, MaxIdleConnsPerHost: conc, DisableCompression: true, ForceAttemptHTTP2: false}
		return &http.Client{Transport: tr, Timeout: timeout}, fmt.Sprintf("http://127.0.0.1:%d", port), tr.CloseIdleConnections
	}
}

// jsonBody returns a benign JSON document of exactly n bytes shaped like an
// API payload (many short fields), optionally carrying a marker.
func jsonBody(n int, marker string) []byte {
	var b bytes.Buffer
	b.Grow(n + 64)
	b.WriteString(`{"marker":"` + marker + `","items":[`)
	for i := 0; ; i++ {
		item := fmt.Sprintf(`{"id":%d,"name":"item-%d","note":"synthetic assessment payload"},`, i, i)
		if b.Len()+len(item)+2 > n {
			break
		}
		b.WriteString(item)
	}
	out := b.Bytes()
	if out[len(out)-1] == ',' {
		out = out[:len(out)-1]
	}
	out = append(out, "]}"...)
	for len(out) < n {
		out = append(out[:len(out)-1], ' ', '}')
	}
	return out
}

// wideJSONBody returns about n bytes of JSON made of 2 KiB string fields so the
// CRS argument count stays far below Coraza's 1000-argument limit.
func wideJSONBody(n int, marker string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"marker":"` + marker + `","fields":[`)
	field := strings.Repeat("synthetic assessment text ", 79)[:2000]
	for b.Len()+len(field)+8 < n {
		if b.Bytes()[b.Len()-1] == '"' {
			b.WriteByte(',')
		}
		b.WriteString(`"` + field + `"`)
	}
	b.WriteString("]}")
	return b.Bytes()
}

func bodyFor(w workload) []byte {
	if w.Body <= 0 {
		return nil
	}
	if w.Shape == "items" {
		return jsonBody(w.Body, "")
	}
	return wideJSONBody(w.Body, "")
}

type slowReader struct {
	data []byte
	off  int
	bps  int
	ctx  context.Context
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.off >= len(s.data) {
		return 0, io.EOF
	}
	chunk := min(1024, len(p), len(s.data)-s.off)
	select {
	case <-time.After(time.Duration(float64(chunk) / float64(s.bps) * float64(time.Second))):
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	}
	n := copy(p, s.data[s.off:s.off+chunk])
	s.off += n
	return n, nil
}

var keySeq atomic.Int64

func doOne(ctx context.Context, c *http.Client, base string, w workload, body []byte) result {
	path := w.Path
	if w.UniqueKey {
		path += fmt.Sprintf("&k=%d", keySeq.Add(1))
	}
	if w.CancelAt > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.CancelAt)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		if w.SlowBPS > 0 {
			rd = &slowReader{data: body, bps: w.SlowBPS, ctx: ctx}
		} else {
			rd = bytes.NewReader(body)
		}
	}
	req, err := http.NewRequestWithContext(ctx, w.Method, base+path, rd)
	if err != nil {
		return result{err: "build"}
	}
	// A hostname avoids CRS 920350 (numeric Host) so clean traffic stays clean.
	req.Host = "localhost"
	req.Header.Set("User-Agent", "jul-695-assessment")
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", "jul-695-assessment")
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		lat := time.Since(start)
		if errors.Is(err, context.DeadlineExceeded) && w.CancelAt > 0 {
			return result{err: "client-cancel", lat: lat}
		}
		return result{err: classify(err), lat: lat}
	}
	n, rerr := io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	r := result{status: resp.StatusCode, lat: time.Since(start)}
	if rerr != nil {
		if errors.Is(rerr, context.DeadlineExceeded) && w.CancelAt > 0 {
			r.err = "client-cancel"
		} else {
			r.err = "body:" + classify(rerr)
		}
		return r
	}
	if resp.StatusCode/100 == 2 {
		r.unverif = verify(resp, n, w)
	}
	return r
}

func verify(resp *http.Response, n int64, w workload) string {
	if w.Resp > 0 && n != int64(w.Resp) {
		return fmt.Sprintf("resp-len %d!=%d", n, w.Resp)
	}
	h := resp.Header
	if w.WantEcho && w.Body > 0 && h.Get("X-Origin-Req-Len") != strconv.Itoa(w.Body) {
		return "origin-req-len " + h.Get("X-Origin-Req-Len")
	}
	if w.WantWASM {
		want := strconv.Itoa(w.Resp)
		if h.Get("X-Assess-a-Resp") == "" && h.Get("X-Assess-a1-Resp") == "" && h.Get("X-Assess-a2-Resp") == "" {
			return "missing a-resp"
		}
		if v := h.Get("X-Assess-b-Resp"); v != want {
			return "b-resp " + v
		}
		if w.WantEcho && w.Body > 0 && h.Get("X-Echo-B-Req-Len") != strconv.Itoa(w.Body) {
			return "b-req-len " + h.Get("X-Echo-B-Req-Len")
		}
	}
	return ""
}

func classify(err error) string {
	s := err.Error()
	switch {
	case strings.Contains(s, "Client.Timeout") || errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case strings.Contains(s, "connection reset"):
		return "reset"
	case strings.Contains(s, "EOF"):
		return "eof"
	case strings.Contains(s, "refused"):
		return "refused"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// drive runs a closed-loop workload until ctx ends. Only results whose request
// started inside [measureFrom, measureTo) are recorded; such requests still in
// flight when ctx ends are recorded as unfinished.
func drive(ctx context.Context, w workload, j *julProc, measureFrom, measureTo time.Time) stats {
	c, base, closeFn := newClient(w.Proto, j.p.Port, j.p.TLSPort, w.Conc, w.Timeout)
	defer closeFn()
	body := bodyFor(w)
	if body != nil {
		w.Body = len(body)
	}
	w.WantWASM = j.p.WASM
	var mu sync.Mutex
	var res []result
	var wg sync.WaitGroup
	for i := 0; i < w.Conc; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				st := time.Now()
				r := doOne(ctx, c, base, w, body)
				inWindow := st.After(measureFrom) && st.Before(measureTo)
				if ctx.Err() != nil && r.err != "" {
					// Still in flight when the phase ended: starvation, not success.
					if inWindow {
						mu.Lock()
						res = append(res, result{err: "unfinished-at-phase-end"})
						mu.Unlock()
					}
					return
				}
				if inWindow {
					mu.Lock()
					res = append(res, r)
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
	return summarize(res, measureTo.Sub(measureFrom))
}

func summarize(res []result, window time.Duration) stats {
	s := stats{Offered: len(res), Statuses: map[string]int{}, Errors: map[string]int{}}
	var lats []float64
	for _, r := range res {
		if r.err == "client-cancel" {
			s.Cancelled++
			continue
		}
		if r.err != "" {
			s.Errors[r.err]++
			continue
		}
		s.Statuses[strconv.Itoa(r.status)]++
		if r.status/100 == 2 {
			if r.unverif != "" {
				s.Unverified++
				if len(s.UnverifiedEx) < 5 {
					s.UnverifiedEx = append(s.UnverifiedEx, r.unverif)
				}
				continue
			}
			s.Completed++
			lats = append(lats, float64(r.lat.Microseconds())/1000)
		}
	}
	sort.Float64s(lats)
	q := func(p float64) float64 {
		if len(lats) == 0 {
			return 0
		}
		return lats[min(len(lats)-1, int(math.Ceil(p*float64(len(lats))))-1)]
	}
	s.P50ms, s.P90ms, s.P99ms = q(0.50), q(0.90), q(0.99)
	if len(lats) > 0 {
		s.Maxms = lats[len(lats)-1]
	}
	s.Throughput = float64(s.Completed) / window.Seconds()
	return s
}

// ---------------------------------------------------------------- scenarios

type phaseSpec struct {
	Name    string        `json:"name"`
	Warmup  time.Duration `json:"warmup"`
	Measure time.Duration `json:"measure"`
	Load    []workload    `json:"workloads"`
}

type canary struct {
	Name   string `json:"name"`
	Status int    `json:"status"`
	Want   int    `json:"want"`
	OK     bool   `json:"ok"`
}

type runResult struct {
	Scenario     string             `json:"scenario"`
	Config       string             `json:"config"`
	Rep          int                `json:"rep"`
	Phases       []phaseOut         `json:"phases"`
	Canaries     []canary           `json:"canaries"`
	Metrics      map[string]float64 `json:"metric_deltas"`
	OriginCalls  int64              `json:"origin_calls"`
	Baseline     sample             `json:"baseline"`
	Peak         peak               `json:"peak"`
	Final        sample             `json:"final_after_recovery"`
	PostGCHeap   float64            `json:"post_gc_heap_inuse"`
	CgMemPeak    int64              `json:"cgroup_memory_peak"`
	OOMKilled    bool               `json:"oom_killed"`
	JulExited    string             `json:"jul_exited,omitempty"`
	CPUSeconds   float64            `json:"jul_cpu_seconds_measure"`
	Reloads      map[string]float64 `json:"reloads,omitempty"`
	Notes        []string           `json:"notes,omitempty"`
	SamplesFile  string             `json:"samples_file"`
	ConfigSHA256 string             `json:"config_sha256"`
}

type phaseOut struct {
	Name     string     `json:"name"`
	Stats    []stats    `json:"stats"`
	Loads    []workload `json:"workloads"`
	CPUSec   float64    `json:"jul_cpu_seconds"`
	Seconds  float64    `json:"seconds"`
	PeakMem  int64      `json:"peak_cg_mem"`
	PeakInst float64    `json:"peak_plugin_instances"`
}

type peak struct {
	CgMem, CgAnon, CgShmem, RSS int64
	FDs, SpillFiles             int
	SpillBytes                  int64
	Goroutines, HeapInuse       float64
	InFlight, PluginInst        float64
	UpActive, UpPending         float64
	Threads                     int64
}

func peakOf(ss []sample) peak {
	var p peak
	for _, s := range ss {
		p.CgMem = max(p.CgMem, s.CgMem)
		p.CgAnon = max(p.CgAnon, s.CgAnon)
		p.CgShmem = max(p.CgShmem, s.CgShmem)
		p.RSS = max(p.RSS, s.RSS)
		p.FDs = max(p.FDs, s.FDs)
		p.SpillFiles = max(p.SpillFiles, s.SpillFiles)
		p.SpillBytes = max(p.SpillBytes, s.SpillBytes)
		p.Goroutines = max(p.Goroutines, s.Goroutines)
		p.HeapInuse = max(p.HeapInuse, s.HeapInuse)
		p.InFlight = max(p.InFlight, s.InFlight)
		p.PluginInst = max(p.PluginInst, s.PluginInst)
		p.UpActive = max(p.UpActive, s.UpActive)
		p.UpPending = max(p.UpPending, s.UpPending)
		p.Threads = max(p.Threads, s.Threads)
	}
	return p
}

func originCount(e *env) int64 {
	resp, err := http.Get("http://" + e.origin + "/__count")
	if err != nil {
		return -1
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	v, _ := strconv.ParseInt(string(b), 10, 64)
	return v
}

func postGCHeap(j *julProc) float64 {
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/debug/pprof/heap?gc=1&debug=1", j.p.AdminPort), nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	c := &http.Client{Timeout: 10 * time.Second}
	if resp, err := c.Do(req); err == nil {
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
	time.Sleep(300 * time.Millisecond)
	m, err := scrape(j.p.AdminPort)
	if err != nil {
		return -1
	}
	return sum(m, "go_memstats_heap_inuse_bytes")
}

var watchedMetrics = []struct {
	key, metric string
	filters     []string
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

func metricSnapshot(j *julProc) map[string]float64 {
	out := map[string]float64{}
	m, err := scrape(j.p.AdminPort)
	if err != nil {
		return out
	}
	for _, w := range watchedMetrics {
		out[w.key] = sum(m, w.metric, w.filters...)
	}
	for k, v := range m {
		if strings.HasPrefix(k, "jul_reload_total") || strings.HasPrefix(k, "jul_waf_transactions_total") || strings.HasPrefix(k, "jul_plugin_invocations_total") {
			out["raw:"+k] = v
		}
	}
	return out
}

func diff(a, b map[string]float64) map[string]float64 {
	out := map[string]float64{}
	for k, v := range b {
		if d := v - a[k]; d != 0 {
			out[k] = d
		}
	}
	return out
}

// canaries prove the configured inspection actually executes on this binary
// and configuration: each marker must be blocked by its owning layer.
func runCanaries(j *julProc, p cfgParams) []canary {
	c, base, closeFn := newClient("h1", j.p.Port, j.p.TLSPort, 1, 10*time.Second)
	defer closeFn()
	do := func(name, method, path string, body []byte, want int) canary {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		req.Host = "localhost"
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/html")
		req.Header.Set("User-Agent", "jul-695-assessment")
		resp, err := c.Do(req)
		st := -1
		if err == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			st = resp.StatusCode
		}
		return canary{Name: name, Status: st, Want: want, OK: st == want}
	}
	var out []canary
	clean := 200
	out = append(out, do("clean-proxy", "POST", "/proxy/echo?resp=4096", jsonBody(2048, ""), clean))
	if p.WAF {
		out = append(out, do("waf-request-body", "POST", "/proxy/echo?resp=4096", jsonBody(2048, wafReqMark), 403))
		out = append(out, do("waf-response-body", "GET", "/proxy/echo?resp=4096&canary=waf", nil, 403))
		out = append(out, do("waf-static-response-clean", "GET", "/static/page.html", nil, 200))
	}
	if p.WASM {
		out = append(out, do("wasm-request-body", "POST", "/proxy/echo?resp=4096", jsonBody(2048, wasmReqMark), 403))
		out = append(out, do("wasm-on-static", "POST", "/static/page.html", jsonBody(2048, wasmReqMark), 403))
	}
	return out
}

type scenario struct {
	name   string
	cfg    cfgParams
	phases []phaseSpec
	during func(ctx context.Context, j *julProc, r *runResult) // optional concurrent action
}

func (e *env) runScenario(sc scenario, cfgName string, rep int, recovery time.Duration) runResult {
	name := fmt.Sprintf("%s/%s/r%d", sc.name, cfgName, rep)
	r := runResult{Scenario: sc.name, Config: cfgName, Rep: rep}
	j, err := e.startJul(name, sc.cfg)
	if err != nil {
		r.Notes = append(r.Notes, "start failed: "+err.Error())
		return r
	}
	defer j.stop()
	if b, err := os.ReadFile(j.cfgPath); err == nil {
		h := sha256.Sum256(b)
		r.ConfigSHA256 = hex.EncodeToString(h[:])
	}
	r.Canaries = runCanaries(j, sc.cfg)
	time.Sleep(time.Second)
	smp := newSampler(j)
	time.Sleep(2 * time.Second)
	m0 := metricSnapshot(j)
	o0 := originCount(e)
	var duringDone chan struct{}
	for _, ph := range sc.phases {
		smp.setPhase(ph.Name + ":warmup")
		ctx, cancel := context.WithTimeout(context.Background(), ph.Warmup+ph.Measure)
		from := time.Now().Add(ph.Warmup)
		to := from.Add(ph.Measure)
		go func() { time.Sleep(ph.Warmup); smp.setPhase(ph.Name) }()
		if sc.during != nil && duringDone == nil {
			duringDone = make(chan struct{})
			go func() { defer close(duringDone); sc.during(ctx, j, &r) }()
		}
		cpu0 := readKV(filepath.Join(j.cgroup, "cpu.stat"))["usage_usec"]
		var wg sync.WaitGroup
		out := phaseOut{Name: ph.Name, Loads: ph.Load, Stats: make([]stats, len(ph.Load))}
		var cpuFrom, cpuTo int64
		go func() {
			time.Sleep(ph.Warmup)
			cpuFrom = readKV(filepath.Join(j.cgroup, "cpu.stat"))["usage_usec"]
			time.Sleep(ph.Measure)
			cpuTo = readKV(filepath.Join(j.cgroup, "cpu.stat"))["usage_usec"]
		}()
		for i, w := range ph.Load {
			wg.Add(1)
			go func() { defer wg.Done(); out.Stats[i] = drive(ctx, w, j, from, to) }()
		}
		wg.Wait()
		cancel()
		_ = cpu0
		time.Sleep(50 * time.Millisecond)
		out.CPUSec = float64(cpuTo-cpuFrom) / 1e6
		out.Seconds = ph.Measure.Seconds()
		r.CPUSeconds += out.CPUSec
		r.Phases = append(r.Phases, out)
	}
	if duringDone != nil {
		<-duringDone
	}
	smp.setPhase("recovery")
	select {
	case <-time.After(recovery):
	case <-j.exited:
	}
	m1 := metricSnapshot(j)
	r.Metrics = diff(m0, m1)
	r.OriginCalls = originCount(e) - o0
	select {
	case <-j.exited:
		r.JulExited = fmt.Sprint(j.exitErr)
	default:
		r.PostGCHeap = postGCHeap(j)
		if !probeOK(fmt.Sprintf("http://127.0.0.1:%d/healthz", j.p.Port)) {
			r.Notes = append(r.Notes, "post-recovery health probe failed")
		}
		r.Canaries = append(r.Canaries, prefixCanaries("post-", runCanaries(j, sc.cfg))...)
	}
	smp.setPhase("final")
	samples := smp.finish()
	r.CgMemPeak = readInt(filepath.Join(j.cgroup, "memory.peak"))
	r.OOMKilled = readKV(filepath.Join(j.cgroup, "memory.events"))["oom_kill"] > 0
	if len(samples) > 0 {
		r.Baseline = samples[0]
		r.Final = samples[len(samples)-1]
	}
	var loadSamples []sample
	for _, s := range samples {
		if s.Phase != "baseline" && s.Phase != "final" {
			loadSamples = append(loadSamples, s)
		}
	}
	r.Peak = peakOf(loadSamples)
	for i := range r.Phases {
		var ps []sample
		for _, s := range samples {
			if s.Phase == r.Phases[i].Name {
				ps = append(ps, s)
			}
		}
		pk := peakOf(ps)
		r.Phases[i].PeakMem, r.Phases[i].PeakInst = pk.CgMem, pk.PluginInst
	}
	sf := filepath.Join(e.out, name, "samples.json")
	if b, err := json.Marshal(samples); err == nil {
		_ = os.WriteFile(sf, b, 0o644)
	}
	r.SamplesFile, _ = filepath.Rel(e.out, sf)
	return r
}

func prefixCanaries(p string, cs []canary) []canary {
	for i := range cs {
		cs[i].Name = p + cs[i].Name
	}
	return cs
}

func configs() []struct {
	name string
	p    cfgParams
} {
	return []struct {
		name string
		p    cfgParams
	}{
		{"none", cfgParams{}},
		{"waf", cfgParams{WAF: true}},
		{"wasm", cfgParams{WASM: true}},
		{"both", cfgParams{WAF: true, WASM: true}},
	}
}

func pick(names ...string) func(string) bool {
	return func(n string) bool {
		for _, x := range names {
			if x == n {
				return true
			}
		}
		return false
	}
}

func scenarios(quick bool) []struct {
	sc      scenario
	cfgs    func(string) bool
	reps    int
	recover time.Duration
} {
	warm, meas, rec := 5*time.Second, 20*time.Second, 10*time.Second
	if quick {
		warm, meas, rec = 1*time.Second, 3*time.Second, 2*time.Second
	}
	reps := 3
	if quick {
		reps = 1
	}
	norm := func(proto string) workload {
		return workload{Proto: proto, Method: "POST", Path: "/proxy/echo?resp=65536", Body: 16 << 10, Resp: 64 << 10, Conc: 16, Timeout: 30 * time.Second, WantEcho: true}
	}
	stress := workload{Proto: "h2", Method: "POST", Path: "/proxy/echo?resp=1048576&chunked=1", Body: 256 << 10, Resp: 1 << 20, Conc: 32, Timeout: 60 * time.Second, WantEcho: true}
	type entry = struct {
		sc      scenario
		cfgs    func(string) bool
		reps    int
		recover time.Duration
	}
	all := func(string) bool { return true }
	var out []entry
	out = append(out,
		entry{scenario{name: "s1-normal-h1", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{norm("h1")}}}}, all, reps, rec},
		entry{scenario{name: "s2-normal-h2", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{norm("h2")}}}}, all, reps, rec},
		entry{scenario{name: "s3-large-h2", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{stress}}}}, all, reps, rec},
	)
	// Ramp: concurrency steps of large inspected bodies on one H2 connection.
	var ramp []phaseSpec
	for _, c := range []int{16, 32, 64, 128, 192} {
		w := stress
		w.Conc = c
		ramp = append(ramp, phaseSpec{Name: fmt.Sprintf("c%d", c), Warmup: 2 * time.Second, Measure: meas / 2, Load: []workload{w}})
	}
	out = append(out, entry{scenario{name: "s4-ramp-large-h2", phases: ramp}, pick("none", "both"), 1, rec})
	// Admission: limited pool (8 active / 16 pending / 2s) behind slow origin.
	adm := workload{Proto: "h2", Method: "POST", Path: "/admitted/echo?resp=65536&delay=500", Body: 256 << 10, Resp: 64 << 10, Conc: 128, Timeout: 30 * time.Second, WantEcho: true}
	out = append(out, entry{scenario{name: "s5-admission-h2", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{adm}}}}, pick("none", "both"), 1, rec})
	// No-upstream route: static file through the same inspection layers.
	st := workload{Proto: "h2", Method: "GET", Path: "/static/page.html?x=1", Resp: 256 << 10, Conc: 128, Timeout: 30 * time.Second}
	out = append(out, entry{scenario{name: "s6-static-h2", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{st}}}}, pick("none", "both"), 1, rec})
	// Cache: cold (unique keys) then warm (one key).
	cold := workload{Proto: "h1", Method: "GET", Path: "/cached/echo?resp=262144", Resp: 256 << 10, Conc: 16, Timeout: 30 * time.Second, UniqueKey: true}
	warmW := cold
	warmW.UniqueKey = false
	warmW.Path = "/cached/echo?resp=262144&k=warm"
	out = append(out, entry{scenario{name: "s7-cache-h1", cfg: cfgParams{Cache: true}, phases: []phaseSpec{
		{Name: "cold", Warmup: warm, Measure: meas, Load: []workload{cold}},
		{Name: "warm", Warmup: warm, Measure: meas, Load: []workload{warmW}},
	}}, pick("none", "both"), 1, rec})
	// Slow uploaders hold inspection resources while fast probes continue.
	slow := workload{Proto: "h1", Method: "POST", Path: "/proxy/echo?resp=4096", Body: 64 << 10, Resp: 4096, Conc: 96, SlowBPS: 16 << 10, Timeout: 60 * time.Second, WantEcho: true}
	slowCancel := slow
	slowCancel.Conc, slowCancel.CancelAt = 32, 2*time.Second
	probe := workload{Proto: "h1", Method: "POST", Path: "/proxy/echo?resp=4096", Body: 2 << 10, Resp: 4096, Conc: 4, Timeout: 30 * time.Second, WantEcho: true}
	out = append(out, entry{scenario{name: "s8-slow-clients-h1", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{slow, slowCancel, probe}}}}, all, 1, rec})
	// Lifecycle: real policy reloads, one failed preparation, slow in-flight work.
	life := norm("h1")
	life.Conc = 16
	slowInflight := workload{Proto: "h2", Method: "POST", Path: "/proxy/echo?resp=65536&delay=3000", Body: 16 << 10, Resp: 64 << 10, Conc: 16, Timeout: 30 * time.Second, WantEcho: true}
	lifeMeas := 40 * time.Second
	if quick {
		lifeMeas = 8 * time.Second
	}
	out = append(out, entry{scenario{name: "s9-reload-churn", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: lifeMeas, Load: []workload{life, slowInflight}}}, during: reloadChurn}, pick("both"), 1, 15 * time.Second})
	// Argument amplification: many-argument JSON at increasing concurrency.
	var amp []phaseSpec
	for _, c := range []int{1, 2, 4, 8, 16} {
		w := norm("h1")
		w.Shape, w.Conc, w.Resp, w.Path = "items", c, 4096, "/proxy/echo?resp=4096"
		amp = append(amp, phaseSpec{Name: fmt.Sprintf("c%d", c), Warmup: 2 * time.Second, Measure: meas / 2, Load: []workload{w}})
	}
	out = append(out, entry{scenario{name: "s11-arg-amplification-h1", phases: amp}, pick("none", "waf"), 1, rec})
	// HTTP/3, reported separately.
	out = append(out, entry{scenario{name: "s10-normal-h3", phases: []phaseSpec{{Name: "load", Warmup: warm, Measure: meas, Load: []workload{norm("h3")}}}}, pick("none", "both"), 1, rec})
	return out
}

// reloadChurn applies real WAF and plugin policy changes every 2s, including
// one invalid candidate, and records the outcomes.
func reloadChurn(ctx context.Context, j *julProc, r *runResult) {
	base := j.p
	i := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		i++
		p := base
		p.NameA = []string{"a1", "a2"}[i%2]
		p.WAFRuleID = 1695101 + i%2*10
		p.BrokenWAF = i == 5
		if err := j.reload(p); err != nil {
			r.Notes = append(r.Notes, "reload signal: "+err.Error())
		}
		r.Notes = append(r.Notes, fmt.Sprintf("t+%ds reload %d nameA=%s rule=%d broken=%v", i*2, i, p.NameA, p.WAFRuleID, p.BrokenWAF))
		if p.BrokenWAF {
			// Restore a valid candidate so later churn continues from it.
			base.NameA = p.NameA
		}
	}
}

func runMain(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	e := &env{nextPort: 18000}
	fs.StringVar(&e.jul, "jul", "./jul", "full-profile jul binary")
	fs.StringVar(&e.plugin, "plugin", "", "inspect-load.wasm")
	fs.StringVar(&e.out, "out", "", "output directory")
	fs.StringVar(&e.julCPUs, "jul-cpus", "0,1", "CPUs for Jul")
	fs.StringVar(&e.loadCPUs, "load-cpus", "4-9", "CPUs for the load generator (this process)")
	fs.StringVar(&e.originCPUs, "origin-cpus", "10,11", "CPUs for the origin")
	fs.StringVar(&e.cpuQuota, "cpu-quota", "200%", "Jul scope CPUQuota")
	fs.StringVar(&e.memMax, "mem-max", "768M", "Jul scope MemoryMax")
	only := fs.String("only", "", "comma-separated scenario name prefixes")
	quick := fs.Bool("quick", false, "short exploratory windows, one repetition")
	watchdog := fs.Duration("watchdog", 90*time.Minute, "abort the whole run after this long")
	_ = fs.Parse(args)
	if e.plugin == "" || e.out == "" {
		fmt.Fprintln(os.Stderr, "-plugin and -out are required")
		os.Exit(2)
	}
	for _, p := range []*string{&e.jul, &e.plugin, &e.out} {
		a, _ := filepath.Abs(*p)
		*p = a
	}
	time.AfterFunc(*watchdog, func() {
		fmt.Fprintln(os.Stderr, "watchdog: aborting")
		os.Exit(3)
	})
	runtime.GOMAXPROCS(6)
	if err := os.MkdirAll(e.out, 0o755); err != nil {
		panic(err)
	}
	repo, _ := os.Getwd()
	e.cert, e.key = filepath.Join(repo, "testdata/tls/localhost.crt"), filepath.Join(repo, "testdata/tls/localhost.key")
	e.www = filepath.Join(e.out, "www")
	_ = os.MkdirAll(filepath.Join(e.www, "static"), 0o755)
	_ = os.WriteFile(filepath.Join(e.www, "static", "page.html"), filler(256<<10, ""), 0o644)
	exe, _ := os.Executable()
	e.harness = exe
	e.origin = "127.0.0.1:19001"
	e.originCmd = exec.Command("taskset", "-c", e.originCPUs, exe, "origin", "-listen", e.origin)
	e.originCmd.Env = append(os.Environ(), "GOMAXPROCS=2")
	e.originCmd.Stderr = os.Stderr
	if err := e.originCmd.Start(); err != nil {
		panic(err)
	}
	defer e.originCmd.Process.Kill()
	time.Sleep(500 * time.Millisecond)
	_ = exec.Command("taskset", "-p", "-c", e.loadCPUs, strconv.Itoa(os.Getpid())).Run()

	manifest := map[string]any{"started": time.Now().UTC().Format(time.RFC3339), "args": os.Args, "quick": *quick,
		"jul_sha256": fileSHA(e.jul), "plugin_sha256": fileSHA(e.plugin), "jul_cpus": e.julCPUs, "load_cpus": e.loadCPUs,
		"origin_cpus": e.originCPUs, "cpu_quota": e.cpuQuota, "mem_max": e.memMax}
	var results []runResult
	resF := filepath.Join(e.out, "results.jsonl")
	f, err := os.Create(resF)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	for _, ent := range scenarios(*quick) {
		if *only != "" && !hasPrefixAny(ent.sc.name, strings.Split(*only, ",")) {
			continue
		}
		for _, c := range configs() {
			if !ent.cfgs(c.name) {
				continue
			}
			for rep := 1; rep <= ent.reps; rep++ {
				sc := ent.sc
				cp := c.p
				cp.Cache = sc.cfg.Cache
				sc.cfg = cp
				fmt.Fprintf(os.Stderr, "%s %s/%s r%d\n", time.Now().Format("15:04:05"), sc.name, c.name, rep)
				r := e.runScenario(sc, c.name, rep, ent.recover)
				results = append(results, r)
				b, _ := json.Marshal(r)
				f.Write(append(b, '\n'))
				fmt.Fprintln(os.Stderr, "  ", oneLine(r))
			}
		}
	}
	manifest["finished"] = time.Now().UTC().Format(time.RFC3339)
	manifest["results_sha256"] = fileSHA(resF)
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	_ = os.WriteFile(filepath.Join(e.out, "manifest.json"), mb, 0o644)
}

func hasPrefixAny(s string, ps []string) bool {
	for _, p := range ps {
		if strings.HasPrefix(s, strings.TrimSpace(p)) {
			return true
		}
	}
	return false
}

func fileSHA(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func oneLine(r runResult) string {
	var parts []string
	for _, ph := range r.Phases {
		for i, s := range ph.Stats {
			parts = append(parts, fmt.Sprintf("%s[%d] ok=%d unv=%d st=%v err=%v canc=%d rps=%.1f p99=%.0fms", ph.Name, i, s.Completed, s.Unverified, s.Statuses, s.Errors, s.Cancelled, s.Throughput, s.P99ms))
		}
		parts = append(parts, fmt.Sprintf("%s cpu=%.1fs peakMem=%dMiB inst=%.0f", ph.Name, ph.CPUSec, ph.PeakMem>>20, ph.PeakInst))
	}
	bad := 0
	for _, c := range r.Canaries {
		if !c.OK {
			bad++
		}
	}
	return fmt.Sprintf("%s canaryFail=%d cgPeak=%dMiB oom=%v postGC=%.0fMiB final{mem=%dMiB gor=%.0f fds=%d inst=%.0f} exited=%q notes=%v",
		strings.Join(parts, " | "), bad, r.CgMemPeak>>20, r.OOMKilled, r.PostGCHeap/(1<<20), r.Final.CgMem>>20, r.Final.Goroutines, r.Final.FDs, r.Final.PluginInst, r.JulExited, r.Notes)
}
