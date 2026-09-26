// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"jul/internal/config"
	"jul/internal/egress"
)

// surfaceSet builds the conformance guest with kv and fetch granted, its
// fetches routed by wrap.
func surfaceSet(t *testing.T, wrap func(DialFunc) DialFunc, opts ...func(*config.PluginConfig)) *Set {
	t.Helper()
	m, _ := v2Manager(t, nil)
	pc := v2cfg(append([]func(*config.PluginConfig){func(pc *config.PluginConfig) {
		pc.KV, pc.Fetch, pc.AllowedHosts = true, true, []string{"fetch.test"}
		pc.MaxFetchResponse = config.Size(4)
	}}, opts...)...)
	s, err := m.BuildWithEgress(context.Background(), map[string]config.PluginConfig{"p": pc}, wrap)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestV2RequestSurface(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "fetched") }))
	defer origin.Close()
	toOrigin := func(DialFunc) DialFunc {
		return func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, origin.Listener.Addr().String())
		}
	}
	blocked := func(DialFunc) DialFunc {
		return func(context.Context, string, string) (net.Conn, error) { return nil, egress.ErrBlocked }
	}
	var seenURI, seenHdr, seenBody string
	action := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenURI, seenHdr = r.URL.RequestURI(), r.Header.Get("X-Added")
		b, _ := io.ReadAll(r.Body)
		seenBody = string(b)
	})
	post := func(hdr ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/p", strings.NewReader("abc"))
		r.Header.Set("X-Op", "req-surface")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		return r
	}
	for _, tc := range []struct {
		name string
		wrap func(DialFunc) DialFunc
		hdr  []string
		want string
	}{
		{"fetch ok and truncated", toOrigin, nil, "POST|/p|false|3|3|true|true|v|true|false|200|4|ok|true"},
		{"egress blocked", blocked, nil, "POST|/p|false|3|3|true|true|v|true|false|0|0|egress|false"},
		{"allow-list blocked", toOrigin, []string{"X-Fetch", "http://other.test/"}, "POST|/p|false|3|3|true|true|v|true|false|0|0|blocked|false"},
		{"transport failure", toOrigin, []string{"X-Fetch", "http://fetch.test:bad/"}, "POST|/p|false|3|3|true|true|v|true|false|0|0|failed|false"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := surfaceSet(t, tc.wrap)
			rec := serve(chainFor(s, action, "p"), post(tc.hdr...))
			if got := rec.Header().Get("X-Surface"); got != tc.want {
				t.Fatalf("surface = %q, want %q", got, tc.want)
			}
			if seenURI != "/rewritten?x=1" || seenHdr != "1" || seenBody != "abc" {
				t.Fatalf("action saw %q %q %q", seenURI, seenHdr, seenBody)
			}
		})
	}
	// Oversized request body fails the invocation, as in v1.
	s := surfaceSet(t, toOrigin, func(pc *config.PluginConfig) { pc.MaxRequestBody = config.Size(2) })
	if rec := serve(chainFor(s, action, "p"), post()); rec.Code != 500 {
		t.Fatalf("oversized body: %d", rec.Code)
	}
}

func TestV2ResponseSurface(t *testing.T) {
	m, _ := v2Manager(t, nil)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(func(pc *config.PluginConfig) { pc.KV = true })})
	rec := serve(chainFor(s, act(200, "x"), "p"), req(http.MethodGet, "/p", "X-Op", "resp-surface"))
	if got := rec.Header().Get("X-Surface"); got != "GET|/p|resp-surface|true|true|rv|true|0|denied" {
		t.Fatalf("response surface = %q", got)
	}
}

// callerModule assembles a guest whose export "call" invokes one host import
// with all-zero arguments and drops its results.
func callerModule(module, name string, params, results []api.ValueType) []byte {
	ty := func(ps, rs []api.ValueType) []byte {
		b := append([]byte{0x60}, leb(len(ps))...)
		b = append(b, ps...)
		b = append(b, leb(len(rs))...)
		return append(b, rs...)
	}
	body := []byte{0x00}
	for range params {
		body = append(body, 0x41, 0x00)
	}
	body = append(body, 0x10, 0x00)
	for range results {
		body = append(body, 0x1a)
	}
	body = append(body, 0x0b)
	out := append([]byte(nil), wasmHeader...)
	out = append(out, wasmSection(1, [][]byte{ty(params, results), ty(nil, nil)})...)
	out = append(out, wasmSection(2, [][]byte{append(append(wasmName(module), wasmName(name)...), 0x00, 0x00)})...)
	out = append(out, wasmSection(3, [][]byte{leb(1)})...)
	out = append(out, wasmSection(7, [][]byte{append(wasmName("call"), 0x00, 0x01)})...)
	return append(out, wasmSection(10, [][]byte{append(leb(len(body)), body...)})...)
}

// TestV2HostCallsOutsideAnInvocation: a host call with no request in its
// context (for example from a guest's module initialization) returns the
// neutral value without failing or touching guest memory.
func TestV2HostCallsOutsideAnInvocation(t *testing.T) {
	ctx := context.Background()
	r := wazero.NewRuntime(ctx)
	defer r.Close(ctx)
	p := &plugin{name: "bare", kvUsage: newKVLedger(), capKV: true, capFetch: true, configJSON: []byte("{}")}
	if err := registerJulV2HostModule(ctx, r, p); err != nil {
		t.Fatal(err)
	}
	for name, def := range r.Module(hostModuleV2).ExportedFunctionDefinitions() {
		mod, err := r.Instantiate(ctx, callerModule(hostModuleV2, name, def.ParamTypes(), def.ResultTypes()))
		if err != nil {
			t.Fatalf("%s: instantiate caller: %v", name, err)
		}
		if _, err := mod.ExportedFunction("call").Call(ctx); err != nil {
			t.Errorf("%s outside an invocation failed: %v", name, err)
		}
		_ = mod.Close(ctx)
	}
}

// TestV2ContractViolationsFailInvocation: an out-of-bounds pointer in any host
// call, or a request-mutating call from handle_response, fails the invocation.
func TestV2ContractViolationsFailInvocation(t *testing.T) {
	s := surfaceSet(t, nil)
	h := chainFor(s, act(200, "upstream"), "p")
	for _, fn := range []string{"log", "get_method", "set_uri", "get_request_header", "set_request_header",
		"set_response_header", "read_request_body", "write_response_body", "get_config", "kv_get", "kv_set",
		"fetch", "set_request_state"} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("b", 300)))
		r.Header.Set("X-Op", "req-oob:"+fn)
		if rec := serve(h, r); rec.Code != 500 {
			t.Errorf("request oob %s: %d", fn, rec.Code)
		}
	}
	for _, fn := range []string{"resp_get_header", "resp_set_header", "resp_del_header", "resp_body_read", "resp_body_replace", "get_request_state", "get_config"} {
		if rec := serve(h, req(http.MethodGet, "/", "X-Op", "oob:"+fn, "X-Mode", "body", "X-State", strings.Repeat("s", 100))); rec.Code != 500 || rec.Body.String() != "plugin error\n" {
			t.Errorf("response oob %s: %d %q", fn, rec.Code, rec.Body.String())
		}
	}
	for _, fn := range []string{"set_uri", "set_request_header", "read_request_body", "write_response_body", "set_response_status", "set_response_header"} {
		if rec := serve(h, req(http.MethodGet, "/", "X-Op", "wrong:"+fn)); rec.Code != 500 {
			t.Errorf("wrong phase %s: %d", fn, rec.Code)
		}
	}
}
