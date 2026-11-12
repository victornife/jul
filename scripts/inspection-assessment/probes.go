// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// F4Result records one stalled/slow upload with its sibling request.
type F4Result struct {
	Proto  string `json:"proto"`
	Mode   string `json:"mode"`
	Warmup string `json:"warmup_class"`
	// Upload is the stalled request's outcome; WatchdogFired means the
	// client limit, not the runtime, ended it.
	UploadClass   string  `json:"upload_class"`
	UploadStatus  int     `json:"upload_status,omitempty"`
	UploadSeconds float64 `json:"upload_seconds"`
	WatchdogFired bool    `json:"watchdog_fired"`
	// Expect is the planned upload outcome (UploadVerified, UploadNoDeadline
	// or UploadDeadline); Deadline is the configured body_timeout in seconds
	// when it applies. Runs before plan v2 recorded neither.
	Expect   string  `json:"upload_expected,omitempty"`
	Deadline float64 `json:"upload_deadline_seconds,omitempty"`
	// UploadOutcome classifies what ended the upload.
	UploadOutcome  string  `json:"upload_outcome,omitempty"`
	SiblingClass   string  `json:"sibling_class"`
	SiblingMs      float64 `json:"sibling_ms"`
	SiblingProto   string  `json:"sibling_proto,omitempty"`
	Dials          int64   `json:"connections_dialled"`
	WantDials      int64   `json:"connections_expected"`
	SameConnection bool    `json:"sibling_shared_connection"`
}

// Planned F4 upload outcomes.
const (
	// UploadVerified: a slow upload completes with a fully verified 2xx.
	UploadVerified = "verified"
	// UploadNoDeadline: no inspection deadline applies to the stalled
	// upload; the client watchdog or another runtime limit ends it.
	UploadNoDeadline = "no-inspection-deadline"
	// UploadDeadline: the configured body_timeout must end the stalled
	// upload with 408 close to the deadline, before the watchdog.
	UploadDeadline = "body-timeout-408"
	// UploadReadTimeout: the listener read_timeout (H1/H2 only) is shorter
	// than the upload, so Jul must end it close to that limit. The status
	// depends on the layer reading the body and is not judged.
	UploadReadTimeout = "read-timeout"
)

func positive(s string) (time.Duration, bool) {
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}

// F4Expect derives the planned upload outcome and its bound in seconds.
// read_timeout bounds the whole H1/H2 request (not HTTP/3, as documented in
// docs/plugins.md). body_timeout applies only while an inspection layer reads
// the body, so it never applies to none. The earlier bound wins.
func F4Expect(sc Scenario, proto, mode, variant string) (string, float64) {
	upload := time.Duration(1<<62 - 1)
	if mode != "stall" {
		upload = time.Duration(sc.F4.BodyBytes/sc.F4.ChunkBytes) * sc.F4.SlowInterval.D()
	}
	rt, hasRT := positive(sc.ReadTimeout)
	hasRT = hasRT && proto != "h3" && rt < upload
	bt, hasBT := positive(sc.BodyTimeout)
	hasBT = hasBT && variant != "none" && bt < upload
	switch {
	case hasRT && (!hasBT || rt <= bt):
		return UploadReadTimeout, rt.Seconds()
	case hasBT:
		return UploadDeadline, bt.Seconds()
	case mode != "stall":
		return UploadVerified, 0
	}
	return UploadNoDeadline, 0
}

// classifyUpload names what ended the upload: "verified", "client-watchdog",
// "runtime-ended:<class>" or "unexpected:<class>".
func (r F4Result) classifyUpload() string {
	switch {
	case r.UploadClass == "ok":
		return "verified"
	case r.WatchdogFired:
		return "client-watchdog"
	case r.UploadClass == "status:408", strings.HasPrefix(r.UploadClass, "error:") && r.UploadClass != "error:client" && r.UploadClass != "error:build":
		return "runtime-ended:" + r.UploadClass
	}
	return "unexpected:" + r.UploadClass
}

// deadlineSlack bounds how late a body_timeout 408 may arrive.
const deadlineSlack = 3.0

