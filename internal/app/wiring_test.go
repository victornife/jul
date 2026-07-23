// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/server"
)

func TestRateKeyKind(t *testing.T) {
	cases := map[string]string{
		"header:X-Api-Key": "header",
		"jwt:sub":          "jwt",
		"ip":               "ip",
		"":                 "ip",
		"something else":   "ip",
	}
	for spec, want := range cases {
		if got := RateKeyKind(spec); got != want {
			t.Errorf("RateKeyKind(%q) = %q, want %q", spec, got, want)
		}
	}
}

func TestAuthAndWAFScopeAreStableAndDistinctPerLocation(t *testing.T) {
	srv := config.ServerConfig{Listen: ":8080", ServerNames: []string{"a.example", "b.example"}}
	loc1 := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/"}}
	loc2 := config.LocationConfig{Match: config.MatchConfig{Type: "prefix", Path: "/api"}}

	// Stable: same inputs -> same key. Auth and WAF share the scope shape.
	k1 := AuthScope(srv, loc1)
	if k1 != AuthScope(srv, loc1) {
		t.Error("AuthScope not stable for identical inputs")
	}
	if k1 != WAFScope(srv, loc1) {
		t.Error("AuthScope and WAFScope should produce the same scope for a location")
	}
	// Distinct: different location paths -> different keys.
	if k1 == AuthScope(srv, loc2) {
		t.Error("AuthScope must differ for different location paths")
	}
}

func TestEffectiveWAF(t *testing.T) {
	global := config.WAFConfig{Enabled: true}
	c := &config.Config{WAF: global}

	// No override -> the global policy applies.
	loc := config.LocationConfig{}
	if got, ok := EffectiveWAF(c, loc); !ok || got.Enabled != true {
		t.Errorf("EffectiveWAF(no override) = (%+v, %v), want global enabled", got, ok)
	}

	// A disabled override wins over an enabled global.
	off := config.WAFConfig{Enabled: false}
	loc.WAF = &off
	if got, ok := EffectiveWAF(c, loc); ok || got.Enabled {
		t.Errorf("EffectiveWAF(disabled override) = (%+v, %v), want disabled", got, ok)
	}
}

func TestUniqueListenAddrs(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":8080"},
		{Listen: ":8443"},
		{Listen: ":8080"}, // duplicate
		{Listen: ""},      // skipped
	}
	got := UniqueListenAddrs(servers)
	want := []string{":8080", ":8443"}
	if len(got) != len(want) {
		t.Fatalf("UniqueListenAddrs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("UniqueListenAddrs[%d] = %q, want %q (order should be first-seen)", i, got[i], want[i])
		}
	}
}

func TestAddrServesTLS(t *testing.T) {
	servers := []config.ServerConfig{
		{Listen: ":80"},
		{Listen: ":443", TLS: &config.TLSConfig{Enabled: true}},
		{Listen: ":8443", TLS: &config.TLSConfig{Enabled: false}},
	}
	if AddrServesTLS(servers, ":443") != true {
		t.Error("AddrServesTLS(:443) = false, want true")
	}
	if AddrServesTLS(servers, ":80") != false {
		t.Error("AddrServesTLS(:80) = true, want false")
	}
	if AddrServesTLS(servers, ":8443") != false {
		t.Error("AddrServesTLS(:8443 disabled) = true, want false")
	}
}

func TestIndexUpstreams(t *testing.T) {
	ups := []config.UpstreamConfig{
		{Name: "a", Servers: []config.UpstreamServer{{Address: "10.0.0.1:80"}}},
		{Name: "b"},
	}
	idx := IndexUpstreams(ups)
	if len(idx) != 2 {
		t.Fatalf("IndexUpstreams len = %d, want 2", len(idx))
	}
	if got, ok := idx["a"]; !ok || len(got.Servers) != 1 {
		t.Errorf("IndexUpstreams[a] = (%+v, %v), want the 'a' upstream", got, ok)
	}
	if _, ok := idx["missing"]; ok {
		t.Error("IndexUpstreams should not contain an unknown name")
	}
}

func TestMergeReloadFansInAndSkipsNil(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	a := make(chan struct{}, 1)
	admin := make(chan server.ReloadRequest, 1)
	// fileWatch is nil; admin is non-nil.
	var lastAdminDigest atomic.Pointer[[32]byte]
	out := MergeReload(ctx, a, nil, admin, &lastAdminDigest)

	recv := func() (server.ReloadRequest, bool) {
		select {
		case r := <-out:
			return r, true
		case <-time.After(time.Second):
			return server.ReloadRequest{}, false
		}
	}

	a <- struct{}{}
	if r, ok := recv(); !ok || r.Source != server.ReloadSourceSIGHUP {
		t.Error("signal on source a did not propagate to merged channel")
	}

	// Admin channel carries typed ReloadRequest, not bare struct{}. Candidate-
	// bearing events must remain intact so the server applies the exact
	// preflight-resolved candidate (R9-02).
	want := server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: &config.Candidate{}}
	admin <- want
	if r, ok := recv(); !ok || r.Source != server.ReloadSourceAdmin || r.Candidate == nil {
		t.Errorf("admin candidate event was corrupted or dropped: got source=%v candidate=%v", r.Source, r.Candidate)
	}

	// nil admin channel should also be accepted without panic.
	_ = MergeReload(ctx, a, nil, nil, &lastAdminDigest)
}

