// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// Target addresses the Jul listeners of one run.
type Target struct {
	Port, TLSPort int
	WASM          bool
	// Roots trusts the certificate Jul serves; the TLS clients verify against it.
	Roots *x509.CertPool
}

// TrustedRoots returns a pool holding the PEM certificates in path. The
// harness pins the self-signed loopback certificate it configured Jul with.
func TrustedRoots(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path) //nolint:gosec // operator-supplied certificate path
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: no PEM certificate", path)
	}
	return pool, nil
}

// Client is a protocol-specific client that counts the connections it dials.
type Client struct {
	HTTP  *http.Client
	Base  string
	Proto string
	dials atomic.Int64
	close func()
}

// Dials reports how many transport connections were established.
func (c *Client) Dials() int64 { return c.dials.Load() }

// Close releases idle connections.
func (c *Client) Close() { c.close() }

// NewClient builds an h1, h2 or h3 client. conns > 0 caps H1/H2 connections;
// H2 then queues streams on the existing connection rather than dialling.
func NewClient(proto string, t Target, conns int, timeout time.Duration) (*Client, error) {
	tlsCfg := &tls.Config{RootCAs: t.Roots, ServerName: "localhost", MinVersion: tls.VersionTLS12}
	c := &Client{Proto: proto}
	d := &net.Dialer{Timeout: 10 * time.Second}
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := d.DialContext(ctx, network, addr)
		if err == nil {
			c.dials.Add(1)
		}
		return conn, err
	}
	switch proto {
	case "h1", "h2":
		tr := &http.Transport{DialContext: dial, DisableCompression: true, MaxIdleConnsPerHost: 1024,
			TLSClientConfig: tlsCfg, Protocols: new(http.Protocols)}
		if conns > 0 {
			tr.MaxConnsPerHost = conns
		}
		if proto == "h1" {
			tr.Protocols.SetHTTP1(true)
			c.Base = fmt.Sprintf("http://127.0.0.1:%d", t.Port)
		} else {
			tr.Protocols.SetHTTP2(true)
			tr.HTTP2 = &http.HTTP2Config{StrictMaxConcurrentRequests: conns > 0}
			c.Base = fmt.Sprintf("https://localhost:%d", t.TLSPort)
		}
		c.HTTP, c.close = &http.Client{Transport: tr, Timeout: timeout}, tr.CloseIdleConnections
	case "h3":
		tr := &http3.Transport{TLSClientConfig: tlsCfg,
			Dial: func(ctx context.Context, addr string, tc *tls.Config, qc *quic.Config) (*quic.Conn, error) {
				conn, err := quic.DialAddrEarly(ctx, addr, tc, qc)
				if err == nil {
					c.dials.Add(1)
				}
				return conn, err
			}}
		c.Base = fmt.Sprintf("https://localhost:%d", t.TLSPort)
		c.HTTP, c.close = &http.Client{Transport: tr, Timeout: timeout}, func() { _ = tr.Close() }
	default:
		return nil, fmt.Errorf("unknown protocol %q", proto)
	}
	return c, nil
}

