// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"

	"jul/internal/config"
	"jul/internal/inspection"
)

// hostFn is one imported host function whose parameters and results are all
// i32, as every jul host function used by these guests is.
type hostFn struct {
	name            string
	params, results int
}

var (
	hfWriteBody = hostFn{"write_response_body", 2, 0}
	hfStatus    = hostFn{"set_response_status", 1, 0}
	hfFetch     = hostFn{"fetch", 8, 1}
	hfState     = hostFn{"set_request_state", 2, 1}
	hfSubscribe = hostFn{"subscribe_response", 1, 1}
)

// hostGuest assembles a module importing fns from abi's host module, with
// pages of memory initialised from data (offset → bytes), whose
// handle_request runs body (one i32 local) and must leave an i32 action.
type hostGuest struct {
	abi   string
	fns   []hostFn
	pages byte
	data  map[int32][]byte
	body  []byte
	// respond exports a v2 handle_response that returns Continue.
	respond bool
}

func (g hostGuest) wasm() []byte {
	host := hostModuleV1
	if g.abi == ABIJulV2 {
		host = hostModuleV2
	}
	var types, imports [][]byte
	for i, f := range g.fns {
		ty := []byte{0x60, byte(f.params)}
		for range f.params {
			ty = append(ty, i32)
		}
		ty = append(ty, byte(f.results))
		for range f.results {
			ty = append(ty, i32)
		}
		types = append(types, ty)
		imports = append(imports, append(append(wasmName(host), wasmName(f.name)...), 0x00, byte(i)))
	}
	n := len(g.fns)
	types = append(types, []byte{0x60, 0x00, 0x01, i32}, []byte{0x60, 0x00, 0x00})
	funcs := [][]byte{leb(n)}
	exports := [][]byte{append(wasmName(exportHandleRequest), append([]byte{0x00}, leb(n)...)...)}
	fb := append([]byte{0x01, 0x01, i32}, g.body...)
	fb = append(fb, 0x0b)
	code := [][]byte{append(leb(len(fb)), fb...)}
	if g.abi == ABIJulV2 {
		funcs = append(funcs, leb(n+1))
		exports = append(exports, append(wasmName(exportABIV2Marker), append([]byte{0x00}, leb(n+1)...)...))
		code = append(code, []byte{0x02, 0x00, 0x0b})
		if g.respond {
			funcs = append(funcs, leb(n))
			exports = append(exports, append(wasmName(exportHandleResponse), append([]byte{0x00}, leb(n+2)...)...))
			code = append(code, []byte{0x04, 0x00, 0x41, 0x00, 0x0b})
		}
	}
	pages := g.pages
	if pages == 0 {
		pages = 2
	}
	var data [][]byte
	for off, b := range g.data {
		seg := append([]byte{0x00}, i32c(off)...)
		seg = append(seg, 0x0b)
		seg = append(seg, leb(len(b))...)
		data = append(data, append(seg, b...))
	}
	out := append([]byte(nil), wasmHeader...)
	out = append(out, wasmSection(1, types)...)
	out = append(out, wasmSection(2, imports)...)
	out = append(out, wasmSection(3, funcs)...)
	out = append(out, wasmSection(5, [][]byte{{0x00, pages}})...)
	out = append(out, wasmSection(7, exports)...)
	out = append(out, wasmSection(10, code)...)
	if len(data) > 0 {
		out = append(out, wasmSection(11, data)...)
	}
	return out
}

// i32c encodes i32.const v.
func i32c(v int32) []byte {
	out := []byte{0x41}
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

// call encodes a call of function index i, given its i32 arguments.
func call(i int, args ...int32) []byte {
	var out []byte
	for _, a := range args {
		out = append(out, i32c(a)...)
	}
	return append(append(out, 0x10), leb(i)...)
}

const (
	opDrop        = 0x1a
	opUnreachable = 0x00
)

func seq(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func (g hostGuest) config(opts ...func(*config.PluginConfig)) config.PluginConfig {
	all := append([]func(*config.PluginConfig){
		inlineSrc(g.wasm()),
		func(pc *config.PluginConfig) {
			pc.Path = ""
			pc.ABI = g.abi
			pc.Timeout = config.Duration(2 * time.Second)
		},
	}, opts...)
	return pcfg("", all...)
}

// govRecorder records the governor hooks under test.
type govRecorder struct {
	mu         sync.Mutex
	rejections map[string]int
	timeouts   map[string]int
}

func (r *govRecorder) rejected(reason inspection.Outcome, mode inspection.Mode) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rejections[string(reason)+"/"+string(mode)]++
}

func (r *govRecorder) timedOut(protocol, result string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timeouts[protocol+"/"+result]++
}

func (r *govRecorder) snapshot() (rejections, timeouts map[string]int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return maps(r.rejections), maps(r.timeouts)
}

// govManager is bodyManager with a governor the test controls.
func govManager(t *testing.T, limits inspection.Limits) (*Manager, *bodyHooks, *inspection.Governor, *govRecorder) {
	t.Helper()
	rec := &govRecorder{rejections: map[string]int{}, timeouts: map[string]int{}}
	g, err := inspection.New(limits, inspection.Hooks{OnRejection: rec.rejected, OnBodyTimeout: rec.timedOut})
	if err != nil {
		t.Fatal(err)
	}
	h := &bodyHooks{results: map[string]int{}, failed: make(chan string, 16)}
	m, err := NewManager(Options{Inspection: g,
		Logger:               slog.New(slog.NewTextHandler(io.Discard, nil)),
		OnInvocation:         h.invoked,
		OnPanic:              func(string) { h.mu.Lock(); h.panics++; h.mu.Unlock() },
		OnInstanceWait:       func(string, string) { h.mu.Lock(); h.waits++; h.mu.Unlock() },
		OnRequestBodyFailure: h.fail,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = m.cache.Close(context.Background())
	if m.cache, err = wazero.NewCompilationCacheWithDir(testCompilationCacheDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, h, g, rec
}

// limitsWith returns testLimits with MaxBytes set so that exactly extra
// buffer bytes fit beside one lease's base charge.
func limitsWith(extra uint64, mut ...func(*inspection.Limits)) inspection.Limits {
	l := testLimits()
	l.MaxBytes = l.BaseChargeBytes + extra
	for _, f := range mut {
		f(&l)
	}
	return l
}

// assertDrained checks that every lease closed and every charge was refunded.
func assertDrained(t *testing.T, g *inspection.Governor) {
	t.Helper()
	s := g.Snapshot()
	if s.ActiveLeases != 0 || s.Usage != [3]uint64{} {
		t.Fatalf("governor not drained: leases=%d usage=%v", s.ActiveLeases, s.Usage)
	}
}
