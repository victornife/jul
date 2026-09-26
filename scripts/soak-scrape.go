//go:build ignore

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// soak-scrape retains a timestamped time series of a Jul /metrics endpoint for
// soak and fault evidence (#422). It is deliberately a small collector, not a
// monitoring product: one process, one endpoint, one gzip'd JSONL file.
//
// Record (runs until SIGINT/SIGTERM, -duration or the -max-bytes cap):
//
//	go run scripts/soak-scrape.go -url http://127.0.0.1:9901/metrics \
//	    -out soak-artifacts/<run>/metrics -interval 10s \
//	    -label jul_sha=$(git rev-parse HEAD) -label config=burn-in-current.toml -label workload=...
//
// Summarize a recording (min/max/first/last per series, gaps, quiescence):
//
//	go run scripts/soak-scrape.go -summarize soak-artifacts/<run>/metrics
//
// File format (samples.jsonl.gz), one JSON object per line:
//
//	{"kind":"header", "started":..., "url":..., "interval_s":..., "labels":{...}}
//	{"kind":"series", "add":[[index,"name{labels}"],...]}   // before first use
//	{"kind":"sample", "seq":n, "t":"RFC3339Nano", "ms":scrape_ms, "v":[value by index, null if absent]}
//	{"kind":"error",  "seq":n, "t":..., "err":"..."}          // a failed scrape
//	{"kind":"gap",    "from":..., "to":..., "seconds":s}      // successive successes > 2x interval apart
//	{"kind":"end",    "t":..., "reason":"signal|duration|cap_reached"}
//
// Histogram _bucket series are dropped unless -buckets: trends need _sum and
// _count, and buckets multiply the file size. The writer flushes after every
// line, so a killed collector still leaves a readable prefix.
package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

const maxInterval = 15 * time.Second

type labelFlags map[string]string

func (l labelFlags) String() string { return fmt.Sprint(map[string]string(l)) }
func (l labelFlags) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("label %q: want key=value", s)
	}
	l[k] = v
	return nil
}

func main() {
	labels := labelFlags{}
	url := flag.String("url", "http://127.0.0.1:9901/metrics", "metrics endpoint")
	out := flag.String("out", "", "output directory (created)")
	interval := flag.Duration("interval", 10*time.Second, "scrape interval (<= 15s)")
	timeout := flag.Duration("timeout", 5*time.Second, "per-scrape timeout")
	duration := flag.Duration("duration", 0, "stop after this long (0 = until signal)")
	maxBytes := flag.Int64("max-bytes", 256<<20, "stop when the compressed file exceeds this size")
	include := flag.String("include", `^(process_|go_goroutines$|go_threads$|go_memstats_(heap_alloc|heap_inuse|heap_objects|sys|next_gc)_bytes$|go_gc_duration_seconds_count$|jul_)`, "regexp of metric families to retain")
	buckets := flag.Bool("buckets", false, "retain histogram _bucket series")
	summarize := flag.String("summarize", "", "summarize a recording directory or samples file and exit")
	flag.Var(labels, "label", "key=value recorded in the header (repeatable)")
	flag.Parse()

	if *summarize != "" {
		if err := summarizeRecording(*summarize, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "summarize:", err)
			os.Exit(1)
		}
		return
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "-out is required")
		os.Exit(2)
	}
	if *interval <= 0 || *interval > maxInterval {
		fmt.Fprintf(os.Stderr, "-interval must be in (0, %s]\n", maxInterval)
		os.Exit(2)
	}
	re, err := regexp.Compile(*include)
	if err != nil {
		fmt.Fprintln(os.Stderr, "-include:", err)
		os.Exit(2)
	}
	if err := record(*url, *out, *interval, *timeout, *duration, *maxBytes, re, *buckets, labels); err != nil {
		fmt.Fprintln(os.Stderr, "record:", err)
		os.Exit(1)
	}
}

type recorder struct {
	f     *os.File
	gz    *gzip.Writer
	enc   *json.Encoder
	index map[string]int
	names []string
}

func (r *recorder) write(v any) error {
	if err := r.enc.Encode(v); err != nil {
		return err
	}
	return r.gz.Flush()
}