// ItemsBody returns a benign JSON document of exactly n bytes made of many
// short fields (the many-argument amplification shape), optionally marked.
func ItemsBody(n int, marker string) []byte {
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

// WideBody returns about n bytes of JSON made of 2000-byte string fields so
// the CRS argument count stays far below Coraza's argument limit.
func WideBody(n int, marker string) []byte {
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

// BodyFor returns the request body of a workload.
func BodyFor(w Workload) []byte {
	switch {
	case w.BodyBytes <= 0:
		return nil
	case w.BodyShape == "items":
		return ItemsBody(w.BodyBytes, "")
	case w.BodyShape == "fill":
		return bytes.Repeat([]byte("x"), w.BodyBytes)
	default:
		return WideBody(w.BodyBytes, "")
	}
}

type slowReader struct {
	ctx  context.Context
	data []byte
	off  int
	bps  int
}

func (s *slowReader) Read(p []byte) (int, error) {
	if s.off >= len(s.data) {
		return 0, io.EOF
	}
	chunk := min(1024, len(p), len(s.data)-s.off)
	t := time.NewTimer(time.Duration(float64(chunk) / float64(s.bps) * float64(time.Second)))
	defer t.Stop()
	select {
	case <-t.C:
	case <-s.ctx.Done():
		return 0, s.ctx.Err()
	}
	n := copy(p, s.data[s.off:s.off+chunk])
	s.off += n
	return n, nil
}

// Outcome is one request's classified result.
type Outcome struct {
	Start, End time.Time
	Class      string // ok | unverified:<why> | status:<code> | error:<class> | client-cancel | unfinished
	Status     int
	Bytes      int64
	Proto      string
	Err        string // the transport/read error for error classes
}

var keySeq atomic.Int64

func newRequest(ctx context.Context, method, url string, body io.Reader, n int) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	// A hostname avoids CRS 920350 (numeric Host) so clean traffic stays clean.
	req.Host = "localhost"
	req.Header.Set("User-Agent", "jul-695-assessment")
	req.Header.Set("Accept", "text/html")
	if body != nil {
		req.ContentLength = int64(n)
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Do performs one workload request and verifies it completely.
func Do(ctx context.Context, c *Client, w Workload, body []byte, wasm bool) Outcome {
	path := w.Path
	if w.UniqueKey {
		path += fmt.Sprintf("&k=%d", keySeq.Add(1))
	}
	if w.CancelAfter > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, w.CancelAfter.D())
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
		if w.SlowBPS > 0 {
			rd = &slowReader{ctx: ctx, data: body, bps: w.SlowBPS}
		}
	}
	o := Outcome{Start: time.Now()}
	req, err := newRequest(ctx, w.Method, c.Base+path, rd, len(body))
	if err != nil {
		o.End, o.Class = time.Now(), "error:build"
		return o
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		o.End, o.Class, o.Err = time.Now(), errClass(err, w.CancelAfter > 0), err.Error()
		return o
	}
	got, rerr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	o.End, o.Status, o.Bytes, o.Proto = time.Now(), resp.StatusCode, int64(len(got)), resp.Proto
	switch {
	case rerr != nil:
		o.Class, o.Err = errClass(rerr, w.CancelAfter > 0), rerr.Error()
		if !strings.HasPrefix(o.Class, "client-cancel") {
			o.Class = "error:body-" + strings.TrimPrefix(o.Class, "error:")
		}
	case resp.StatusCode/100 != 2:
		o.Class = "status:" + strconv.Itoa(resp.StatusCode)
	default:
		o.Class = "ok"
		if why := Verify(resp, got, w, len(body), wasm, c.Proto); why != "" {
			o.Class = "unverified:" + why
		}
	}
	return o
}

// Verify checks protocol, complete content and origin/plugin canaries.
func Verify(resp *http.Response, got []byte, w Workload, bodyLen int, wasm bool, proto string) string {
	if want := map[string]int{"h1": 1, "h2": 2, "h3": 3}[proto]; resp.ProtoMajor != want {
		return "proto " + resp.Proto
	}
	if len(got) != w.RespBytes {
		return fmt.Sprintf("resp-len %d!=%d", len(got), w.RespBytes)
	}
	if !bytes.Equal(got, Filler(w.RespBytes, "")) {
		return "resp-content"
	}
	h := resp.Header
	if w.WantEcho && bodyLen > 0 && h.Get("X-Origin-Req-Len") != strconv.Itoa(bodyLen) {
		return "origin-req-len " + h.Get("X-Origin-Req-Len")
	}
	if wasm {
		if h.Get("X-Assess-a-Resp") == "" && h.Get("X-Assess-a1-Resp") == "" && h.Get("X-Assess-a2-Resp") == "" {
			return "missing a-resp"
		}
		if v := h.Get("X-Assess-b-Resp"); v != strconv.Itoa(w.RespBytes) {
			return "b-resp " + v
		}
		if w.WantEcho && bodyLen > 0 && h.Get("X-Echo-B-Req-Len") != strconv.Itoa(bodyLen) {
			return "b-req-len " + h.Get("X-Echo-B-Req-Len")
		}
	}
	return ""
}

func errClass(err error, cancelling bool) string {
	if cancelling && errors.Is(err, context.DeadlineExceeded) {
		return "client-cancel"
	}
	s := err.Error()
	switch {
	case errors.Is(err, context.Canceled):
		return "error:cancelled"
	case strings.Contains(s, "Client.Timeout") || errors.Is(err, context.DeadlineExceeded):
		return "error:timeout"
	case strings.Contains(s, "stream error") || strings.Contains(s, "H3_REQUEST_CANCELLED") || strings.Contains(s, "H3_INTERNAL_ERROR"):
		return "error:stream-reset"
	case strings.Contains(s, "connection reset"):
		return "error:reset"
	case strings.Contains(s, "EOF"):
		return "error:eof"
	case strings.Contains(s, "refused"):
		return "error:refused"
	case strings.Contains(s, "unexpected end") || strings.Contains(s, "short"):
		return "error:short"
	}
	return "error:other"
}

// ClassStats summarises one workload class inside one measurement window.
type ClassStats struct {
	Workload    string         `json:"workload"`
	Proto       string         `json:"proto"`
	Concurrency int            `json:"concurrency"`
	WindowSec   float64        `json:"window_seconds"`
	Attempts    int            `json:"attempts"`
	Verified    int            `json:"verified"`
	GoodN       int            `json:"verified_in_window"`
	GoodputRPS  float64        `json:"goodput_rps"`
	GoodputBPS  float64        `json:"goodput_bytes_per_second"`
	Classes     map[string]int `json:"classes"`
	P50ms       *float64       `json:"p50_ms"`
	P95ms       *float64       `json:"p95_ms"`
	P99ms       *float64       `json:"p99_ms"`
	MaxMs       *float64       `json:"max_ms"`
	// CrossedIn counts requests started during warm-up that completed in
	// the window; they are excluded from every other field.
	CrossedIn int `json:"crossed_in_from_warmup"`
	// Outstanding counts attempts started in the window that were still in
	// flight when it ended. They are verified and counted in Attempts and
	// Classes, but excluded from goodput and latency.
	Outstanding int            `json:"outstanding_at_window_end"`
	Protos      map[string]int `json:"response_protos"`
	Dials       int64          `json:"connections_dialled"`
	WantConns   int            `json:"connections_required,omitempty"`
	Expect      []string       `json:"expected_classes,omitempty"`
	// ErrorSamples keeps the first error text of each error class.
	ErrorSamples map[string]string `json:"error_samples,omitempty"`
	// OutcomeLatency is the latency of every completed attempt started in
	// the window, per non-ok outcome class (for example status:503 for
	// overload), including attempts that finished after the window.
	// Unfinished attempts have no latency. Absent before plan v2.
	OutcomeLatency map[string]Latency `json:"outcome_latency_ms,omitempty"`
}

// Latency summarises one outcome class's request durations in milliseconds.
type Latency struct {
	N   int     `json:"n"`
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
	Max float64 `json:"max"`
}

func latencyOf(ms []float64) Latency {
	slices.Sort(ms)
	q := func(p float64) float64 { return round3(ms[int(p*float64(len(ms)-1))]) }
	return Latency{N: len(ms), P50: q(0.50), P95: q(0.95), P99: q(0.99), Max: q(1)}
}

// Check separates instrument problems (the class did not measure what it
// claims) from runtime findings (Jul produced an unexpected outcome).
func (s ClassStats) Check() (instrument, findings []string) {
	if s.Attempts == 0 {
		instrument = append(instrument, "no attempt started inside the window")
	}
	if s.WantConns > 0 && s.Dials != int64(s.WantConns) {
		instrument = append(instrument, fmt.Sprintf("dialled %d connections, required %d", s.Dials, s.WantConns))
	}
	for _, k := range sortedKeys(s.Classes) {
		switch {
		case k == "ok" || slices.Contains(s.Expect, k):
		case strings.HasPrefix(k, "unverified:proto"), k == "error:client", k == "error:build":
			instrument = append(instrument, fmt.Sprintf("%s ×%d", k, s.Classes[k]))
		default:
			findings = append(findings, fmt.Sprintf("%s ×%d", k, s.Classes[k]))
		}
	}
	return instrument, findings
}

// Summarize attributes outcomes to the window by start time.
func Summarize(w Workload, outs []Outcome, from, to time.Time, dials int64) ClassStats {
	s := ClassStats{Workload: w.Name, Proto: w.Proto, Concurrency: w.Concurrency, WindowSec: to.Sub(from).Seconds(),
		Classes: map[string]int{}, Protos: map[string]int{}, Dials: dials, WantConns: w.Connections, Expect: w.Expect}
	var lats []float64
	other := map[string][]float64{}
	var good int64
	for _, o := range outs {
		if o.Start.Before(from) {
			if o.Class != "unfinished" && !o.End.Before(from) {
				s.CrossedIn++
			}
			continue
		}
		if !o.Start.Before(to) {
			continue
		}
		s.Attempts++
		s.Classes[o.Class]++
		if o.Err != "" {
			if s.ErrorSamples == nil {
				s.ErrorSamples = map[string]string{}
			}
			if _, ok := s.ErrorSamples[o.Class]; !ok {
				s.ErrorSamples[o.Class] = o.Err
			}
		}
		if o.Proto != "" {
			s.Protos[o.Proto]++
		}
		late := o.End.After(to)
		if late {
			s.Outstanding++
		}
		ms := float64(o.End.Sub(o.Start).Microseconds()) / 1000
		if o.Class != "ok" && o.Class != "unfinished" {
			other[o.Class] = append(other[o.Class], ms)
		}
		if o.Class == "ok" {
			s.Verified++
			if late {
				continue
			}
			s.GoodN++
			good += o.Bytes
			lats = append(lats, ms)
		}
	}
	if s.WindowSec > 0 {
		s.GoodputRPS = round3(float64(s.GoodN) / s.WindowSec)
		s.GoodputBPS = round3(float64(good) / s.WindowSec)
	}
	if len(lats) > 0 {
		l := latencyOf(lats)
		s.P50ms, s.P95ms, s.P99ms, s.MaxMs = &l.P50, &l.P95, &l.P99, &l.Max
	}
	for k, v := range other {
		if s.OutcomeLatency == nil {
			s.OutcomeLatency = map[string]Latency{}
		}
		s.OutcomeLatency[k] = latencyOf(v)
	}
	return s
}

func round3(v float64) float64 {
	return float64(int64(v*1000+0.5)) / 1000
}

// Drive runs a closed-loop workload from now until end; the window
// [from, end) is measured. Requests still in flight at end are cancelled and
// recorded as unfinished.
func Drive(ctx context.Context, t Target, w Workload, from, end time.Time) (ClassStats, error) {
	c, err := NewClient(w.Proto, t, w.Connections, w.Timeout.D())
	if err != nil {
		return ClassStats{}, err
	}
	defer c.Close()
	body := BodyFor(w)
	var mu sync.Mutex
	var outs []Outcome
	var wg sync.WaitGroup
	for range w.Concurrency {
		wg.Go(func() {
			// No request starts at or after end; requests already in flight
			// drain under their own timeout and are verified, so the window
			// is never cut by cancelling the client.
			for ctx.Err() == nil && time.Now().Before(end) {
				o := Do(ctx, c, w, body, t.WASM)
				if ctx.Err() != nil && o.Class != "ok" {
					o.Class = "unfinished"
				}
				mu.Lock()
				outs = append(outs, o)
				mu.Unlock()
				if w.Interval > 0 {
					wait := min(w.Interval.D(), time.Until(end))
					sleepCtx(ctx, wait)
				}
			}
		})
	}
	wg.Wait()
	return Summarize(w, outs, from, end, c.Dials()), nil
}

// Canary is one pre/post policy check.
type Canary struct {
	Name   string `json:"name"`
	Status int    `json:"status"`
	Want   int    `json:"want"`
	OK     bool   `json:"ok"`
}

// RunCanaries checks clean traffic and every enabled policy.
func RunCanaries(t Target, waf, wasm bool) []Canary {
	c, err := NewClient("h1", t, 1, 10*time.Second)
	if err != nil {
		return []Canary{{Name: "client", Status: -1}}
	}
	defer c.Close()
	do := func(name, method, path string, body []byte, want int) Canary {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		st := -1
		if req, err := newRequest(context.Background(), method, c.Base+path, rd, len(body)); err == nil {
			if resp, err := c.HTTP.Do(req); err == nil {
				_, _ = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				st = resp.StatusCode
			}
		}
		return Canary{Name: name, Status: st, Want: want, OK: st == want}
	}
	out := []Canary{do("clean-proxy", "POST", "/proxy/echo?resp=4096", ItemsBody(2048, ""), 200)}
	if waf {
		out = append(out,
			do("waf-request-body", "POST", "/proxy/echo?resp=4096", ItemsBody(2048, wafReqMark), 403),
			do("waf-response-body", "GET", "/proxy/echo?resp=4096&canary=waf", nil, 403),
			do("waf-static-response-clean", "GET", "/static/page.html", nil, 200))
	}
	if wasm {
		out = append(out,
			do("wasm-request-body", "POST", "/proxy/echo?resp=4096", ItemsBody(2048, wasmReqMark), 403),
			do("wasm-on-static", "POST", "/static/page.html", ItemsBody(2048, wasmReqMark), 403))
	}
	return out
}
