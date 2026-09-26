//go:build ignore

// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// fault-load is the focused load generator for #422 host-fault evidence. It
// drives one URL with a fixed number of workers and writes one JSON line per
// second: requests, status counts, a bounded client-side error taxonomy and
// latency percentiles. Unlike burn-in-load it knows nothing about Jul's
// routes, so every fault profile can share it.
//
//	go run scripts/fault-load.go -url http://127.0.0.1:19080/ -workers 64 -duration 60s -out load.jsonl
//
// -hold N additionally opens N idle TCP connections to -hold-addr and keeps
// them open for the run, which is how the FD profile exhausts a lowered
// RLIMIT_NOFILE without needing request volume. -fresh disables keep-alive so
// every request is a new connection. -unique-paths appends a unique query to
// every request (cache fill). -header K=V is sent on every request.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type window struct {
	mu        sync.Mutex
	requests  int
	status    map[int]int
	errs      map[string]int
	latencies []time.Duration
}

func (w *window) add(status int, err error, d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.requests++
	if err != nil {
		w.errs[classify(err)]++
		return
	}
	w.status[status]++
	w.latencies = append(w.latencies, d)
}

func (w *window) drain() (int, map[int]int, map[string]int, []time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	r, s, e, l := w.requests, w.status, w.errs, w.latencies
	w.requests, w.status, w.errs, w.latencies = 0, map[int]int{}, map[string]int{}, nil
	return r, s, e, l
}

// classify reduces a client error to a bounded reason.
func classify(err error) string {
	var ne net.Error
	s := err.Error()
	switch {
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "timeout"
	case strings.Contains(s, "connection refused"):
		return "refused"
	case strings.Contains(s, "connection reset"):
		return "reset"
	case strings.Contains(s, "too many open files"):
		return "client_emfile"
	case strings.Contains(s, "EOF"):
		return "eof"
	case strings.Contains(s, "broken pipe"):
		return "broken_pipe"
	default:
		return "other"
	}
}

func pct(l []time.Duration, p float64) float64 {
	if len(l) == 0 {
		return 0
	}
	i := int(float64(len(l)-1) * p)
	return float64(l[i].Microseconds()) / 1000
}

type headers map[string]string

func (h headers) String() string { return fmt.Sprint(map[string]string(h)) }
func (h headers) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok {
		return fmt.Errorf("header %q: want K=V", s)
	}
	h[k] = v
	return nil
}

func main() {
	hdrs := headers{}
	url := flag.String("url", "", "target URL")
	workers := flag.Int("workers", 32, "concurrent workers")
	duration := flag.Duration("duration", 30*time.Second, "run length (0 = until signal)")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request timeout")
	fresh := flag.Bool("fresh", false, "new connection per request")
	unique := flag.Bool("unique-paths", false, "append a unique ?n= to every request")
	hold := flag.Int("hold", 0, "idle TCP connections to hold open")
	holdAddr := flag.String("hold-addr", "", "host:port for -hold (default: -url's host)")
	out := flag.String("out", "", "per-second JSONL output (default stdout)")
	flag.Var(hdrs, "header", "K=V request header (repeatable)")
	flag.Parse()
	if *url == "" {
		fmt.Fprintln(os.Stderr, "-url is required")
		os.Exit(2)
	}

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	}
	enc := json.NewEncoder(w)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sig; cancel() }()
	if *duration > 0 {
		var c context.CancelFunc
		ctx, c = context.WithTimeout(ctx, *duration)
		defer c()
	}

	var held []net.Conn
	var holdErrs map[string]int
	if *hold > 0 {
		addr := *holdAddr
		if addr == "" {
			addr = strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(*url, "http://"), "https://"), "/", 2)[0]
		}
		holdErrs = map[string]int{}
		for i := 0; i < *hold; i++ {
			c, err := net.DialTimeout("tcp", addr, 2*time.Second)
			if err != nil {
				holdErrs[classify(err)]++
				continue
			}
			held = append(held, c)
		}
		_ = enc.Encode(map[string]any{"t": time.Now().UTC().Format(time.RFC3339Nano), "held": len(held), "hold_errors": holdErrs})
	}

	tr := &http.Transport{MaxIdleConnsPerHost: *workers, DisableKeepAlives: *fresh, DialContext: (&net.Dialer{Timeout: *timeout}).DialContext}
	client := &http.Client{Transport: tr, Timeout: *timeout}
	win := &window{status: map[int]int{}, errs: map[string]int{}}
	var seq atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < *workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				u := *url
				if *unique {
					sep := "?"
					if strings.Contains(u, "?") {
						sep = "&"
					}
					u = fmt.Sprintf("%s%sn=%d", u, sep, seq.Add(1))
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
				if err != nil {
					return
				}
				for k, v := range hdrs {
					req.Header.Set(k, v)
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				if ctx.Err() != nil {
					if resp != nil {
						resp.Body.Close()
					}
					return
				}
				if err != nil {
					win.add(0, err, time.Since(t0))
					time.Sleep(5 * time.Millisecond)
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				win.add(resp.StatusCode, nil, time.Since(t0))
			}
		}()
	}

	var total, totalErr int
	statusTotal := map[int]int{}
	errTotal := map[string]int{}
	ticker := time.NewTicker(time.Second)
	flush := func() {
		r, s, e, l := win.drain()
		sort.Slice(l, func(i, j int) bool { return l[i] < l[j] })
		total += r
		for k, v := range s {
			statusTotal[k] += v
		}
		for k, v := range e {
			errTotal[k] += v
			totalErr += v
		}
		_ = enc.Encode(map[string]any{
			"t": time.Now().UTC().Format(time.RFC3339Nano), "requests": r, "status": s, "errors": e,
			"p50_ms": pct(l, 0.50), "p90_ms": pct(l, 0.90), "p99_ms": pct(l, 0.99), "max_ms": pct(l, 1),
		})
	}
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			flush()
		}
	}
	wg.Wait()
	flush()
	for _, c := range held {
		_ = c.Close()
	}
	tr.CloseIdleConnections()
	_ = enc.Encode(map[string]any{"summary": true, "requests": total, "status": statusTotal, "errors": errTotal, "held": len(held), "hold_errors": holdErrs})
	fmt.Fprintf(os.Stderr, "fault-load: %d requests, status %v, errors %v\n", total, statusTotal, errTotal)
}