func record(url, out string, interval, timeout, duration time.Duration, maxBytes int64, include *regexp.Regexp, buckets bool, labels labelFlags) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	path := filepath.Join(out, "samples.jsonl.gz")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("refusing to overwrite %s", path)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	gz := gzip.NewWriter(f)
	r := &recorder{f: f, gz: gz, enc: json.NewEncoder(gz), index: map[string]int{}}
	started := time.Now().UTC()
	if err := r.write(map[string]any{
		"kind": "header", "started": started.Format(time.RFC3339Nano), "url": url,
		"interval_s": interval.Seconds(), "include": include.String(), "buckets": buckets, "labels": labels,
	}); err != nil {
		return err
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	var deadline <-chan time.Time
	if duration > 0 {
		deadline = time.After(duration)
	}
	client := &http.Client{Timeout: timeout}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var seq, okCount, errCount, gapCount int
	var gapSeconds float64
	var lastOK time.Time
	reason := "signal"
	scrape := func() error {
		seq++
		t0 := time.Now()
		values, err := fetch(client, url, include, buckets)
		ms := float64(time.Since(t0).Microseconds()) / 1000
		now := t0.UTC()
		if err != nil {
			errCount++
			return r.write(map[string]any{"kind": "error", "seq": seq, "t": now.Format(time.RFC3339Nano), "err": err.Error()})
		}
		if !lastOK.IsZero() {
			if d := now.Sub(lastOK); d > 2*interval {
				gapCount++
				gapSeconds += d.Seconds()
				if werr := r.write(map[string]any{"kind": "gap", "from": lastOK.Format(time.RFC3339Nano), "to": now.Format(time.RFC3339Nano), "seconds": d.Seconds()}); werr != nil {
					return werr
				}
			}
		}
		lastOK = now
		okCount++
		var added [][2]any
		keys := make([]string, 0, len(values))
		for k := range values {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if _, ok := r.index[k]; !ok {
				r.index[k] = len(r.names)
				r.names = append(r.names, k)
				added = append(added, [2]any{r.index[k], k})
			}
		}
		if len(added) > 0 {
			if werr := r.write(map[string]any{"kind": "series", "add": added}); werr != nil {
				return werr
			}
		}
		v := make([]any, len(r.names))
		for k, val := range values {
			if math.IsNaN(val) || math.IsInf(val, 0) {
				continue
			}
			v[r.index[k]] = val
		}
		return r.write(map[string]any{"kind": "sample", "seq": seq, "t": now.Format(time.RFC3339Nano), "ms": ms, "v": v})
	}

	if err := scrape(); err != nil {
		return err
	}
loop:
	for {
		select {
		case <-sig:
			break loop
		case <-deadline:
			reason = "duration"
			break loop
		case <-ticker.C:
			if err := scrape(); err != nil {
				return err
			}
			if st, err := f.Stat(); err == nil && st.Size() > maxBytes {
				reason = "cap_reached"
				break loop
			}
		}
	}
	ended := time.Now().UTC()
	if err := r.write(map[string]any{"kind": "end", "t": ended.Format(time.RFC3339Nano), "reason": reason}); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	sum, size, err := fileSHA256(path)
	if err != nil {
		return err
	}
	manifest := map[string]any{
		"file": filepath.Base(path), "sha256": sum, "bytes": size,
		"started": started.Format(time.RFC3339Nano), "ended": ended.Format(time.RFC3339Nano),
		"end_reason": reason, "interval_s": interval.Seconds(), "url": url, "labels": labels,
		"scrapes": seq, "ok": okCount, "errors": errCount, "gaps": gapCount, "gap_seconds": gapSeconds,
		"series": len(r.names),
	}
	b, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(out, "metrics-manifest.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "soak-scrape: %d scrapes (%d ok, %d errors, %d gaps), %d series, %d bytes, sha256 %s, end %s\n",
		seq, okCount, errCount, gapCount, len(r.names), size, sum, reason)
	return nil
}

// fetch scrapes url and flattens every retained sample to "name{k=v,...}".
// SOAK_SCRAPE_BEARER, when set, is sent as a bearer token; it is read from the
// environment so it never appears in a process listing or the recording.
func fetch(client *http.Client, url string, include *regexp.Regexp, buckets bool) (map[string]float64, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := os.Getenv("SOAK_SCRAPE_BEARER"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(resp.Body)
	if err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for name, mf := range families {
		if !include.MatchString(name) {
			continue
		}
		for _, m := range mf.GetMetric() {
			lbl := labelString(m.GetLabel())
			switch mf.GetType() {
			case dto.MetricType_COUNTER:
				out[name+lbl] = m.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				out[name+lbl] = m.GetGauge().GetValue()
			case dto.MetricType_UNTYPED:
				out[name+lbl] = m.GetUntyped().GetValue()
			case dto.MetricType_SUMMARY:
				out[name+"_sum"+lbl] = m.GetSummary().GetSampleSum()
				out[name+"_count"+lbl] = float64(m.GetSummary().GetSampleCount())
			case dto.MetricType_HISTOGRAM:
				h := m.GetHistogram()
				out[name+"_sum"+lbl] = h.GetSampleSum()
				out[name+"_count"+lbl] = float64(h.GetSampleCount())
				if buckets {
					for _, b := range h.GetBucket() {
						out[fmt.Sprintf("%s_bucket%s", name, withLE(lbl, b.GetUpperBound()))] = float64(b.GetCumulativeCount())
					}
				}
			}
		}
	}
	return out, nil
}

func labelString(ls []*dto.LabelPair) string {
	if len(ls) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ls))
	for _, l := range ls {
		parts = append(parts, fmt.Sprintf("%s=%q", l.GetName(), l.GetValue()))
	}
	sort.Strings(parts)
	return "{" + strings.Join(parts, ",") + "}"
}