func TestMergeReloadNeverDropsPreparedAdminRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan struct{}, 8)
	watch := make(chan [32]byte, 8)
	admin := make(chan server.ReloadRequest, 1)
	var lastAdminDigest atomic.Pointer[[32]byte]
	out := MergeReload(ctx, sig, watch, admin, &lastAdminDigest)
	for i := 0; i < 8; i++ {
		sig <- struct{}{}
		watch <- [32]byte{byte(i + 1)}
	}
	prepared := server.NewPreparedCommit(nil, func() { t.Error("prepared request was aborted") })
	admin <- server.ReloadRequest{ID: "managed", Source: server.ReloadSourceAdmin, Candidate: &config.Candidate{}, PreparedAdmin: prepared}
	deadline := time.After(time.Second)
	for {
		select {
		case req := <-out:
			if req.ID == "managed" {
				if req.PreparedAdmin != prepared {
					t.Fatal("prepared artifact was not preserved")
				}
				return
			}
		case <-deadline:
			t.Fatal("prepared admin request was dropped behind untyped reloads")
		}
	}
}

// TestMergeReloadSuppressesWatcherEcho (R10-01) verifies that a file-watch
// event whose digest matches the last admin-written digest is dropped, while
// a file-watch event with a different digest is forwarded as an external
// write.
func TestMergeReloadSuppressesWatcherEcho(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sig := make(chan struct{})
	fileWatch := make(chan [32]byte, 1)
	admin := make(chan server.ReloadRequest, 1)
	var lastAdminDigest atomic.Pointer[[32]byte]
	out := MergeReload(ctx, sig, fileWatch, admin, &lastAdminDigest)

	recv := func(timeout time.Duration) (server.ReloadRequest, bool) {
		select {
		case r := <-out:
			return r, true
		case <-time.After(timeout):
			return server.ReloadRequest{}, false
		}
	}

	adminDigest := [32]byte{1, 2, 3}
	lastAdminDigest.Store(&adminDigest)
	admin <- server.ReloadRequest{Source: server.ReloadSourceAdmin}

	r, ok := recv(time.Second)
	if !ok || r.Source != server.ReloadSourceAdmin {
		t.Fatalf("admin event not forwarded: got %+v ok=%v", r, ok)
	}

	// Watcher reports the same digest -> echo, must be suppressed.
	fileWatch <- adminDigest
	if r, ok := recv(200 * time.Millisecond); ok {
		t.Fatalf("watcher echo was not suppressed: %+v", r)
	}

	// Watcher reports a different digest -> external write, must forward.
	externalDigest := [32]byte{4, 5, 6}
	fileWatch <- externalDigest
	r, ok = recv(time.Second)
	if !ok || r.Source != server.ReloadSourceFileWatch {
		t.Fatalf("external file watch not forwarded: got %+v ok=%v", r, ok)
	}
}

// TestMergeReloadConsumesAdminDigest (R11-01) verifies that the digest used
// to suppress a watcher echo is consumed. An admin A -> admin B -> external A
// sequence must still reload, because the digest from the original admin A
// write was cleared after its echo was suppressed.
func TestMergeReloadConsumesAdminDigest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fileWatch := make(chan [32]byte, 1)
	admin := make(chan server.ReloadRequest, 1)
	var lastAdminDigest atomic.Pointer[[32]byte]
	out := MergeReload(ctx, nil, fileWatch, admin, &lastAdminDigest)

	recv := func(timeout time.Duration) (server.ReloadRequest, bool) {
		select {
		case r := <-out:
			return r, true
		case <-time.After(timeout):
			return server.ReloadRequest{}, false
		}
	}

	digestA := [32]byte{1, 2, 3}
	digestB := [32]byte{4, 5, 6}

	// Admin writes A; watcher echoes A and must be suppressed.
	lastAdminDigest.Store(&digestA)
	admin <- server.ReloadRequest{Source: server.ReloadSourceAdmin}
	if r, ok := recv(time.Second); !ok || r.Source != server.ReloadSourceAdmin {
		t.Fatalf("admin event A not forwarded: got %+v ok=%v", r, ok)
	}
	fileWatch <- digestA
	if r, ok := recv(200 * time.Millisecond); ok {
		t.Fatalf("watcher echo of A was not suppressed: %+v", r)
	}

	// Admin writes B; watcher echoes B and must be suppressed.
	lastAdminDigest.Store(&digestB)
	admin <- server.ReloadRequest{Source: server.ReloadSourceAdmin}
	if r, ok := recv(time.Second); !ok || r.Source != server.ReloadSourceAdmin {
		t.Fatalf("admin event B not forwarded: got %+v ok=%v", r, ok)
	}
	fileWatch <- digestB
	if r, ok := recv(200 * time.Millisecond); ok {
		t.Fatalf("watcher echo of B was not suppressed: %+v", r)
	}

	// A legitimate external write restores A. The old digestA was consumed,
	// so this must be forwarded.
	fileWatch <- digestA
	r, ok := recv(time.Second)
	if !ok || r.Source != server.ReloadSourceFileWatch {
		t.Fatalf("external write restoring A not forwarded: got %+v ok=%v", r, ok)
	}
}

func TestValidateRuntimeConfig(t *testing.T) {
	// A well-formed proxy config passes the runtime preflight.
	good := config.ProxyTarget("127.0.0.1:9000", ":8080")
	if err := ValidateRuntimeConfig(good); err != nil {
		t.Fatalf("ValidateRuntimeConfig(valid) = %v, want nil", err)
	}

	// A structurally invalid config (a location with no match) is rejected.
	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}},
		}},
	}
	if err := ValidateRuntimeConfig(bad); err == nil {
		t.Error("ValidateRuntimeConfig(invalid) = nil, want an error")
	}
}
