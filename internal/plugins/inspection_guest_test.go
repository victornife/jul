// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/plugins/wasmtest"
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
	hfRespHdr   = hostFn{"set_response_header", 4, 0}
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
	if s.ActiveLeases != 0 || s.Usage != ([len(s.Usage)]uint64{}) {
		t.Fatalf("governor not drained: leases=%d usage=%v", s.ActiveLeases, s.Usage)
	}
}

func TestGuestCapacityEnforceTracksReuseAndCleansFailedCandidate(t *testing.T) {
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 32 << 20
	m, _, g, _ := govManager(t, limits)
	old, err := m.Build(context.Background(), map[string]config.PluginConfig{
		"old": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	oldPlugin := old.plugins["old"]
	oldTicket := onlyGuestTicket(t, oldPlugin)
	if oldTicket.Generation() != oldPlugin.generation {
		t.Fatalf("old ticket generation = %d, plugin generation = %d", oldTicket.Generation(), oldPlugin.generation)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestIdle] != 1 {
		t.Fatalf("old generation reservation = %+v", got)
	}

	pm, err := oldPlugin.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestBusy] != 1 {
		t.Fatalf("busy reuse changed capacity accounting: %+v", got)
	}
	oldPlugin.release(pm)
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestIdle] != 1 {
		t.Fatalf("idle reuse changed capacity accounting: %+v", got)
	}

	_, err = m.Build(context.Background(), map[string]config.PluginConfig{
		"candidate-a": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
		"candidate-b": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
	})
	if !inspection.IsOutcome(err, inspection.GuestCapacitySaturated) {
		t.Fatalf("candidate build error = %v, want guest-capacity refusal", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 32<<20 ||
		got.Instances[inspection.GuestIdle] != 1 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
		t.Fatalf("failed candidate debt = %+v", got)
	}

	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 32<<20 ||
		got.Instances[inspection.GuestTeardownUnconfirmed] != 2 {
		t.Fatalf("retired generation debt = %+v", got)
	}
}

func TestFailedGuestCandidateReportsPriorRuntimeCloseFailure(t *testing.T) {
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 64 << 20
	m, _, g, _ := govManager(t, limits)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeErr := errors.New("candidate runtime close failed")
	var actualRuntime wazero.Runtime
	var reads int
	afterModuleRead = func(string) {
		reads++
		if reads != 2 {
			return
		}
		m.liveMu.Lock()
		for p := range m.live {
			actualRuntime = p.runtime
			p.runtime = closeErrorRuntime{Runtime: actualRuntime, err: closeErr}
		}
		m.liveMu.Unlock()
		cancel()
	}
	t.Cleanup(func() {
		afterModuleRead = nil
		if actualRuntime != nil {
			_ = actualRuntime.Close(context.Background())
		}
	})
	_, err := m.Build(ctx, map[string]config.PluginConfig{
		"a": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
		"b": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, closeErr) {
		t.Errorf("failed candidate = %v, want cancellation and prior runtime close failure", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 ||
		got.Instances[inspection.GuestFailedRetained] != 1 {
		t.Errorf("failed runtime teardown lost its capacity debt: %+v", got)
	}
}

func TestGuestCapacityStatsIncludeStartingCandidate(t *testing.T) {
	m, _, g, _ := govManager(t, testLimits())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var buildErr error
	go func() {
		set, err := m.Build(ctx, map[string]config.PluginConfig{
			"starting": pcfg("", inlineSrc(wasmtest.StartLoopV2()), func(pc *config.PluginConfig) {
				pc.Path = ""
				pc.ABI = ABIJulV2
				pc.MemoryLimit = config.Size(16 << 20)
			}),
		})
		if set != nil {
			_ = set.Close()
		}
		buildErr = err
		close(done)
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("starting candidate did not stop after cancellation")
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for g.Snapshot().GuestCapacity.Instances[inspection.GuestStarting] == 0 {
		select {
		case <-done:
			t.Fatalf("candidate startup ended before observation: %v", buildErr)
		case <-timer.C:
			t.Fatal("candidate startup did not reserve guest capacity")
		case <-ticker.C:
		}
	}
	stats, err := m.GuestCapacityStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.LiveInstances != 1 || stats.MaxInstances != defaultMaxInstances ||
		stats.MaxPermittedBytes != defaultMaxInstances*(16<<20) {
		t.Fatalf("starting candidate missing from manager capacity stats: %+v", stats)
	}
}

func TestGuestCapacityObserveAllowsAndCountsOverLimitGenerations(t *testing.T) {
	limits := testLimits()
	limits.Mode = inspection.Observe
	limits.MaxGuestCapacityBytes = 16 << 20
	m, _, g, recorder := govManager(t, limits)
	cfg := func(name string) map[string]config.PluginConfig {
		return map[string]config.PluginConfig{
			name: pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
		}
	}
	old, err := m.Build(context.Background(), cfg("old"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = old.Close() })
	candidate, err := m.Build(context.Background(), cfg("candidate"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = candidate.Close() })
	oldTicket := onlyGuestTicket(t, old.plugins["old"])
	candidateTicket := onlyGuestTicket(t, candidate.plugins["candidate"])
	if oldTicket.Generation() == candidateTicket.Generation() {
		t.Fatalf("overlapping sets share generation %d", oldTicket.Generation())
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 32<<20 ||
		got.Instances[inspection.GuestIdle] != 2 || got.Draining {
		t.Fatalf("observe-mode overlap = %+v", got)
	}
	rejections, _ := recorder.snapshot()
	if got := rejections["guest_capacity/observe"]; got != 1 {
		t.Fatalf("observe guest-capacity decisions = %d, want 1", got)
	}
}

func TestGuestRetirementDebtAdmissionAndLimitIncrease(t *testing.T) {
	for _, mode := range []inspection.Mode{inspection.Enforce, inspection.Observe} {
		t.Run(string(mode), func(t *testing.T) {
			limits := testLimits()
			limits.Mode = mode
			limits.MaxGuestCapacityBytes = 16 << 20
			m, _, g, recorder := govManager(t, limits)
			cfg := map[string]config.PluginConfig{
				"guest": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
			}
			old, err := m.Build(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := old.Close(); err != nil {
				t.Fatal(err)
			}
			candidate, err := m.Build(context.Background(), cfg)
			if mode == inspection.Enforce {
				if !inspection.IsOutcome(err, inspection.GuestCapacitySaturated) || candidate != nil {
					t.Fatalf("admission with debt = %v, %v", candidate, err)
				}
				limits.MaxGuestCapacityBytes = 32 << 20
				if err := g.PublishLimitsIfVersion(g.Snapshot().LimitsVersion, limits); err != nil {
					t.Fatal(err)
				}
				candidate, err = m.Build(context.Background(), cfg)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 32<<20 ||
				got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || got.Instances[inspection.GuestIdle] != 1 {
				t.Fatalf("debt plus candidate = %+v", got)
			}
			rejections, _ := recorder.snapshot()
			if got := rejections["guest_capacity/"+string(mode)]; got != 1 {
				t.Fatalf("saturation decisions = %d, want 1", got)
			}
			if err := candidate.Close(); err != nil {
				t.Fatal(err)
			}
			if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 32<<20 ||
				got.Instances[inspection.GuestTeardownUnconfirmed] != 2 {
				t.Fatalf("candidate retirement debt = %+v", got)
			}
		})
	}
}

func TestFailedGuestCandidateDebtRespectsModes(t *testing.T) {
	for _, mode := range []inspection.Mode{inspection.Enforce, inspection.Observe} {
		t.Run(string(mode), func(t *testing.T) {
			limits := testLimits()
			limits.Mode = mode
			limits.MaxGuestCapacityBytes = 64 << 10
			m, _, g, recorder := govManager(t, limits)
			wasm := assemble(nil, []wasmFunc{{export: "_initialize", body: []byte{opUnreachable}}, fnRequest})
			cfg := map[string]config.PluginConfig{"bad": pcfg("", inlineSrc(wasm), func(pc *config.PluginConfig) {
				pc.Path = ""
				pc.MemoryLimit = config.Size(64 << 10)
			})}
			if set, err := m.Build(context.Background(), cfg); err == nil || set != nil {
				t.Fatalf("trapping candidate = %v, %v", set, err)
			}
			if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
				t.Fatalf("trapping candidate debt = %+v", got)
			}
			set, err := m.Build(context.Background(), cfg)
			if set != nil || err == nil {
				t.Fatalf("second trapping candidate = %v, %v", set, err)
			}
			if saturated := inspection.IsOutcome(err, inspection.GuestCapacitySaturated); saturated != (mode == inspection.Enforce) {
				t.Fatalf("second candidate saturation = %t: %v", saturated, err)
			}
			want := uint64(64 << 10)
			if mode == inspection.Observe {
				want *= 2
			}
			if got := g.Snapshot().GuestCapacity; got.ReservedBytes != want {
				t.Fatalf("repeated candidate debt = %+v, want %d", got, want)
			}
			rejections, _ := recorder.snapshot()
			if got := rejections["guest_capacity/"+string(mode)]; got != 1 {
				t.Fatalf("saturation decisions = %d, want 1", got)
			}
		})
	}
}

func TestGuestHelpersWithoutGovernorAreNoops(t *testing.T) {
	p := &plugin{}
	if ticket, err := p.reserveGuest(); ticket != nil || err != nil {
		t.Fatalf("reserve without governor = %v, %v", ticket, err)
	}
	if err := p.transitionGuest(nil, inspection.GuestIdle); err != nil {
		t.Fatalf("nil-ticket transition = %v", err)
	}
	if released, err := p.resolveGuest(nil, nil); !released || err != nil {
		t.Fatalf("nil-ticket teardown = %v, %v", released, err)
	}
	if released, err := p.resolveGuest(nil, errors.New("unconfirmed")); released || err != nil {
		t.Fatalf("failed nil-ticket teardown = %v, %v", released, err)
	}
	if p.hasGuestTickets() {
		t.Fatal("empty plugin has guest tickets")
	}
	p.finishGuestClose()
	p.discard(nil)

	var nilPlugin *plugin
	if nilPlugin.hasGuestTickets() {
		t.Fatal("nil plugin has guest tickets")
	}
	nilPlugin.discard(nil)
}

func TestGuestCapacityRemainsChargedUntilRuntimeCloseAfterModuleCloseFailure(t *testing.T) {
	g := testGovernor(t)
	var logs bytes.Buffer
	m, err := NewManager(Options{Inspection: g, Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := g.ReserveGuestCapacity(1, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	rtCloseErr := errors.New("runtime close failed")
	p := &plugin{
		name:         "close-failure",
		runtime:      closeErrorRuntime{err: rtCloseErr},
		gov:          g,
		mgr:          m,
		log:          m.log,
		generation:   1,
		guestTickets: map[*inspection.GuestTicket]*guestOwnership{ticket: {active: true}},
		slots:        make(chan struct{}, 1),
	}
	p.slots <- struct{}{}
	moduleCloseErr := errors.New("module close failed")
	p.discard(&pooledModule{mod: closeErrorModule{err: moduleCloseErr}, ticket: ticket})
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestFailedRetained] != 1 {
		t.Fatalf("module close failure did not retain capacity: %+v", got)
	}
	if len(p.guestTickets) != 1 || len(p.slots) != 1 {
		t.Fatalf("failed close lost owner state: tickets=%d slots=%d", len(p.guestTickets), len(p.slots))
	}
	firstClose := p.close()
	if !errors.Is(firstClose, rtCloseErr) || !errors.Is(firstClose, moduleCloseErr) {
		t.Fatalf("plugin close error = %v, want module and runtime failures", firstClose)
	}
	if !strings.Contains(logs.String(), "plugin runtime teardown failed") {
		t.Error("runtime close failure was not logged")
	}
	if stats, err := m.GuestCapacityStats(); err != nil ||
		stats.LiveInstances != 1 || stats.MaxInstances != 1 {
		t.Fatalf("retained close stats = %+v, %v", stats, err)
	}
	if retry := m.Close(); !errors.Is(retry, rtCloseErr) || !errors.Is(retry, moduleCloseErr) || retry.Error() != firstClose.Error() {
		t.Fatalf("manager retry = %v, want stable module and runtime failures from %v", retry, firstClose)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 ||
		got.Instances[inspection.GuestFailedRetained] != 1 {
		t.Fatalf("runtime retry lost unconfirmed capacity: %+v", got)
	}
	m.retainedMu.Lock()
	defer m.retainedMu.Unlock()
	if len(m.retained) != 1 {
		t.Fatalf("manager retained %d plugins after failed teardown, want 1", len(m.retained))
	}
}

func TestGuestRuntimeCloseFailureFromIdleRetainsDebtOnRetry(t *testing.T) {
	g := testGovernor(t)
	ticket, err := g.ReserveGuestCapacity(1, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	if err := ticket.Transition(inspection.GuestIdle); err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("runtime close failed")
	p := &plugin{
		name: "idle-close", runtime: closeErrorRuntime{err: closeErr},
		guestTickets: map[*inspection.GuestTicket]*guestOwnership{ticket: {}},
		pool:         make(chan *pooledModule, 1), slots: make(chan struct{}, 1),
	}
	p.pool <- &pooledModule{ticket: ticket}
	p.slots <- struct{}{}
	first := p.close()
	if !errors.Is(first, closeErr) || inspection.IsOutcome(first, inspection.InvalidAccounting) {
		t.Fatalf("first close = %v, want runtime failure without accounting error", first)
	}
	before := g.Snapshot().GuestCapacity
	if before.ReservedBytes != 16<<20 || before.Instances[inspection.GuestFailedRetained] != 1 {
		t.Fatalf("idle runtime failure lost debt: %+v", before)
	}
	retry := p.closeRuntime(context.Background())
	if !errors.Is(retry, closeErr) || retry.Error() != first.Error() {
		t.Fatalf("runtime retry = %v, want stable original failure %v", retry, first)
	}
	if got := g.Snapshot().GuestCapacity; got != before || len(p.slots) != 1 {
		t.Fatalf("runtime retry changed debt/slot: %+v, slots=%d", got, len(p.slots))
	}
}

type poolWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *poolWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestGuestCapacityRemainsChargedDuringPoolHandoff(t *testing.T) {
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 16 << 20
	m, _, g, _ := govManager(t, limits)
	set, err := m.Build(context.Background(), map[string]config.PluginConfig{
		"guest": pcfg("header-inject", func(pc *config.PluginConfig) {
			pc.MemoryLimit = config.Size(16 << 20)
			pc.MaxInstances = 1
			pc.Timeout = config.Duration(10 * time.Second)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	p := set.plugins["guest"]
	pm, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if pm != nil {
			p.release(pm)
		}
	})

	received, resume := make(chan struct{}), make(chan struct{})
	var resumeOnce sync.Once
	t.Cleanup(func() { resumeOnce.Do(func() { close(resume) }) })
	p.onWait = func(_ string, result string) {
		if result == instanceWaitAcquired {
			close(received)
			<-resume
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &poolWaitContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := p.acquire(waitCtx)
		done <- err
	}()
	select {
	case <-waitCtx.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("second acquisition did not begin waiting")
	}
	p.release(pm)
	pm = nil
	select {
	case <-received:
	case <-time.After(5 * time.Second):
		t.Fatal("second acquisition did not receive the pooled guest")
	}

	closeErr := p.close()
	during := g.Snapshot().GuestCapacity
	candidate, admissionErr := g.ReserveGuestCapacity(2, 16<<20)
	if candidate != nil {
		if err := candidate.Transition(inspection.GuestClosing); err != nil {
			t.Errorf("close unexpectedly admitted candidate: %v", err)
		} else if err := candidate.ResolveTeardown(nil); err != nil {
			t.Errorf("release unexpectedly admitted candidate: %v", err)
		}
	}
	resumeOnce.Do(func() { close(resume) })
	select {
	case err := <-done:
		if !errors.Is(err, errPluginClosed) {
			t.Errorf("acquisition during retirement = %v, want plugin closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("acquisition did not finish after retirement")
	}
	if !errors.Is(closeErr, errGuestWorkInFlight) || during.ReservedBytes != 16<<20 {
		t.Errorf("retirement during pool handoff = %v, capacity = %+v; want in-flight error and 16 MiB retained",
			closeErr, during)
	}
	if !inspection.IsOutcome(admissionErr, inspection.GuestCapacitySaturated) {
		t.Errorf("candidate admission during pool handoff = %v, want guest-capacity refusal", admissionErr)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
		t.Errorf("completed pool handoff lost debt: %+v", got)
	}
	if err := m.Close(); err != nil {
		t.Errorf("manager close after pool handoff: %v", err)
	}
}

func TestGuestRetirementClearsReleasedModuleAlias(t *testing.T) {
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 16 << 20
	m, _, g, _ := govManager(t, limits)
	set, err := m.Build(context.Background(), map[string]config.PluginConfig{
		"guest": pcfg("header-inject", func(pc *config.PluginConfig) { pc.MemoryLimit = config.Size(16 << 20) }),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	p := set.plugins["guest"]
	pm, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.release(pm)
	if err := p.close(); err != nil {
		t.Fatal(err)
	}
	if pm.mod != nil {
		t.Fatalf("released wrapper still retained %d bytes",
			pm.mod.Memory().Size())
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
		t.Errorf("retired guest debt = %+v", got)
	}
}

func TestGuestRetirementWakesCapacityWaiters(t *testing.T) {
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 16 << 20
	m, _, g, _ := govManager(t, limits)
	set, err := m.Build(context.Background(), map[string]config.PluginConfig{
		"guest": pcfg("header-inject", func(pc *config.PluginConfig) {
			pc.MemoryLimit = config.Size(16 << 20)
			pc.MaxInstances = 1
			pc.Timeout = config.Duration(30 * time.Second)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	p := set.plugins["guest"]
	pm, err := p.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { p.release(pm) }) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waitCtx := &poolWaitContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := p.acquire(waitCtx)
		done <- err
	}()
	select {
	case <-waitCtx.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("second acquisition did not begin waiting")
	}
	if err := p.close(); !errors.Is(err, errGuestWorkInFlight) {
		t.Errorf("retirement with a busy guest = %v, want in-flight error", err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, errInstanceLimit) {
			t.Errorf("waiting acquisition after retirement = %v, want instance limit", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("retirement did not wake the waiting acquisition")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 {
		t.Errorf("retirement refunded the busy guest: %+v", got)
	}
	releaseOnce.Do(func() { p.release(pm) })
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
		t.Errorf("guest release after retirement lost debt: %+v", got)
	}
	if err := m.Close(); err != nil {
		t.Errorf("manager close after guest release: %v", err)
	}
}

func TestGuestCapacityRemainsChargedWhileClosedCallIsRunning(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	t.Cleanup(func() {
		unblockOnce.Do(func() { close(unblock) })
		_ = rt.Close(ctx)
	})
	_, err := rt.NewHostModuleBuilder(hostModuleV1).NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module) {
			memory, ok := mod.Memory().Read(0, 1)
			if !ok {
				panic("guest memory unavailable")
			}
			close(entered)
			<-unblock
			memory[0] = 1
		}).Export("hold").Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	guest := hostGuest{
		abi:  ABIJulV1,
		fns:  []hostFn{{name: "hold"}},
		body: []byte{0x10, 0x00, 0x41, 0x01},
	}
	compiled, err := rt.CompileModule(ctx, guest.wasm())
	if err != nil {
		t.Fatal(err)
	}
	g := testGovernor(t)
	m, err := NewManager(Options{Inspection: g, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p := &plugin{
		name:                   "active",
		runtime:                rt,
		compiled:               compiled,
		pool:                   make(chan *pooledModule, 1),
		slots:                  make(chan struct{}, 1),
		gov:                    g,
		mgr:                    m,
		generation:             1,
		memoryLimitBytes:       64 << 10,
		guestTickets:           make(map[*inspection.GuestTicket]*guestOwnership),
		timeout:                5 * time.Second,
		maxInstanceInvocations: 100,
		log:                    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	m.track(p)
	callDone := make(chan struct{})
	go func() {
		defer close(callDone)
		_, _, _, _ = p.call(ctx, exportHandleRequest, &invocation{}, nil)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not enter the host call")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- p.close() }()
	select {
	case err := <-closeDone:
		if !errors.Is(err, errGuestWorkInFlight) {
			t.Fatalf("close runtime = %v, want in-flight guest error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runtime close did not return while the host call was blocked")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 {
		t.Errorf("runtime close refunded a running guest: %+v", got)
	}
	if err := m.Close(); !errors.Is(err, errGuestWorkInFlight) {
		t.Errorf("manager close with active guest = %v, want in-flight error", err)
	}
	if stats, err := m.GuestCapacityStats(); err != nil || stats.LiveInstances != 1 {
		t.Errorf("manager lost active owner: stats=%+v err=%v", stats, err)
	}
	unblockOnce.Do(func() { close(unblock) })
	select {
	case <-callDone:
	case <-time.After(5 * time.Second):
		t.Fatal("guest call did not finish")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 {
		t.Errorf("completed guest call lost debt: %+v", got)
	}
	if err := m.Close(); err != nil {
		t.Errorf("manager close after guest finished: %v", err)
	}
}

func TestGuestCapacityRetainsModuleCloseFailureAfterSuccessfulRuntimeClose(t *testing.T) {
	g := testGovernor(t)
	ticket, err := g.ReserveGuestCapacity(1, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("module close failed")
	p := &plugin{
		name:         "failed-module-close",
		runtime:      successfulCloseRuntime{},
		gov:          g,
		generation:   1,
		guestTickets: map[*inspection.GuestTicket]*guestOwnership{ticket: {active: true}},
		slots:        make(chan struct{}, 1),
	}
	p.slots <- struct{}{}
	p.discard(&pooledModule{mod: closeErrorModule{err: closeErr}, ticket: ticket})
	if err := p.close(); !errors.Is(err, closeErr) {
		t.Fatalf("runtime close lost module close error: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 ||
		got.Instances[inspection.GuestFailedRetained] != 1 {
		t.Fatalf("successful runtime close refunded unconfirmed module teardown: %+v", got)
	}
}

func TestGuestStartupDoesNotInheritRetainingAllocator(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	wasm := []byte{
		0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x01,
		0x08, 0x01, 0x00,
		0x0a, 0x05, 0x01, 0x03, 0x00, 0x00, 0x0b,
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	g := testGovernor(t)
	p := &plugin{
		runtime:          rt,
		compiled:         compiled,
		pool:             make(chan *pooledModule, 1),
		slots:            make(chan struct{}, 1),
		gov:              g,
		generation:       1,
		memoryLimitBytes: 64 << 10,
		guestTickets:     make(map[*inspection.GuestTicket]*guestOwnership),
	}
	var used atomic.Bool
	mem := &retainingLinearMemory{}
	parent := experimental.WithMemoryAllocator(ctx, experimental.MemoryAllocatorFunc(func(_, _ uint64) experimental.LinearMemory {
		used.Store(true)
		return mem
	}))
	if _, err := p.acquire(parent); err == nil {
		t.Fatal("trapping guest start unexpectedly succeeded")
	}
	if used.Load() || mem.buffer != nil {
		t.Fatal("guest startup inherited an allocator that could retain guest memory")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || len(p.slots) != 0 {
		t.Fatalf("failed startup debt/slots: %+v, slots=%d", got, len(p.slots))
	}
}

func TestGuestStartupRejectsModuleClosedByInitialize(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	_, err := rt.NewHostModuleBuilder("test").NewFunctionBuilder().
		WithFunc(func(ctx context.Context, mod api.Module) { _ = mod.Close(ctx) }).
		Export("close").Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wasm := assemble([]wasmImport{{module: "test", name: "close"}}, []wasmFunc{
		{export: "_initialize", body: []byte{0x10, 0x00}},
		fnRequest,
	})
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	g := testGovernor(t)
	p := &plugin{
		runtime:          rt,
		compiled:         compiled,
		pool:             make(chan *pooledModule, 1),
		slots:            make(chan struct{}, 1),
		gov:              g,
		generation:       1,
		memoryLimitBytes: 64 << 10,
		guestTickets:     make(map[*inspection.GuestTicket]*guestOwnership),
	}
	if _, err := p.acquire(ctx); err == nil {
		t.Fatal("initialization returned a closed module as usable")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || len(p.slots) != 0 {
		t.Fatalf("closed initialization debt/slots: %+v, slots=%d", got, len(p.slots))
	}
}

func TestGuestCapacityRemainsChargedDuringRetiredStartup(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	entered, unblock := make(chan struct{}), make(chan struct{})
	var unblockOnce sync.Once
	t.Cleanup(func() {
		unblockOnce.Do(func() { close(unblock) })
		_ = rt.Close(ctx)
	})
	_, err := rt.NewHostModuleBuilder("test").NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module) {
			memory, ok := mod.Memory().Read(0, 1)
			if !ok {
				panic("guest memory unavailable")
			}
			close(entered)
			<-unblock
			memory[0] = 1
		}).Export("wait").Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wasm := []byte{
		0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00,
		0x02, 0x0d, 0x01, 0x04, 't', 'e', 's', 't', 0x04, 'w', 'a', 'i', 't', 0x00, 0x00,
		0x03, 0x02, 0x01, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x01,
		0x08, 0x01, 0x01,
		0x0a, 0x06, 0x01, 0x04, 0x00, 0x10, 0x00, 0x0b,
	}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	g := testGovernor(t)
	m, err := NewManager(Options{Inspection: g, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	p := &plugin{
		name:             "starting",
		runtime:          rt,
		compiled:         compiled,
		pool:             make(chan *pooledModule, 1),
		slots:            make(chan struct{}, 1),
		gov:              g,
		mgr:              m,
		generation:       1,
		memoryLimitBytes: 64 << 10,
		guestTickets:     make(map[*inspection.GuestTicket]*guestOwnership),
	}
	m.track(p)
	done := make(chan error, 1)
	go func() {
		_, err := p.acquire(ctx)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not enter startup")
	}
	if err := p.close(); !errors.Is(err, errGuestWorkInFlight) {
		t.Fatalf("retire while starting = %v, want in-flight error", err)
	}
	if err := m.Close(); !errors.Is(err, errGuestWorkInFlight) {
		t.Fatalf("manager close while starting = %v, want in-flight error", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 ||
		got.Instances[inspection.GuestStarting] != 1 {
		t.Fatalf("retirement refunded starting guest: %+v", got)
	}
	unblockOnce.Do(func() { close(unblock) })
	select {
	case err := <-done:
		if !errors.Is(err, errPluginClosed) {
			t.Errorf("retired startup = %v, want plugin closed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("retired guest startup did not finish")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || len(p.slots) != 0 {
		t.Errorf("retired startup debt/slots: %+v, slots=%d", got, len(p.slots))
	}
	if err := m.Close(); err != nil {
		t.Errorf("manager close after startup finished: %v", err)
	}
}

type retainingLinearMemory struct {
	buffer []byte
}

func (m *retainingLinearMemory) Reallocate(size uint64) []byte {
	m.buffer = make([]byte, size)
	return m.buffer
}

func (m *retainingLinearMemory) Free() {}

type successfulCloseRuntime struct{ wazero.Runtime }

func (successfulCloseRuntime) Close(context.Context) error { return nil }

func TestGuestCapacityDoesNotForgetFailedRuntimeCloseOnRetry(t *testing.T) {
	g := testGovernor(t)
	m, err := NewManager(Options{Inspection: g, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := g.ReserveGuestCapacity(1, 16<<20)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := errors.New("runtime close failed")
	p := &plugin{
		name:         "failed-close",
		runtime:      &failedOnceRuntime{err: closeErr},
		gov:          g,
		mgr:          m,
		generation:   1,
		guestTickets: map[*inspection.GuestTicket]*guestOwnership{ticket: {}},
		slots:        make(chan struct{}, 1),
	}
	p.slots <- struct{}{}
	if err := p.close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close = %v, want %v", err, closeErr)
	}
	if err := m.Close(); !errors.Is(err, closeErr) {
		t.Errorf("manager retry hid the original close failure: %v", err)
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 16<<20 ||
		got.Instances[inspection.GuestFailedRetained] != 1 {
		t.Errorf("retry refunded unconfirmed teardown: %+v", got)
	}
}

func TestManagerKeepsRuntimeCloseErrorWithoutGuestTickets(t *testing.T) {
	g := testGovernor(t)
	m, err := NewManager(Options{Inspection: g, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.cache.Close(context.Background()) })
	closeErr := errors.New("runtime close failed")
	p := &plugin{
		name:         "empty-failed-close",
		runtime:      &failedOnceRuntime{err: closeErr},
		mgr:          m,
		guestTickets: make(map[*inspection.GuestTicket]*guestOwnership),
	}
	if err := p.close(); !errors.Is(err, closeErr) {
		t.Fatalf("first close = %v, want %v", err, closeErr)
	}
	if err := m.Close(); !errors.Is(err, closeErr) {
		t.Fatalf("manager retry hid a runtime close failure: %v", err)
	}
}

func TestGuestCapacityRetainsFailedInstantiationWithoutModule(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, newRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(1))
	t.Cleanup(func() { _ = rt.Close(ctx) })
	wasm := []byte{0x00, 'a', 's', 'm', 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, 0x03, 0x02, 0x01, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x01, 0x08, 0x01, 0x00,
		0x0a, 0x05, 0x01, 0x03, 0x00, 0x00, 0x0b}
	compiled, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		t.Fatal(err)
	}
	g := testGovernor(t)
	p := &plugin{
		name: "failed-start", runtime: rt, compiled: compiled,
		pool: make(chan *pooledModule, 1), slots: make(chan struct{}, 1),
		gov: g, generation: 1, memoryLimitBytes: 64 << 10,
		guestTickets: make(map[*inspection.GuestTicket]*guestOwnership),
	}
	p.slots <- struct{}{}
	_, err, retained := p.instantiateGuest(ctx, inspection.GuestBusy)
	if err == nil || retained {
		t.Fatalf("failed start = (%v, slot retained=%t), want error and reusable slot", err, retained)
	}
	got := g.Snapshot().GuestCapacity
	if got.ReservedBytes != 64<<10 || got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || len(p.slots) != 1 {
		t.Errorf("unreturned start module was not retained: capacity=%+v slots=%d", got, len(p.slots))
	}
}

func TestGuestCancellationRetainsDebtAndRefusesReplacement(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntimeWithConfig(ctx, newRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(2))
	t.Cleanup(func() { _ = rt.Close(ctx) })
	entered := make(chan struct{})
	_, err := rt.NewHostModuleBuilder(hostModuleV1).NewFunctionBuilder().
		WithFunc(func(ctx context.Context) {
			close(entered)
			<-ctx.Done()
		}).Export("wait").Instantiate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := rt.CompileModule(ctx, (hostGuest{
		abi: ABIJulV1, fns: []hostFn{{name: "wait"}},
		body: []byte{0x10, 0x00, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x41, 0x00},
	}).wasm())
	if err != nil {
		t.Fatal(err)
	}
	limits := testLimits()
	limits.MaxGuestCapacityBytes = 128 << 10
	g, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	p := &plugin{
		name: "cancelled", runtime: rt, compiled: compiled,
		pool: make(chan *pooledModule, 1), slots: make(chan struct{}, 1),
		gov: g, generation: 1, memoryLimitBytes: 128 << 10,
		guestTickets: make(map[*inspection.GuestTicket]*guestOwnership),
		timeout:      5 * time.Second, log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, _, err := p.call(request, exportHandleRequest, &invocation{}, nil)
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("guest did not enter host call")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled guest call succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("guest call did not stop after cancellation")
	}
	if got := g.Snapshot().GuestCapacity; got.ReservedBytes != 128<<10 ||
		got.Instances[inspection.GuestTeardownUnconfirmed] != 1 || len(p.slots) != 0 {
		t.Fatalf("cancelled call debt/slots = %+v, %d", got, len(p.slots))
	}
	if _, err := p.acquire(ctx); !inspection.IsOutcome(err, inspection.GuestCapacitySaturated) {
		t.Fatalf("replacement after cancellation = %v, want saturation", err)
	}
}

func TestGuestCapacityStatsIncludesCompilingCandidate(t *testing.T) {
	m, _, _, _ := govManager(t, testLimits())
	entered := make(chan struct{})
	resume := make(chan struct{})
	var release sync.Once
	afterModuleRead = func(string) {
		close(entered)
		<-resume
	}
	t.Cleanup(func() {
		afterModuleRead = nil
		release.Do(func() { close(resume) })
	})
	type buildResult struct {
		set *Set
		err error
	}
	built := make(chan buildResult, 1)
	go func() {
		set, err := m.Build(context.Background(), map[string]config.PluginConfig{
			"candidate": pcfg("header-inject", func(pc *config.PluginConfig) {
				pc.MemoryLimit = config.Size(16 << 20)
			}),
		})
		built <- buildResult{set: set, err: err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("candidate did not reach the compilation barrier")
	}
	stats, err := m.GuestCapacityStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.MaxPermittedBytes != 1<<30 || stats.MaxInstances != 64 || stats.LiveInstances != 0 {
		t.Errorf("compiling candidate potential = %+v, want 1 GiB / 64 slots / no live instances", stats)
	}
	release.Do(func() { close(resume) })
	result := <-built
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := result.set.Close(); err != nil {
		t.Fatal(err)
	}
	stats, err = m.GuestCapacityStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.MaxPermittedBytes != 0 || stats.MaxInstances != 0 {
		t.Errorf("candidate potential remained after close: %+v", stats)
	}
}

type closeErrorModule struct {
	api.Module
	err error
}

func (m closeErrorModule) Close(context.Context) error { return m.err }

func (m closeErrorModule) IsClosed() bool { return false }

type closeErrorRuntime struct {
	wazero.Runtime
	err error
}

func (r closeErrorRuntime) Close(context.Context) error { return r.err }

type failedOnceRuntime struct {
	wazero.Runtime
	err    error
	closed bool
}

func (r *failedOnceRuntime) Close(context.Context) error {
	if r.closed {
		return nil
	}
	r.closed = true
	return r.err
}

func onlyGuestTicket(t *testing.T, p *plugin) *inspection.GuestTicket {
	t.Helper()
	p.guestMu.Lock()
	defer p.guestMu.Unlock()
	if len(p.guestTickets) != 1 {
		t.Fatalf("plugin guest tickets = %d, want 1", len(p.guestTickets))
	}
	for ticket := range p.guestTickets {
		return ticket
	}
	return nil
}