func withLE(lbl string, le float64) string {
	s := fmt.Sprintf("le=%q", fmt.Sprint(le))
	if lbl == "" {
		return "{" + s + "}"
	}
	return lbl[:len(lbl)-1] + "," + s + "}"
}

func fileSHA256(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, err
}

// ---- summarize ----

type seriesStats struct {
	first, last, min, max float64
	minT, maxT            string
	n                     int
}

// quiescenceSeries are compared first-sample vs last-sample to judge whether
// the process returned to its idle footprint.
var quiescenceSeries = []string{
	"go_goroutines", "process_open_fds", "process_resident_memory_bytes",
	"go_memstats_heap_inuse_bytes", "jul_listener_conns", "jul_http_requests_in_flight",
}

func summarizeRecording(path string, w io.Writer) error {
	if st, err := os.Stat(path); err == nil && st.IsDir() {
		path = filepath.Join(path, "samples.jsonl.gz")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	var names []string
	stats := map[int]*seriesStats{}
	var header map[string]any
	var firstT, lastT, endReason string
	var samples, errs, gaps int
	var gapSecs float64
	var errKinds = map[string]int{}
	for sc.Scan() {
		var line struct {
			Kind    string          `json:"kind"`
			T       string          `json:"t"`
			Add     [][2]any        `json:"add"`
			V       []*float64      `json:"v"`
			Err     string          `json:"err"`
			Seconds float64         `json:"seconds"`
			Reason  string          `json:"reason"`
			Raw     json.RawMessage `json:"-"`
		}
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			return fmt.Errorf("line %d: %w", samples+errs+gaps+1, err)
		}
		switch line.Kind {
		case "header":
			_ = json.Unmarshal(sc.Bytes(), &header)
		case "series":
			for _, a := range line.Add {
				idx := int(a[0].(float64))
				for len(names) <= idx {
					names = append(names, "")
				}
				names[idx] = a[1].(string)
			}
		case "sample":
			samples++
			if firstT == "" {
				firstT = line.T
			}
			lastT = line.T
			for i, v := range line.V {
				if v == nil {
					continue
				}
				s := stats[i]
				if s == nil {
					s = &seriesStats{first: *v, min: *v, max: *v, minT: line.T, maxT: line.T}
					stats[i] = s
				}
				if *v < s.min {
					s.min, s.minT = *v, line.T
				}
				if *v > s.max {
					s.max, s.maxT = *v, line.T
				}
				s.last = *v
				s.n++
			}
		case "error":
			errs++
			k := line.Err
			if len(k) > 80 {
				k = k[:80]
			}
			errKinds[k]++
		case "gap":
			gaps++
			gapSecs += line.Seconds
		case "end":
			endReason = line.Reason
		}
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return err
	}
	fmt.Fprintf(w, "# Metrics summary: %s\n\n", path)
	if header != nil {
		b, _ := json.Marshal(header["labels"])
		fmt.Fprintf(w, "- url: `%v`, interval: %vs, labels: `%s`\n", header["url"], header["interval_s"], b)
	}
	fmt.Fprintf(w, "- window: %s → %s, samples: %d, scrape errors: %d, gaps (>2× interval): %d (%.0fs), end: %s\n\n",
		firstT, lastT, samples, errs, gaps, gapSecs, orUnknown(endReason))
	for k, n := range errKinds {
		fmt.Fprintf(w, "- scrape error ×%d: `%s`\n", n, k)
	}
	fmt.Fprintln(w, "\n## Quiescence (first vs last sample)\n\n| Series | first | last | max | max at |\n| --- | --- | --- | --- | --- |")
	byName := map[string]int{}
	for i, n := range names {
		byName[n] = i
	}
	for _, n := range quiescenceSeries {
		if i, ok := byName[n]; ok && stats[i] != nil {
			s := stats[i]
			fmt.Fprintf(w, "| `%s` | %g | %g | %g | %s |\n", n, s.first, s.last, s.max, s.maxT)
		}
	}
	fmt.Fprintln(w, "\n## All series (changed during the window)\n\n| Series | first | last | min | max | max at |\n| --- | --- | --- | --- | --- | --- |")
	idx := make([]int, 0, len(stats))
	for i := range stats {
		idx = append(idx, i)
	}
	sort.Slice(idx, func(a, b int) bool { return names[idx[a]] < names[idx[b]] })
	for _, i := range idx {
		s := stats[i]
		if s.min == s.max {
			continue
		}
		fmt.Fprintf(w, "| `%s` | %g | %g | %g | %g | %s |\n", names[i], s.first, s.last, s.min, s.max, s.maxT)
	}
	return nil
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown (no end record: collector was killed)"
	}
	return s
}
