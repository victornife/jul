// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const fillerUnit = "<p>synthetic assessment response paragraph 0123456789</p>\n"

// Filler returns n bytes of the synthetic response, optionally prefixed by a
// marker, exactly as the archived origin produced them.
func Filler(n int, marker string) []byte {
	var b bytes.Buffer
	b.Grow(n + len(fillerUnit))
	b.WriteString(marker)
	for b.Len() < n {
		b.WriteString(fillerUnit)
	}
	return b.Bytes()[:n]
}

// Origin is the synthetic upstream. Fault, when set, deliberately breaks
// responses so the runner's correctness checks can be validated.
type Origin struct {
	Fault string // "" | truncate | corrupt | stall
	calls atomic.Int64
}

// ServeHTTP implements the archived origin plus optional faults.
func (o *Origin) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/__count" {
		fmt.Fprintf(w, "%d", o.calls.Load())
		return
	}
	o.calls.Add(1)
	n, _ := io.Copy(io.Discard, r.Body)
	q := r.URL.Query()
	size, _ := strconv.Atoi(q.Get("resp"))
	if size <= 0 {
		size = 1024
	}
	delay, _ := strconv.Atoi(q.Get("delay"))
	if o.Fault == "stall" {
		delay = 3_600_000
	}
	if delay > 0 {
		select {
		case <-time.After(time.Duration(delay) * time.Millisecond):
		case <-r.Context().Done():
			return
		}
	}
	marker := ""
	if q.Get("canary") == "waf" {
		marker = wafRespMark
	}
	body := Filler(size, marker)
	if o.Fault == "corrupt" && !strings.HasPrefix(r.URL.Path, "/__") {
		body = bytes.Clone(body)
		body[len(body)/2] ^= 0x20
	}
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
	if o.Fault == "truncate" {
		// Chunked framing ends cleanly early: the client sees a short 200.
		_, _ = w.Write(body[:len(body)/2])
		return
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
}
