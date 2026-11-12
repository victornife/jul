// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/quic-go/quic-go/http3"
)

// The test binary doubles as the harness's origin subprocess and as a fake
// Jul, so the runner's process, evidence and accounting paths execute for
// real without a Jul build. The fake reproduces only the observable contract
// the harness verifies (listeners, canaries, evidence headers, metrics).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "origin":
			os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
		case "serve", "check", "version-key":
			os.Exit(fakeJul(os.Args[1:]))
		}
	}
	os.Exit(m.Run())
}

type fakeLoc struct {
	Match     struct{ Path string }
	Return    int
	ProxyPass string `toml:"proxy_pass"`
	Root      string
	Plugins   []string
	WAF       *struct{ Enabled bool } `toml:"waf"`
}

type fakeConfig struct {
	Admin  struct{ Listen string }
	Global struct {
		Inspection struct {
			MaxBuffered string `toml:"max_buffered_bytes"`
			Mode        string
		}
	}
	Upstreams []struct {
		Name    string
		Servers []struct{ Address string }
	}
	Plugins map[string]struct{ Config map[string]string }
	Servers []struct {
		Listen string
		TLS    struct {
			Enabled   bool
			Cert, Key string
		} `toml:"tls"`
		Locations []fakeLoc
	}
}

// fakeJul implements `version-key init`, `check` and `serve`. FAKE_JUL_MODE
// selects controlled misbehaviour: exit-at-startup, never-ready,
// ignore-term, crash-after-1s, http500 and accept-all-reloads (a reload below
// the 64 MiB fake route minimum is otherwise refused, as Jul refuses one
// below the compiled minimum).
func fakeJul(args []string) int {
	mode := os.Getenv("FAKE_JUL_MODE")
	switch args[0] {
	case "version-key":
		if len(args) == 4 && args[1] == "init" && args[2] == "--file" {
			if err := os.WriteFile(args[3], []byte("fake-key\n"), 0o600); err != nil {
				return 1
			}
			return 0
		}
		return 2
	case "check":
		b, err := os.ReadFile(args[2])
		if err != nil || bytes.Contains(b, []byte("THIS_IS_NOT_A_VARIABLE")) || mode == "check-fails" {
			fmt.Println("configuration rejected")
			return 1
		}
		fmt.Println("configuration ok")
		return 0
	}
	if os.Getenv("JUL_EFFECTIVE_VERSION_KEY_FILE") == "" {
		return 3
	}
	if mode == "exit-at-startup" {
		return 4
	}
	cfgPath := args[2]
	var reloads, refused atomic.Int64
	load := func() (*fakeConfig, error) {
		b, err := os.ReadFile(cfgPath)
		if err != nil {
			return nil, err
		}
		var c fakeConfig
		return &c, toml.Unmarshal(b, &c)
	}
	cfg, err := load()
	if err != nil {
		fmt.Println(err)
		return 5
	}
	var cur atomic.Pointer[fakeConfig]
	cur.Store(cfg)
	if mode == "never-ready" {
		select {}
	}
	origin := cfg.Upstreams[0].Servers[0].Address
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fakeServe(w, r, cur.Load(), origin, mode)
	})
	go func() {
		_ = http.ListenAndServe(cfg.Admin.Listen, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { //nolint:gosec // test fake
			if r.Header.Get("Authorization") != "Bearer "+adminToken {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/metrics" {
				fmt.Fprintf(w, "# HELP fake\ngo_goroutines 7\ngo_memstats_heap_inuse_bytes 1048576\ngo_memstats_heap_alloc_bytes 524288\n")
				fmt.Fprintf(w, "jul_reload_total{outcome=\"applied_live\",source=\"sighup\"} %d\n", reloads.Load())
				fmt.Fprintf(w, "jul_reload_total{outcome=\"not_applied\",source=\"sighup\"} %d\n", refused.Load())
				leases := 0
				if mode == "leak-lease" {
					leases = 1
				}
				fmt.Fprintf(w, "jul_inspection_leases %d\njul_inspection_bytes{class=\"base\"} 0\njul_inspection_bytes{class=\"buffer\"} 0\njul_inspection_bytes{class=\"waf_state\"} 0\n", leases)
				fmt.Fprintf(w, "jul_inspection_evaluations{state=\"active\"} 0\njul_inspection_evaluations{state=\"waiting\"} 0\n")
				return
			}
			if r.URL.Path == "/api/v1/status" {
				in := cur.Load().Global.Inspection
				mode := in.Mode
				if mode == "" {
					mode = "enforce"
				}
				fmt.Fprintf(w, `{"inspection":{"effective":{"mode":%q,"max_buffered_bytes":%d}}}`, mode, fakeMiB(in.MaxBuffered))
				return
			}
			_, _ = io.WriteString(w, "heap profile")
		}))
	}()
	cert, err := tls.LoadX509KeyPair(cfg.Servers[1].TLS.Cert, cfg.Servers[1].TLS.Key)
	if err != nil {
		fmt.Println(err)
		return 6
	}
	tc := &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2", "http/1.1"}, MinVersion: tls.VersionTLS12}
	plain := &http.Server{Addr: cfg.Servers[0].Listen, Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	secure := &http.Server{Addr: cfg.Servers[1].Listen, Handler: handler, TLSConfig: tc, ReadHeaderTimeout: 5 * time.Second}
	h3 := &http3.Server{Addr: cfg.Servers[1].Listen, Handler: handler, TLSConfig: http3.ConfigureTLSConfig(tc.Clone())}
	go func() { _ = plain.ListenAndServe() }()
	go func() {
		l, err := net.Listen("tcp", cfg.Servers[1].Listen)
		if err == nil {
			_ = secure.Serve(tls.NewListener(l, tc))
		}
	}()
	go func() { _ = h3.ListenAndServe() }()
	if mode == "crash-after-1s" {
		go func() { time.Sleep(time.Second); os.Exit(9) }()
	}
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, syscall.SIGHUP, syscall.SIGTERM)
	for s := range sig {
		if s == syscall.SIGHUP {
			c, err := load()
			switch {
			case err != nil:
			case c.Global.Inspection.MaxBuffered != "" && fakeMiB(c.Global.Inspection.MaxBuffered) < 64<<20 && mode != "accept-all-reloads":
				refused.Add(1)
			default:
				cur.Store(c)
				reloads.Add(1)
			}
			continue
		}
		if mode == "ignore-term" {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = plain.Shutdown(ctx)
		_ = secure.Shutdown(ctx)
		_ = h3.Close()
		cancel()
		return 0
	}
	return 0
}

