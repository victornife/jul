// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"jul/internal/config"
	"jul/internal/lifecycle"
	"jul/internal/redact"
	"jul/internal/server"
	"jul/internal/upstream"
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
	ctx := context.Background()
	// A well-formed proxy config passes the runtime preflight.
	good := config.ProxyTarget("127.0.0.1:9000", ":8080")
	if err := ValidateRuntimeConfig(ctx, good); err != nil {
		t.Fatalf("ValidateRuntimeConfig(valid) = %v, want nil", err)
	}

	// A structurally invalid config (a location with no match) is rejected.
	bad := &config.Config{
		Servers: []config.ServerConfig{{
			Listen:    ":8080",
			Locations: []config.LocationConfig{{Return: 200}},
		}},
	}
	if err := ValidateRuntimeConfig(ctx, bad); err == nil {
		t.Error("ValidateRuntimeConfig(invalid) = nil, want an error")
	}
}

func TestListenerClientAuthHelpers(t *testing.T) {
	srvs := []config.ServerConfig{
		{
			Listen: "127.0.0.1:8443",
			TLS: &config.TLSConfig{
				Enabled: true,
				ClientAuth: &config.ClientAuthConfig{
					Mode:               "require",
					CAFile:             "ca.pem",
					ForwardCertificate: "chain",
				},
			},
		},
		{
			Listen: "127.0.0.1:8443",
		},
	}
	if !listenerClientAuthRequired(srvs, "127.0.0.1:8443") {
		t.Fatal("expected listenerClientAuthRequired=true on shared TLS listener")
	}
	if listenerClientAuthRequired(srvs, "127.0.0.1:8080") {
		t.Fatal("expected listenerClientAuthRequired=false on plain listener")
	}
	if got := listenerClientCertForward(srvs, "127.0.0.1:8443", srvs[1]); got != "chain" {
		t.Fatalf("listenerClientCertForward from sibling = %q, want chain", got)
	}
	curLeaf := config.ServerConfig{
		Listen: "127.0.0.1:8443",
		TLS:    &config.TLSConfig{Enabled: true, ClientAuth: &config.ClientAuthConfig{ForwardCertificate: "leaf"}},
	}
	if got := listenerClientCertForward(srvs, "127.0.0.1:8443", curLeaf); got != "leaf" {
		t.Fatalf("listenerClientCertForward from cur = %q, want leaf", got)
	}
	if got := listenerClientCertForward(srvs, "127.0.0.1:8080", config.ServerConfig{}); got != "none" {
		t.Fatalf("listenerClientCertForward none = %q, want none", got)
	}
}

func TestThird653LoadedHtpasswdMustGovernRetirement(t *testing.T) {
	t.Run("stable_file_control", func(t *testing.T) { third653LoadedHtpasswd(t, false) })
	t.Run("file_replaced_after_prepare", func(t *testing.T) { third653LoadedHtpasswd(t, true) })
}

func third653LoadedHtpasswd(t *testing.T, replaceFile bool) {
	backendCanceled := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "1" {
			w.WriteHeader(200)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: start\n\n")
		_ = http.NewResponseController(w).Flush()
		<-r.Context().Done()
		close(backendCanceled)
	}))
	defer backend.Close()
	f, cleanup := minimalFactory(t)
	defer cleanup()
	hash, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "users.htpasswd")
	both := []byte(fmt.Sprintf("alice:%s\nbob:%s\n", hash, hash))
	onlyAlice := []byte(fmt.Sprintf("alice:%s\n", hash))
	if err = os.WriteFile(path, both, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.ProxyTarget(backend.URL, freePort(t))
	rid := "private"
	cfg.Servers[0].Locations[0].RouteID = &rid
	cfg.Servers[0].Locations[0].Auth = &config.AuthConfig{Basic: &config.BasicAuthConfig{File: path}}
	initial, err := config.NewCandidate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var replaceAfterPrepare atomic.Bool
	factory := func(ctx context.Context, c *config.Config) (map[string]http.Handler, uint64, func() (upstream.SnapshotMap, func()), func(), error) {
		handlers, gen, commit, abort, err := f.Prepare(ctx, c)
		if err == nil && replaceAfterPrepare.Swap(false) {
			if writeErr := os.WriteFile(path, both, 0o600); writeErr != nil {
				abort()
				return nil, 0, nil, nil, writeErr
			}
		}
		return handlers, gen, commit, abort, err
	}
	srv := server.New(initial.Effective, initial.Raw, lifecycle.ComputeFingerprint(initial.Effective), f.Log, factory, config.NewTOMLSource(""), func(context.Context, *config.Config) error { return nil })
	ready := make(chan struct{})
	srv.OnInitialGenerationReady = func() { close(ready) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	reload := make(chan server.ReloadRequest, 1)
	go func() { done <- srv.Run(ctx, reload, redact.EmptyState()) }()
	defer func() { cancel(); <-done }()
	<-ready

	client := &http.Client{Timeout: 5 * time.Second}
	request := func(query string) (*http.Response, error) {
		r, _ := http.NewRequest("GET", "http://"+cfg.Servers[0].Listen+"/"+query, nil)
		r.SetBasicAuth("bob", "pw")
		return client.Do(r)
	}
	oldStream, err := request("?stream=1")
	if err != nil {
		t.Fatal(err)
	}
	defer oldStream.Body.Close()
	if oldStream.StatusCode != 200 {
		t.Fatalf("before=%d", oldStream.StatusCode)
	}
	if err = os.WriteFile(path, onlyAlice, 0o600); err != nil {
		t.Fatal(err)
	}
	next, _ := cfg.Clone()
	next.Global.LogLevel = "debug"
	cand, err := config.NewCandidate(next)
	if err != nil {
		t.Fatal(err)
	}
	replaceAfterPrepare.Store(replaceFile)
	result := make(chan server.ReloadResult, 1)
	// Also pass KeepOpenEntries: ["private"] to prove loaded htpasswd user removal
	// (narrowed) overrides keep_open_entries even when the disk file is replaced after Prepare!
	reload <- server.ReloadRequest{Source: server.ReloadSourceAdmin, Candidate: cand, KeepOpenEntries: []string{"private"}, Result: result}
	rel := <-result
	if rel.Outcome != server.ReloadAppliedLive {
		t.Fatalf("reload=%+v", rel)
	}
	after, err := request("")
	if err != nil {
		t.Fatal(err)
	}
	_ = after.Body.Close()
	if after.StatusCode != 401 {
		t.Fatalf("fixture: newly loaded authenticator must deny removed bob, got %d", after.StatusCode)
	}
	select {
	case <-backendCanceled:
	case <-time.After(2200 * time.Millisecond):
		t.Fatalf("loaded htpasswd removed bob, but his old stream survived the 2s bound; disk recapture hid the loaded change; retirements=%+v", rel.EntryRetirements)
	}
	if len(rel.EntryRetirements) != 1 || rel.EntryRetirements[0].EndedExchanges != 1 || rel.EntryRetirements[0].KeepOpen || !rel.EntryRetirements[0].CompletedWithinBound {
		t.Fatalf("expected 1 retired exchange with keep_open overridden, got %+v", rel.EntryRetirements)
	}
}