// Check separates instrument problems (the connection relationship or
// protocol was not the one the probe claims) from runtime findings (the
// warm-up, the sibling or the upload did not end as planned).
func (r F4Result) Check() (instrument, findings []string) {
	id := r.Proto + "/" + r.Mode
	if r.Dials != r.WantDials {
		instrument = append(instrument, fmt.Sprintf("f4 %s: dialled %d connections, expected %d", id, r.Dials, r.WantDials))
	}
	up := fmt.Sprintf("f4 %s: upload %s after %.1f s", id, r.UploadClass, r.UploadSeconds)
	switch out := r.classifyUpload(); {
	case r.UploadClass == "error:client" || r.UploadClass == "error:build" || strings.HasPrefix(r.UploadClass, "unverified:proto"):
		instrument = append(instrument, up)
	case r.Expect == "":
		// Plan v1 runs did not verify the upload; they are not re-judged.
	case r.Expect == UploadVerified:
		if out != "verified" {
			findings = append(findings, up+": expected a verified response")
		}
	case r.Expect == UploadNoDeadline:
		if out == "verified" || strings.HasPrefix(out, "unexpected:") {
			findings = append(findings, up+": a stalled upload must not succeed or fail with another status")
		}
	case r.Expect == UploadReadTimeout:
		if out == "verified" || r.WatchdogFired || r.UploadSeconds < 0.9*r.Deadline || r.UploadSeconds > r.Deadline+deadlineSlack {
			findings = append(findings, fmt.Sprintf("%s (%s): expected read_timeout %.0f s to end it within %.1f–%.1f s", up, out, r.Deadline, 0.9*r.Deadline, r.Deadline+deadlineSlack))
		}
	case r.Expect == UploadDeadline:
		if r.UploadClass != "status:408" || r.UploadSeconds < 0.9*r.Deadline || r.UploadSeconds > r.Deadline+deadlineSlack {
			findings = append(findings, fmt.Sprintf("%s (%s): expected 408 from body_timeout %.0f s within %.1f–%.1f s", up, out, r.Deadline, 0.9*r.Deadline, r.Deadline+deadlineSlack))
		}
	default:
		instrument = append(instrument, fmt.Sprintf("f4 %s: unknown upload expectation %q", id, r.Expect))
	}
	for _, c := range []struct{ name, class string }{{"warm-up", r.Warmup}, {"sibling", r.SiblingClass}} {
		switch {
		case c.class == "ok":
		case strings.HasPrefix(c.class, "unverified:proto"), c.class == "error:client":
			instrument = append(instrument, fmt.Sprintf("f4 %s: %s %s", id, c.name, c.class))
		default:
			findings = append(findings, fmt.Sprintf("f4 %s: %s %s", id, c.name, c.class))
		}
	}
	return instrument, findings
}

// RunF4 performs one F4 case. H2/H3 share one connection between the stalled
// upload and the sibling; H1 needs a second connection by design.
func RunF4(ctx context.Context, t Target, f F4, proto, mode, expect string, deadline float64) F4Result {
	r := F4Result{Proto: proto, Mode: mode, WantDials: 1, Expect: expect, Deadline: deadline}
	if proto == "h1" {
		r.WantDials = 2
	}
	c, err := NewClient(proto, t, 0, f.ClientLimit.D())
	if err != nil {
		r.UploadClass = "error:client"
		return r
	}
	defer c.Close()
	small := Workload{Proto: proto, Method: "GET", Path: "/proxy/echo?resp=16", RespBytes: 16}
	r.Warmup = Do(ctx, c, small, nil, t.WASM).Class
	body := WideBody(f.BodyBytes, "")
	pr, pw := io.Pipe()
	// The external watchdog owns the upload: when it expires the request is
	// cancelled and the pipe is closed, so a transport blocked reading the
	// request body cannot outlive it.
	upCtx, cancel := context.WithTimeout(ctx, f.ClientLimit.D())
	defer cancel()
	stop := context.AfterFunc(upCtx, func() { _ = pr.CloseWithError(context.DeadlineExceeded) })
	defer stop()
	go func() {
		chunk := f.ChunkBytes
		if mode == "stall" {
			_, _ = pw.Write(body[:chunk])
			<-upCtx.Done()
			_ = pw.CloseWithError(upCtx.Err())
			return
		}
		for off := 0; off < len(body); off += chunk {
			if _, err := pw.Write(body[off:min(off+chunk, len(body))]); err != nil {
				return
			}
			sleepCtx(upCtx, f.SlowInterval.D())
		}
		_ = pw.Close()
	}()
	done := make(chan Outcome, 1)
	start := time.Now()
	upload := Workload{Proto: proto, Method: "POST", Path: "/proxy/echo?resp=16", RespBytes: 16, WantEcho: true}
	go func() {
		o := Outcome{Start: start}
		req, err := newRequest(upCtx, upload.Method, c.Base+upload.Path, pr, len(body))
		if err != nil {
			o.End, o.Class = time.Now(), "error:build"
			done <- o
			return
		}
		resp, err := c.HTTP.Do(req)
		if err != nil {
			o.End, o.Class = time.Now(), errClass(err, false)
			done <- o
			return
		}
		got, rerr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		o.End, o.Status = time.Now(), resp.StatusCode
		switch {
		case rerr != nil:
			o.Class = "error:body-" + strings.TrimPrefix(errClass(rerr, false), "error:")
		case resp.StatusCode/100 != 2:
			o.Class = "status:" + strconv.Itoa(resp.StatusCode)
		default:
			o.Class = "ok"
			if why := Verify(resp, got, upload, len(body), t.WASM, c.Proto); why != "" {
				o.Class = "unverified:" + why
			}
		}
		done <- o
	}()
	sleepCtx(ctx, f.SiblingAfter.D())
	sib := Do(ctx, c, Workload{Proto: proto, Method: "GET", Path: "/proxy/echo?resp=4096", RespBytes: 4096}, nil, t.WASM)
	r.SiblingClass, r.SiblingMs, r.SiblingProto = sib.Class, float64(sib.End.Sub(sib.Start).Microseconds())/1000, sib.Proto
	up := <-done
	r.UploadClass, r.UploadStatus = up.Class, up.Status
	r.UploadSeconds = round3(up.End.Sub(up.Start).Seconds())
	r.WatchdogFired = errors.Is(upCtx.Err(), context.DeadlineExceeded) && strings.HasPrefix(up.Class, "error:")
	r.Dials = c.Dials()
	r.SameConnection = proto != "h1" && r.Dials == 1
	r.UploadOutcome = r.classifyUpload()
	return r
}
