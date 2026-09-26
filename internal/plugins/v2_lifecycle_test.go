// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycletest"
)

// TestV2ReloadUnderLoadAndChurnQuiesces drives the #428 lifecycle invariants
// through the response phase: generations are built and retired while
// response-phase traffic (body transforms included) flows on a live one, with
// instances retiring every few calls; afterwards goroutines, FDs and heap return
// to their baseline.
func TestV2ReloadUnderLoadAndChurnQuiesces(t *testing.T) {
	m, hooks := v2Manager(t, nil)
	cfg := map[string]config.PluginConfig{"p": v2cfg(withOp("upper"), func(pc *config.PluginConfig) { pc.MaxInvocations = 4 })}
	live := buildSet(t, m, cfg)
	h := chainFor(live, act(200, "abc"), "p")

	// Warm up so the baseline includes the live generation's steady state.
	for i := 0; i < 8; i++ {
		serve(h, req(http.MethodGet, "/", "X-Mode", "body"))
	}
	base := lifecycletest.Take()
	var heapBase runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&heapBase)

	workers, reloads := 8, 10
	if runtime.NumCPU() <= 4 {
		workers, reloads = 3, 4
	}
	var wg sync.WaitGroup
	var stop atomic.Bool
	var bad atomic.Int64
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if rec := serve(h, req(http.MethodGet, "/", "X-Mode", "body")); rec.Code != 200 || rec.Body.String() != "ABC" {
					bad.Add(1)
				}
			}
		}()
	}
	for r := 0; r < reloads; r++ {
		gen, err := m.Build(t.Context(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		if rec := serve(chainFor(gen, act(200, "xyz"), "p"), req(http.MethodGet, "/", "X-Mode", "body")); rec.Body.String() != "XYZ" {
			t.Fatalf("reload %d: %q", r, rec.Body.String())
		}
		_ = gen.Close()
	}
	stop.Store(true)
	wg.Wait()
	if bad.Load() > 0 {
		t.Fatalf("%d live responses failed during churn (errors=%d)", bad.Load(), hooks.count(hooks.results, "error"))
	}
	lifecycletest.AwaitQuiescence(t, base, 4)

	var heapNow runtime.MemStats
	debug.FreeOSMemory()
	runtime.ReadMemStats(&heapNow)
	if grown := int64(heapNow.HeapInuse) - int64(heapBase.HeapInuse); grown > 64<<20 {
		t.Fatalf("heap in use grew by %d MiB across churn", grown>>20)
	}
}

// TestInstanceAcquisitionFailureIsContained: when no instance can be created
// (here the runtime is gone and the pool is empty) both phases fail closed and
// count a contained failure instead of crashing.
func TestInstanceAcquisitionFailureIsContained(t *testing.T) {
	m, hooks := v2Manager(t, nil)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg()})
	p := s.plugins["p"]
	pm := <-p.pool
	_ = pm.mod.Close(t.Context())
	_ = p.runtime.Close(t.Context())
	if _, _, err := p.invoke(t.Context(), httptest.NewRecorder(), req(http.MethodGet, "/")); err == nil {
		t.Fatal("request phase ran without an instance")
	}
	view := &responseView{status: 200, header: http.Header{}, bodyState: bodyNotRequested}
	if _, err := p.invokeResponse(t.Context(), req(http.MethodGet, "/"), view, nil); err == nil {
		t.Fatal("response phase ran without an instance")
	}
	if hooks.panics != 2 || hooks.count(hooks.results, "error") != 1 {
		t.Fatalf("panics=%d results=%+v", hooks.panics, hooks.results)
	}
	if (*Set)(nil).ABIs() != nil {
		t.Fatal("nil set ABIs")
	}
}

// TestV1GuestWithoutResultIsContained: a v1 handle_request that returns no
// value panics the host call path; the panic is contained as a 500.
func TestV1GuestWithoutResultIsContained(t *testing.T) {
	m, hooks := v2Manager(t, nil)
	mod := assemble(nil, []wasmFunc{fnReqNoRes})
	s := buildSet(t, m, map[string]config.PluginConfig{"p": {Inline: base64.StdEncoding.EncodeToString(mod)}})
	next, called := okNext()
	rec := serve(s.Middleware("p")(next), req(http.MethodGet, "/"))
	if rec.Code != 500 || *called || hooks.panics != 1 {
		t.Fatalf("code=%d called=%v panics=%d", rec.Code, *called, hooks.panics)
	}
}