func fakeMiB(s string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimSuffix(s, "m"), 10, 64)
	return n << 20
}

func fakeServe(w http.ResponseWriter, r *http.Request, cfg *fakeConfig, origin, mode string) {
	var loc *fakeLoc
	for i, l := range cfg.Servers[0].Locations {
		if r.URL.Path == l.Match.Path || (l.Return == 0 && strings.HasPrefix(r.URL.Path, l.Match.Path)) {
			loc = &cfg.Servers[0].Locations[i]
			break
		}
	}
	if loc == nil {
		http.NotFound(w, r)
		return
	}
	if loc.Return != 0 {
		w.WriteHeader(loc.Return)
		return
	}
	body, _ := io.ReadAll(r.Body)
	waf := loc.WAF != nil && loc.WAF.Enabled
	wasm := len(loc.Plugins) > 0
	pa := cfg.Plugins["inspect-a"].Config
	nobody := wasm && pa["state_bytes"] != ""
	subscribed := wasm && pa["subscribe"] == "true"
	if (waf && bytes.Contains(body, []byte(wafReqMark))) || (wasm && !nobody && bytes.Contains(body, []byte(wasmReqMark))) ||
		(nobody && r.Header.Get("X-Assess-Wasm-Block") != "") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if mode == "http500" {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	var status int
	var hdr http.Header
	var resp []byte
	if loc.Root != "" {
		b, err := os.ReadFile(filepath.Join(loc.Root, filepath.Clean(r.URL.Path)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		status, hdr, resp = http.StatusOK, http.Header{}, b
	} else {
		req, _ := http.NewRequestWithContext(r.Context(), r.Method, "http://"+origin+r.URL.RequestURI(), bytes.NewReader(body))
		switch {
		case nobody:
			req.Header.Set("X-Assess-"+pa["name"]+"-State", pa["state_bytes"])
			req.Header.Set("X-Assess-b-State", pa["state_bytes"])
		case wasm:
			req.Header.Set("X-Assess-"+pa["name"]+"-Req-Len", strconv.Itoa(len(body)))
			req.Header.Set("X-Assess-b-Req-Len", strconv.Itoa(len(body)))
		}
		or, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		resp, err = io.ReadAll(or.Body)
		_ = or.Body.Close()
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		status, hdr = or.StatusCode, or.Header
	}
	if waf && bytes.Contains(resp, []byte(wafRespMark)) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	for _, k := range []string{"X-Origin-Req-Len", "X-Echo-A-Req-Len", "X-Echo-B-Req-Len", "X-Echo-A-State", "X-Echo-B-State", "Cache-Control"} {
		if v := hdr.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	switch {
	case nobody:
		w.Header().Set("X-Assess-"+pa["name"]+"-Resp", "state-"+pa["state_bytes"])
		w.Header().Set("X-Assess-b-Resp", "state-"+pa["state_bytes"])
	case subscribed:
		w.Header().Set("X-Assess-"+pa["name"]+"-Resp", strconv.Itoa(len(resp)))
		w.Header().Set("X-Assess-b-Resp", strconv.Itoa(len(resp)))
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(resp)))
	w.WriteHeader(status)
	_, _ = w.Write(resp)
}
