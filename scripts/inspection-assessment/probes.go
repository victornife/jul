// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	UploadClass    string  `json:"upload_class"`
	UploadStatus   int     `json:"upload_status,omitempty"`
	UploadSeconds  float64 `json:"upload_seconds"`
	WatchdogFired  bool    `json:"watchdog_fired"`
	SiblingClass   string  `json:"sibling_class"`
	SiblingMs      float64 `json:"sibling_ms"`
	SiblingProto   string  `json:"sibling_proto,omitempty"`
	Dials          int64   `json:"connections_dialled"`
	WantDials      int64   `json:"connections_expected"`
	SameConnection bool    `json:"sibling_shared_connection"`
}

// Check separates instrument problems (the connection relationship or
// protocol was not the one the probe claims) from runtime findings (the
// warm-up or the sibling did not complete correctly).
func (r F4Result) Check() (instrument, findings []string) {
	id := r.Proto + "/" + r.Mode
	if r.Dials != r.WantDials {
		instrument = append(instrument, fmt.Sprintf("f4 %s: dialled %d connections, expected %d", id, r.Dials, r.WantDials))
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
func RunF4(ctx context.Context, t Target, f F4, proto, mode string) F4Result {
	r := F4Result{Proto: proto, Mode: mode, WantDials: 1}
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
	go func() {
		o := Outcome{Start: start}
		req, err := newRequest(upCtx, "POST", c.Base+"/proxy/echo?resp=16", pr, len(body))
		if err == nil {
			resp, derr := c.HTTP.Do(req)
			if derr == nil {
				_, err = io.Copy(io.Discard, resp.Body)
				_ = resp.Body.Close()
				o.Status = resp.StatusCode
			} else {
				err = derr
			}
		}
		o.End = time.Now()
		o.Class = "status"
		if err != nil {
			o.Class = errClass(err, false)
		}
		done <- o
	}()
	sleepCtx(ctx, f.SiblingAfter.D())
	sib := Do(ctx, c, Workload{Proto: proto, Method: "GET", Path: "/proxy/echo?resp=4096", RespBytes: 4096}, nil, t.WASM)
	r.SiblingClass, r.SiblingMs, r.SiblingProto = sib.Class, float64(sib.End.Sub(sib.Start).Microseconds())/1000, sib.Proto
	up := <-done
	r.UploadClass, r.UploadStatus = up.Class, up.Status
	r.UploadSeconds = round3(up.End.Sub(up.Start).Seconds())
	r.WatchdogFired = errors.Is(upCtx.Err(), context.DeadlineExceeded) && up.Class != "status"
	r.Dials = c.Dials()
	r.SameConnection = proto != "h1" && r.Dials == 1
	return r
}
