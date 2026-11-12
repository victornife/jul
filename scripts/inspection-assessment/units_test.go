// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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
	nobody := map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "state-4096", "X-Assess-b-Resp": "state-4096", "X-Echo-A-State": "4096", "X-Echo-B-State": "4096"}
	cases := []struct {
		resp *http.Response
		body []byte
		fix  string
		want string
	}{
		{mk(1, echo), good, "", ""},
		{mk(2, echo), good, "", "proto"},
		{mk(1, echo), good[:7], "", "resp-len"},
		{mk(1, echo), append(good[:7:7], 'X'), "", "resp-content"},
		{mk(1, nil), good, "", "origin-req-len"},
		{mk(1, wasm), good, FixtureFull, ""},
		{mk(1, echo), good, FixtureFull, "missing a-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "8", "X-Assess-b-Resp": "7"}), good, FixtureFull, "b-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "8", "X-Assess-b-Resp": "8"}), good, FixtureFull, "b-req-len"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Echo-A-Req-Len": "5", "X-Echo-B-Req-Len": "5"}), good, FixtureRequestOnly, ""},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Echo-A-Req-Len": "4", "X-Echo-B-Req-Len": "5"}), good, FixtureRequestOnly, "a-req-len"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Echo-A-Req-Len": "5"}), good, FixtureRequestOnly, "b-req-len"},
		{mk(1, nobody), good, FixtureNoBody, ""},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a2-Resp": "state-4096", "X-Assess-b-Resp": "state-4096", "X-Echo-A-State": "4096", "X-Echo-B-State": "4096"}), good, FixtureNoBody, ""},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "state-4096", "X-Assess-b-Resp": "state-0", "X-Echo-A-State": "4096", "X-Echo-B-State": "4096"}), good, FixtureNoBody, "b-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-b-Resp": "state-4096", "X-Echo-A-State": "4096", "X-Echo-B-State": "4096"}), good, FixtureNoBody, "a-resp"},
		{mk(1, map[string]string{"X-Origin-Req-Len": "5", "X-Assess-a-Resp": "state-4096", "X-Assess-b-Resp": "state-4096", "X-Echo-B-State": "4096"}), good, FixtureNoBody, "state-echo"},
	}
	for i, tc := range cases {
		got := Verify(tc.resp, tc.body, w, 5, tc.fix, "h1")
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

func TestClientVerifiesPinnedCertificate(t *testing.T) {
	kp, err := tls.LoadX509KeyPair("../../testdata/tls/localhost.crt", "../../testdata/tls/localhost.key")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{kp}, NextProtos: []string{"h2"}}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	tp, _ := strconv.Atoi(port)
	roots, err := TrustedRoots("../../testdata/tls/localhost.crt")
	if err != nil {
		t.Fatal(err)
	}
	get := func(r *Target) error {
		c, err := NewClient("h2", *r, 1, 5*time.Second)
		if err != nil {
			return err
		}
		defer c.Close()
		resp, err := c.HTTP.Get(c.Base + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}
	if err := get(&Target{TLSPort: tp, Roots: roots}); err != nil {
		t.Fatalf("pinned certificate rejected: %v", err)
	}
	if err := get(&Target{TLSPort: tp}); err == nil {
		t.Fatal("unpinned client accepted the self-signed loopback certificate")
	}
	if _, err := TrustedRoots("../../testdata/tls/localhost.key.missing"); err == nil {
		t.Error("missing certificate accepted")
	}
	if _, err := TrustedRoots("../../testdata/tls/localhost.ext"); err == nil {
		t.Error("non-PEM certificate accepted")
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
	insp, err := os.ReadFile(filepath.Join("..", "..", "internal", "observability", "inspection_metrics.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range CounterMetrics {
		if strings.HasPrefix(c.Metric, "jul_inspection_") {
			// The inspection collector pre-registers every label value as a
			// quoted literal rather than listing them in the help text.
			if !strings.Contains(string(insp), `Name: "`+c.Metric+`"`) {
				t.Errorf("%s: metric %s not defined by Jul", c.Key, c.Metric)
			}
			for _, f := range c.Filters {
				_, value, _ := strings.Cut(f, "=")
				if !strings.Contains(string(insp), value) {
					t.Errorf("%s: %s is not a registered %s value", c.Key, f, c.Metric)
				}
			}
			continue
		}
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
	want := DefaultEnvelope
	want.GoMemLimit = "480MiB"
	if e := fakeSys(t, "805306368", "0", "200000 100000", "0-1", "2").VerifyEnvelope(42, want); e.Verified {
		t.Error("a missing GOMEMLIMIT verified against a scenario that sets it")
	}
	sys := fakeSys(t, "805306368", "0", "200000 100000", "0-1", "2")
	if err := os.WriteFile(filepath.Join(sys.Proc, "42", "environ"), []byte("GOMAXPROCS=2\x00GOMEMLIMIT=480MiB\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if e := sys.VerifyEnvelope(42, want); !e.Verified || e.GoMemLimit != "480MiB" {
		t.Errorf("scenario GOMEMLIMIT not verified: %+v", e)
	}
	if e := sys.VerifyEnvelope(42, DefaultEnvelope); e.Verified {
		t.Error("an inherited GOMEMLIMIT verified against a scenario without one")
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
	if got := strings.Join(c.Args, " "); !strings.Contains(got, "env -u GOMEMLIMIT GOMAXPROCS=2") {
		t.Errorf("launch without a scenario GOMEMLIMIT must clear an inherited one: %q", got)
	}
	env := DefaultEnvelope
	env.GoMemLimit = "480MiB"
	if got := strings.Join(Launch(env, "u", "j", "c", "t").Args, " "); !strings.Contains(got, "GOMEMLIMIT=480MiB") || strings.Contains(got, "-u GOMEMLIMIT") {
		t.Errorf("scenario GOMEMLIMIT not applied: %q", got)
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
	if err != nil || len(cells) != 11 {
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

var writeRunSeq int

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
	rules, _ := indentJSON(Rules)
	rulesSHA := sha256Hex(rules)
	writeRunSeq++
	r.Started = now.Add(time.Duration(writeRunSeq) * time.Second)
	m := Manifest{Schema: ManifestSchema, RunID: fmt.Sprintf("run-%d", writeRunSeq), Started: now, Ended: &now, State: "complete", PlanVersion: PlanVersion, PlanSHA: sha256Hex(plan),
		RulesSHA: rulesSHA, Profile: Profiles["quick"], ScenarioSHAs: map[string]string{sc.ID + "/none": r.ScenarioHash},
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

func TestLoadRunsRejectsDuplicatesAndReinterpretation(t *testing.T) {
	a, b := writeRun(t, nil), writeRun(t, nil)
	if _, err := LoadRuns([]string{a, b}); err != nil {
		t.Fatalf("distinct runs rejected: %v", err)
	}
	if _, err := LoadRuns([]string{a, a}); err == nil || !strings.Contains(err.Error(), "duplicate evidence") {
		t.Errorf("same directory twice: %v", err)
	}
	cp := filepath.Join(t.TempDir(), "copy")
	if err := os.CopyFS(cp, os.DirFS(a)); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuns([]string{a, cp}); err == nil || !strings.Contains(err.Error(), "duplicate evidence") {
		t.Errorf("copied directory: %v", err)
	}
	// A renamed copy keeps its result files, which still identify it.
	renamed := writeRun(t, nil)
	res, _ := os.ReadFile(filepath.Join(a, "cells", "result.json"))
	_ = os.WriteFile(filepath.Join(renamed, "cells", "result.json"), res, 0o600)
	if err := WriteSums(renamed); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRuns([]string{a, renamed}); err == nil || !strings.Contains(err.Error(), "duplicate evidence") {
		t.Errorf("copied result under a new run ID: %v", err)
	}

	rewritePlan := func(dir string, edit func(map[string]any)) {
		t.Helper()
		var doc map[string]any
		b, _ := os.ReadFile(filepath.Join(dir, "plan.json"))
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		edit(doc)
		nb, _ := indentJSON(doc)
		_ = os.WriteFile(filepath.Join(dir, "plan.json"), nb, 0o600)
		var m Manifest
		if err := readJSON(filepath.Join(dir, "manifest.json"), &m); err != nil {
			t.Fatal(err)
		}
		m.PlanSHA = sha256Hex(nb)
		_ = writeJSON(filepath.Join(dir, "manifest.json"), m)
		if err := WriteSums(dir); err != nil {
			t.Fatal(err)
		}
	}
	tmpl := writeRun(t, nil)
	rewritePlan(tmpl, func(d map[string]any) { d["config_template_sha256"] = "other" })
	if _, err := LoadRuns([]string{a, tmpl}); err == nil || !strings.Contains(err.Error(), "template") {
		t.Errorf("different configuration template merged: %v", err)
	}
	later := writeRun(t, nil)
	rewritePlan(later, func(d map[string]any) { d["interpretation"].(map[string]any)["max_median_regression"] = 0.2 })
	if _, err := LoadRuns([]string{later}); err == nil || !strings.Contains(err.Error(), "manifest records") {
		t.Errorf("archived interpretation not bound to the manifest: %v", err)
	}
	// A consistent archive frozen under other rules must not be
	// reinterpreted by this harness's rules.
	other := Rules
	other.MaxRegression = 0.2
	ob, _ := indentJSON(other)
	frozen := writeRun(t, func(m *Manifest, _ *RunResult) { m.RulesSHA = sha256Hex(ob) })
	rewritePlan(frozen, func(d map[string]any) { d["interpretation"].(map[string]any)["max_median_regression"] = 0.2 })
	if _, err := LoadRuns([]string{frozen}); err == nil || !strings.Contains(err.Error(), "harness that froze it") {
		t.Errorf("other frozen rules reinterpreted: %v", err)
	}
	missing := writeRun(t, nil)
	rewritePlan(missing, func(d map[string]any) { delete(d, "interpretation") })
	if _, err := LoadRuns([]string{missing}); err == nil {
		t.Error("plan without an interpretation loaded")
	}
}

func TestCompareThresholdsUseUnroundedValues(t *testing.T) {
	no := false
	pts := func(vs ...float64) []Point {
		var ps []Point
		for _, v := range vs {
			ps = append(ps, Point{Goodput: v, Valid: true, Correct: true, Saturated: &no})
		}
		return ps
	}
	// 1 - 899.6/1000 = 0.1004, which rounds to the 0.100 threshold.
	v, reg := Compare(pts(1000, 1000, 1000), pts(899.6, 899.6, 899.6), Rules)
	if v != VerdictRegression || reg == nil || *reg != 0.1 {
		t.Errorf("0.1004 regression: %s %v", v, reg)
	}
	// Spread (1000.4-900)/... crosses MaxSpread only before rounding.
	hi := 1000 * (1 + Rules.MaxSpread + 0.0004)
	if v, _ := Compare(pts(1000, 1000, hi), pts(1000, 1000, 1000), Rules); v != VerdictNoise {
		t.Errorf("unrounded spread above the limit: %s", v)
	}
}

func TestF4UploadOutcomes(t *testing.T) {
	base := F4Result{Proto: "h2", Warmup: "ok", SiblingClass: "ok", Dials: 1, WantDials: 1}
	for name, tc := range map[string]struct {
		r       F4Result
		finding bool
	}{
		"slow verified":          {F4Result{Mode: "slow", Expect: UploadVerified, UploadClass: "ok"}, false},
		"slow 500":               {F4Result{Mode: "slow", Expect: UploadVerified, UploadClass: "status:500"}, true},
		"slow truncated 200":     {F4Result{Mode: "slow", Expect: UploadVerified, UploadClass: "unverified:length"}, true},
		"slow corrupted 200":     {F4Result{Mode: "slow", Expect: UploadVerified, UploadClass: "unverified:echo"}, true},
		"stall watchdog":         {F4Result{Mode: "stall", Expect: UploadNoDeadline, UploadClass: "error:timeout", WatchdogFired: true}, false},
		"stall succeeded":        {F4Result{Mode: "stall", Expect: UploadNoDeadline, UploadClass: "ok"}, true},
		"stall 500":              {F4Result{Mode: "stall", Expect: UploadNoDeadline, UploadClass: "status:500"}, true},
		"deadline on time":       {F4Result{Mode: "stall", Expect: UploadDeadline, Deadline: 5, UploadClass: "status:408", UploadSeconds: 5.2}, false},
		"deadline early":         {F4Result{Mode: "stall", Expect: UploadDeadline, Deadline: 5, UploadClass: "status:408", UploadSeconds: 1}, true},
		"deadline late":          {F4Result{Mode: "stall", Expect: UploadDeadline, Deadline: 5, UploadClass: "status:408", UploadSeconds: 9}, true},
		"deadline not enforced":  {F4Result{Mode: "stall", Expect: UploadDeadline, Deadline: 5, UploadClass: "error:timeout", WatchdogFired: true, UploadSeconds: 20}, true},
		"deadline other status":  {F4Result{Mode: "stall", Expect: UploadDeadline, Deadline: 5, UploadClass: "status:503", UploadSeconds: 5}, true},
		"read timeout ends it":   {F4Result{Mode: "slow", Expect: UploadReadTimeout, Deadline: 5, UploadClass: "status:500", UploadSeconds: 5}, false},
		"read timeout ignored":   {F4Result{Mode: "slow", Expect: UploadReadTimeout, Deadline: 5, UploadClass: "ok", UploadSeconds: 8}, true},
		"read timeout watchdog":  {F4Result{Mode: "stall", Expect: UploadReadTimeout, Deadline: 5, UploadClass: "error:timeout", WatchdogFired: true, UploadSeconds: 12}, true},
		"v1 result not rejudged": {F4Result{Mode: "slow", UploadClass: "status:500"}, false},
	} {
		r := tc.r
		r.Proto, r.Warmup, r.SiblingClass, r.Dials, r.WantDials = base.Proto, base.Warmup, base.SiblingClass, base.Dials, base.WantDials
		inst, f := r.Check()
		if len(inst) != 0 || (len(f) != 0) != tc.finding {
			t.Errorf("%s: instrument=%v findings=%v", name, inst, f)
		}
	}
	sc := func(read, body string) Scenario {
		return Scenario{ReadTimeout: read, BodyTimeout: body, F4: &F4{BodyBytes: 16, ChunkBytes: 1, SlowInterval: Dur(500 * time.Millisecond)}}
	}
	for _, tc := range []struct {
		read, body, proto, mode, variant string
		want                             string
		bound                            float64
	}{
		{"", "5s", "h1", "stall", "none", UploadNoDeadline, 0},
		{"", "5s", "h2", "stall", "both", UploadDeadline, 5},
		{"", "0s", "h1", "stall", "waf", UploadNoDeadline, 0},
		{"", "", "h1", "slow", "both", UploadVerified, 0},
		{"5s", "", "h1", "slow", "none", UploadReadTimeout, 5},
		{"5s", "", "h2", "stall", "both", UploadReadTimeout, 5},
		{"5s", "", "h3", "slow", "both", UploadVerified, 0},
		{"5s", "", "h3", "stall", "both", UploadNoDeadline, 0},
		{"10s", "", "h1", "slow", "none", UploadVerified, 0},
		{"3s", "5s", "h1", "stall", "both", UploadReadTimeout, 3},
		{"9s", "5s", "h1", "stall", "both", UploadDeadline, 5},
		{"9s", "5s", "h3", "stall", "both", UploadDeadline, 5},
	} {
		if e, d := F4Expect(sc(tc.read, tc.body), tc.proto, tc.mode, tc.variant); e != tc.want || d != tc.bound {
			t.Errorf("%+v: got %s %v", tc, e, d)
		}
	}
}

func TestHeapAfterGCRequiresSuccessfulCollections(t *testing.T) {
	serve := func(pprofStatus int, metrics string) int {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/metrics" {
				_, _ = io.WriteString(w, metrics)
				return
			}
			w.WriteHeader(pprofStatus)
		}))
		_ = srv.Listener.Close()
		srv.Listener = ln
		srv.Start()
		t.Cleanup(srv.Close)
		return ln.Addr().(*net.TCPAddr).Port
	}
	full := "go_memstats_heap_inuse_bytes 10\ngo_memstats_heap_alloc_bytes 7\n"
	if in, al, why := HeapAfterGC(serve(http.StatusOK, full)); in == nil || al == nil || *in != 10 || *al != 7 || why != "" {
		t.Errorf("successful collections: %v %v %q", in, al, why)
	}
	if in, al, why := HeapAfterGC(serve(http.StatusNotFound, full)); in != nil || al != nil || !strings.Contains(why, "404") {
		t.Errorf("failed gc reported heap: %v %v %q", in, al, why)
	}
	if in, _, why := HeapAfterGC(serve(http.StatusOK, "go_memstats_heap_inuse_bytes 10\n")); in != nil || why == "" {
		t.Errorf("missing gauge: %v %q", in, why)
	}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	if in, _, why := HeapAfterGC(port); in != nil || !strings.Contains(why, "gc 1") {
		t.Errorf("unreachable admin: %v %q", in, why)
	}
}

func TestSummarizeKeepsNonSuccessLatency(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(start, ms int, class string) Outcome {
		s := t0.Add(time.Duration(start) * time.Millisecond)
		return Outcome{Start: s, End: s.Add(time.Duration(ms) * time.Millisecond), Class: class}
	}
	outs := []Outcome{at(10, 5, "ok"), at(20, 12, "status:503"), at(30, 40, "status:503"), at(40, 2000, "status:503"),
		at(50, 0, "unfinished"), at(-5, 3, "status:503")}
	s := Summarize(Workload{Name: "w"}, outs, t0, t0.Add(time.Second), 1)
	l, ok := s.OutcomeLatency["status:503"]
	if !ok || l.N != 3 || l.P50 != 40 || l.Max != 2000 {
		t.Errorf("overload latency: %+v", s.OutcomeLatency)
	}
	if _, ok := s.OutcomeLatency["unfinished"]; ok || s.OutcomeLatency["ok"] != (Latency{}) {
		t.Errorf("unfinished or ok latency recorded as an outcome: %+v", s.OutcomeLatency)
	}
	if s.MaxMs == nil || *s.MaxMs != 5 {
		t.Errorf("success latency changed: %v", s.MaxMs)
	}
	md := (&Summary{Classes: []ClassRow{{Scenario: "s9", OutcomeLatency: s.OutcomeLatency}}}).Markdown()
	if !strings.Contains(md, "## Non-success outcome latency") || !strings.Contains(md, "| status:503 | 3 | 40.0 |") {
		t.Errorf("overload latency not reported:\n%s", md)
	}
	if strings.Contains((&Summary{Classes: []ClassRow{{Scenario: "s9"}}}).Markdown(), "Non-success") {
		t.Error("latency section rendered without data")
	}
}

func TestF4WatchdogOutlastsPlannedUploads(t *testing.T) {
	for name, p := range Profiles {
		for _, sc := range BuildScenarios(p) {
			f := sc.F4
			if f == nil {
				continue
			}
			limit := f.ClientLimit.D()
			if slices.Contains(f.Modes, "slow") {
				slow := time.Duration(f.BodyBytes/f.ChunkBytes) * f.SlowInterval.D()
				if limit <= slow {
					t.Errorf("%s/%s: watchdog %s ends the %s slow upload", name, sc.ID, limit, slow)
				}
			}
			for _, v := range []string{sc.BodyTimeout, sc.ReadTimeout} {
				if d, ok := positive(v); ok && limit <= d+time.Duration(deadlineSlack*float64(time.Second)) {
					t.Errorf("%s/%s: watchdog %s fires before the %s limit can be judged", name, sc.ID, limit, d)
				}
			}
			if d, err := time.ParseDuration(sc.BodyTimeout); err == nil && d > 0 {
				if limit <= d+time.Duration(deadlineSlack*float64(time.Second)) {
					t.Errorf("%s/%s: watchdog %s fires before body_timeout %s can be judged", name, sc.ID, limit, d)
				}
			}
		}
	}
}

func TestBodyTimeoutNeedsGlobalInspection(t *testing.T) {
	if _, err := RenderConfig(ConfigParams{Adapter: Adapters["baseline-858590dc"], BodyTimeout: "5s"}); err == nil {
		t.Error("baseline adapter rendered body_timeout")
	}
	cfg, err := RenderConfig(ConfigParams{Adapter: Adapters["current"], BodyTimeout: "5s", Origin: "o:1"})
	if err != nil || !bytes.Contains(cfg, []byte(`body_timeout = "5s"`)) {
		t.Errorf("current adapter: %v\n%s", err, cfg)
	}
	var errOut bytes.Buffer
	out := filepath.Join(t.TempDir(), "run")
	if Main([]string{"run", "-out", out, "-profile", "quick", "-envelope", "none", "-scenario", "f4-body-deadline-5s",
		"-target", "base:baseline-858590dc:/bin/true"}, io.Discard, &errOut) == 0 || !strings.Contains(errOut.String(), "cannot express") {
		t.Errorf("run accepted an inexpressible deadline: %s", errOut.String())
	}
	if _, err := os.Stat(out); err == nil {
		t.Error("refused run created its output directory")
	}
}

func TestPlanV3FixturesAndLimits(t *testing.T) {
	base := ConfigParams{Adapter: Adapters["current"], Origin: "o:1", WASM: true, Plugin: "/g/load.wasm", PluginNoBody: "/g/nobody.wasm"}
	render := func(p ConfigParams) string {
		t.Helper()
		b, err := RenderConfig(p)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	full := render(base)
	if !strings.Contains(full, `subscribe = "true", replace = "true"`) || strings.Contains(full, "[global.inspection]") {
		t.Errorf("full fixture changed:\n%s", full)
	}
	p := base
	p.Fixture, p.MaxBuffered, p.InspectionMode = FixtureRequestOnly, "128m", "observe"
	ro := render(p)
	if strings.Contains(ro, "subscribe") || !strings.Contains(ro, `config = { name = "b", read_body = "true" }`) ||
		!strings.Contains(ro, "[global.inspection]\nmax_buffered_bytes = \"128m\"\nmode = \"observe\"") {
		t.Errorf("request-only fixture:\n%s", ro)
	}
	if strings.Contains(ro, "max_instances") || strings.Contains(ro, "\ntimeout = ") {
		t.Errorf("request-only fixture without pool settings rendered them:\n%s", ro)
	}
	p.PluginTimeout, p.PluginMaxInstances = "2s", 4
	if ro := render(p); strings.Count(ro, "timeout = \"2s\"\nmax_instances = 4\nconfig = ") != 2 {
		t.Errorf("request-only pool settings must apply to both plugins:\n%s", ro)
	}
	p.Fixture = FixtureFull
	if _, err := RenderConfig(p); err == nil {
		t.Error("pool settings rendered outside the request-only fixture")
	}
	p = base
	p.Fixture, p.WAF = FixtureNoBody, true
	nb := render(p)
	if !strings.Contains(nb, `path = "/g/nobody.wasm"`) || strings.Contains(nb, "/g/load.wasm") || !strings.Contains(nb, `state_bytes = "4096"`) ||
		!strings.Contains(nb, `max_buffered_bytes = "512m"`) {
		t.Errorf("nobody fixture:\n%s", nb)
	}
	p.MaxBuffered = "1g"
	if s := render(p); !strings.Contains(s, `max_buffered_bytes = "1g"`) || strings.Contains(s, "512m") {
		t.Errorf("scenario budget must replace the adapter WAF budget:\n%s", s)
	}
	p.PluginNoBody = ""
	if _, err := RenderConfig(p); err == nil {
		t.Error("nobody fixture rendered without its guest")
	}
	p = base
	p.Fixture = "other"
	if _, err := RenderConfig(p); err == nil {
		t.Error("unknown fixture rendered")
	}
	for _, f := range []func(*ConfigParams){func(c *ConfigParams) { c.MaxBuffered = "1g" }, func(c *ConfigParams) { c.InspectionMode = "observe" }} {
		q := base
		q.Adapter = Adapters["baseline-858590dc"]
		f(&q)
		if _, err := RenderConfig(q); err == nil {
			t.Error("baseline adapter rendered [global.inspection]")
		}
	}
	var errOut bytes.Buffer
	if Main([]string{"run", "-out", filepath.Join(t.TempDir(), "run"), "-profile", "quick", "-envelope", "none", "-scenario", "p2-saturation-plugin-h1",
		"-target", "base:baseline-858590dc:/bin/true"}, io.Discard, &errOut) == 0 || !strings.Contains(errOut.String(), "cannot express") {
		t.Errorf("run accepted an inexpressible budget: %s", errOut.String())
	}
	errOut.Reset()
	if Main([]string{"run", "-out", filepath.Join(t.TempDir(), "run"), "-profile", "quick", "-envelope", "none", "-scenario", "p3-nobody-plugin-h1",
		"-variants", "wasm", "-guest", "/bin/true", "-target", "main:current:/bin/true"}, io.Discard, &errOut) == 0 || !strings.Contains(errOut.String(), "-guest-nobody") {
		t.Errorf("run accepted the nobody fixture without its guest: %s", errOut.String())
	}
	steps := map[int][2]string{1: {"128m", ""}, 2: {"256m", "observe"}, 3: {"512m", ""}, 4: {"256m", ""}, 5: {"1m", ""}, 9: {"128m", ""}}
	for i, want := range steps {
		if b, m := budgetStep(i); b != want[0] || m != want[1] {
			t.Errorf("budgetStep(%d) = %s %s", i, b, m)
		}
	}
	if FixtureOf(Scenario{}) != FixtureFull {
		t.Error("empty fixture must be full")
	}
	o := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/proxy/echo?resp=8", nil)
	req.Header.Set("X-Assess-a2-State", "4096")
	req.Header.Set("X-Assess-a1-Req-Len", "7")
	(&Origin{}).ServeHTTP(o, req)
	if o.Header().Get("X-Echo-A-State") != "4096" || o.Header().Get("X-Echo-A-Req-Len") != "7" {
		t.Errorf("origin echo %v", o.Header())
	}
}

func TestPlanV3ExerciseChecksAndGuestIdentity(t *testing.T) {
	// Guest identities are compared across every run, not only the first,
	// and the non-body guest is checked too.
	none := writeRun(t, nil)
	g := func(sha string) func(*Manifest, *RunResult) {
		return func(m *Manifest, _ *RunResult) { m.Guest = &Artifact{SHA256: sha} }
	}
	nb := func(sha string) func(*Manifest, *RunResult) {
		return func(m *Manifest, _ *RunResult) { m.GuestNoBody = &Artifact{SHA256: sha} }
	}
	if _, err := LoadRuns([]string{none, writeRun(t, g("a")), writeRun(t, g("b"))}); err == nil || !strings.Contains(err.Error(), "guest b differs from a") {
		t.Errorf("guest mismatch after a guest-less run: %v", err)
	}
	if _, err := LoadRuns([]string{writeRun(t, nb("a")), none, writeRun(t, nb("b"))}); err == nil || !strings.Contains(err.Error(), "guest_nobody b differs from a") {
		t.Errorf("nobody guest mismatch: %v", err)
	}
	if _, err := LoadRuns([]string{writeRun(t, nb("a")), writeRun(t, g("x")), writeRun(t, nb("a"))}); err != nil {
		t.Errorf("matching identities rejected: %v", err)
	}

	// Only the governor's exact overload answer is the overload class.
	mk := func(code int, retry, body string) string {
		h := http.Header{}
		if retry != "" {
			h.Set("Retry-After", retry)
		}
		return StatusClass(&http.Response{StatusCode: code, Header: h}, []byte(body))
	}
	if got := mk(503, "1", "inspection_overloaded\n"); got != OverloadClass {
		t.Errorf("governor overload classified %q", got)
	}
	for _, c := range [][3]string{{"503", "", "inspection_overloaded\n"}, {"503", "1", "plugin pool exhausted\n"}, {"503", "2", "inspection_overloaded"}} {
		if got := mk(503, c[1], c[2]); got != "status:503" {
			t.Errorf("%v classified %q, want status:503", c, got)
		}
	}
	if got := mk(408, "1", "inspection_overloaded"); got != "status:408" {
		t.Errorf("408 classified %q", got)
	}

	// Exercise requirements: unmet minimums are instrument invalidity,
	// exceeded maximums are findings, and missing counters are never a pass.
	res := RunResult{CounterDelta: Metrics{"inspection_rejected_enforce": 3, "inspection_rejected_observe": 0},
		Phases: []PhaseResult{{Classes: []ClassStats{{Classes: map[string]int{OverloadClass: 2}}, {Classes: map[string]int{OverloadClass: 1, "ok": 4}}}}}}
	in, fi := exercised([]Requirement{
		{Counter: "inspection_rejected_enforce", AtLeast: atLeast(1)}, {Class: OverloadClass, AtLeast: atLeast(3)},
		{Counter: "inspection_rejected_observe", AtMost: atLeast(0)},
	}, res)
	if len(in) != 0 || len(fi) != 0 {
		t.Errorf("satisfied requirements reported %v %v", in, fi)
	}
	in, fi = exercised([]Requirement{
		{Counter: "inspection_rejected_observe", AtLeast: atLeast(1)}, {Class: OverloadClass, AtLeast: atLeast(4)},
		{Counter: "inspection_rejected_enforce", AtMost: atLeast(0)}, {Class: OverloadClass, AtMost: atLeast(0)},
		{Counter: "absent", AtLeast: atLeast(1)}, {Counter: "absent", AtMost: atLeast(0)},
	}, res)
	wantIn := []string{"unexercised: inspection_rejected_observe = 0, want at least 1", "unexercised: class " + OverloadClass + " = 3, want at least 4", "unexercised: absent unavailable"}
	wantFi := []string{"inspection_rejected_enforce = 3, want at most 0", "class " + OverloadClass + " = 3, want at most 0", "exercise check: absent unavailable"}
	if !slices.Equal(in, wantIn) || !slices.Equal(fi, wantFi) {
		t.Errorf("exercised = %q / %q", in, fi)
	}

	// p2 retains more than its budget for the whole window; p2o is its
	// observe twin with the same load.
	var p1, p2, p2o, p2h, p4 Scenario
	for _, sc := range BuildScenarios(Profiles["qualification"]) {
		switch sc.ID {
		case "p4-reload-budget-plugin":
			p4 = sc
		case "p1-large-body-plugin-h1":
			p1 = sc
		case "p2-saturation-plugin-h1":
			p2 = sc
		case "p2o-saturation-plugin-observe-h1":
			p2o = sc
		case "p2h-saturation-hostile-h1":
			p2h = sc
		}
	}
	load := p2.Phases[0].Workloads[0]
	if retained := int64(load.Concurrency) * int64(len(BodyFor(load))); retained <= 128<<20 || load.BodyBytes >= 1<<20 || !strings.Contains(load.Path, "delay=5000") {
		t.Errorf("p2 load retains %d bytes with %d-byte bodies on %s", retained, load.BodyBytes, load.Path)
	}
	if !load.HonorRetryAfter || !p2.Phases[0].Workloads[1].HonorRetryAfter || p2.GoMemLimit == "" || p2.PluginMaxInstances == 0 {
		t.Errorf("p2 must model compliant clients inside a sized static footprint: %+v", p2)
	}
	hostile := p2h.Phases[0].Workloads[0]
	if hostile.HonorRetryAfter || hostile.Path != load.Path || hostile.Concurrency != load.Concurrency || p2h.MaxBuffered != p2.MaxBuffered ||
		p2h.GoMemLimit != p2.GoMemLimit || p2h.PluginMaxInstances != p2.PluginMaxInstances || p2h.Comparison != "record" {
		t.Errorf("p2h must differ from p2 only by ignoring Retry-After: %+v", p2h)
	}
	if p1.PluginMaxInstances != p2.PluginMaxInstances || p1.PluginTimeout != p2.PluginTimeout || p1.GoMemLimit != p2.GoMemLimit || p1.MaxBuffered != "" {
		t.Errorf("p1 must use the plugin-only static footprint at the default budget: %+v", p1)
	}
	if p4.ReloadEvery.D() != 5*time.Second || p4.PluginMaxInstances != p1.PluginMaxInstances || p4.GoMemLimit != p1.GoMemLimit {
		t.Errorf("p4 must reload every 5 s inside p1's static footprint: %+v", p4)
	}
	if !slices.Contains(Suites["i2-plugin"], p2h.ID) {
		t.Error("p2h is not in the i2-plugin suite")
	}
	if p2o.Phases[0].Workloads[0].Path != load.Path || p2o.InspectionMode != "observe" || len(p2.Require) == 0 || len(p2o.Require) == 0 {
		t.Errorf("p2o is not p2's observe twin: %+v", p2o)
	}
}

func TestDriveHonorsRetryAfterOnlyWhenAsked(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "inspection_overloaded\n")
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	w := Workload{Name: "load", Proto: "h1", Method: "POST", Path: "/", BodyBytes: 64, BodyShape: "fill", Concurrency: 1,
		Timeout: Dur(5 * time.Second), Expect: []string{OverloadClass}}
	run := func(honor bool) ClassStats {
		hits.Store(0)
		w.HonorRetryAfter = honor
		now := time.Now()
		st, err := Drive(context.Background(), Target{Port: port}, w, now, now.Add(1500*time.Millisecond))
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	if st := run(true); st.Classes[OverloadClass] != 2 || hits.Load() != 2 {
		t.Errorf("compliant worker sent %d requests in 1.5 s (classes %v), want 2", hits.Load(), st.Classes)
	}
	if st := run(false); st.Classes[OverloadClass] < 10 {
		t.Errorf("hostile worker sent only %d requests (classes %v)", hits.Load(), st.Classes)
	}
}
