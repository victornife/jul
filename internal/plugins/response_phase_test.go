// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
)

// respHooks records the response-phase metric hooks.
type respHooks struct {
	mu      sync.Mutex
	results map[string]int
	noBody  map[string]int
	panics  int
	reqs    map[string]int
}

func (h *respHooks) count(m map[string]int, k string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return m[k]
}

func v2Manager(t *testing.T, kv KVStore) (*Manager, *respHooks) {
	t.Helper()
	h := &respHooks{results: map[string]int{}, noBody: map[string]int{}, reqs: map[string]int{}}
	m, err := NewManager(Options{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		KV:     kv,
		OnInvocation: func(_, result string, _ time.Duration) {
			h.mu.Lock()
			h.reqs[result]++
			h.mu.Unlock()
		},
		OnPanic: func(string) {
			h.mu.Lock()
			h.panics++
			h.mu.Unlock()
		},
		OnResponseInvocation: func(_, result string, _ time.Duration) {
			h.mu.Lock()
			h.results[result]++
			h.mu.Unlock()
		},
		OnResponseBodyUnavailable: func(_, reason string) {
			h.mu.Lock()
			h.noBody[reason]++
			h.mu.Unlock()
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, h
}

func v2cfg(opts ...func(*config.PluginConfig)) config.PluginConfig {
	pc := pcfg("testguest-v2", opts...)
	pc.Path = fixturePath("testguest-v2")
	pc.ABI = ABIJulV2
	return pc
}

func withOp(op string) func(*config.PluginConfig) {
	return func(pc *config.PluginConfig) { pc.Config = map[string]string{"op": op} }
}

// chainFor composes the plugins the way the handler factory does: request
// hooks outermost (first name outermost), the response point directly around
// the action.
func chainFor(s *Set, action http.Handler, names ...string) http.Handler {
	h := action
	if rp := s.ResponsePoint(names...); rp != nil {
		h = rp(h)
	}
	for i := len(names) - 1; i >= 0; i-- {
		h = s.Middleware(names[i])(h)
	}
	return h
}

// act is an action writing status, headers (name/value pairs) and body.
func act(status int, body string, hdr ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(hdr); i += 2 {
			w.Header().Add(hdr[i], hdr[i+1])
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	})
}

func req(method, target string, hdr ...string) *http.Request {
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Add(hdr[i], hdr[i+1])
	}
	return r
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func singleV2(t *testing.T, opts ...func(*config.PluginConfig)) (*Set, *respHooks) {
	t.Helper()
	m, h := v2Manager(t, nil)
	return buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(opts...)}), h
}

func TestResponsePhaseEchoMetadata(t *testing.T) {
	s, hooks := singleV2(t)
	h := chainFor(s, act(201, "hello", "Server", "origin", "X-Dup-In", "a", "X-Dup-In", "b"), "p")
	rec := serve(h, req(http.MethodGet, "/p?q=1", "X-Op", "echo", "X-State", "s1"))
	want := map[string]string{
		"X-Seen-Status": "201", "X-Body-State": "1", "X-State": "s1", "X-Method": "GET",
		"X-URI": "/p?q=1", "X-Dup": "a|b", "X-Body-RC": "-4", "X-Sub-RC": "0",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if names := rec.Header().Get("X-Names"); names != "Server,X-Body-State,X-Dup-In,X-Method,X-Seen-Status,X-State,X-Sub-Rc,X-Uri" {
		t.Errorf("X-Names = %q", names)
	}
	if rec.Code != 201 || rec.Body.String() != "hello" {
		t.Fatalf("response = %d %q", rec.Code, rec.Body.String())
	}
	if hooks.count(hooks.results, "continue") != 1 || hooks.count(hooks.noBody, "none") != 0 {
		t.Fatalf("hooks = %+v %+v", hooks.results, hooks.noBody)
	}
}

func TestResponsePhaseBodyReadAndReplace(t *testing.T) {
	s, _ := singleV2(t)
	origin := act(200, "hello", "Etag", `"abc"`, "Accept-Ranges", "bytes", "Last-Modified", "Mon, 01 Jan 2026 00:00:00 GMT", "Content-Length", "5")

	rec := serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
	if rec.Header().Get("X-Body-State") != "0" || rec.Header().Get("X-Body-Len") != "5" || rec.Body.String() != "hello" {
		t.Fatalf("echo body: %v %q", rec.Header(), rec.Body.String())
	}
	if rec.Header().Get("Etag") == "" || rec.Header().Get("Accept-Ranges") != "" {
		t.Fatalf("unreplaced body: ETag must stay, Accept-Ranges must go: %v", rec.Header())
	}

	rec = serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "upper", "X-Mode", "body"))
	if rec.Body.String() != "HELLO" || rec.Header().Get("X-Replace-RC") != "0" || rec.Header().Get("Content-Length") != "5" {
		t.Fatalf("upper: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	if rec.Header().Get("Etag") != "" || rec.Header().Get("Last-Modified") == "" {
		t.Fatalf("replaced body validators: %v", rec.Header())
	}

	rec = serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "empty", "X-Mode", "body"))
	if rec.Body.Len() != 0 || rec.Header().Get("Content-Length") != "0" || rec.Header().Get("X-Replace-RC") != "0" {
		t.Fatalf("empty: %q %v", rec.Body.String(), rec.Header())
	}

	rec = serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "big", "X-Mode", "body", "X-Size", strconv.Itoa(9<<20)))
	if rec.Header().Get("X-Replace-RC") != "-5" || rec.Body.String() != "hello" {
		t.Fatalf("oversized replacement: %v %q", rec.Header(), rec.Body.String())
	}

	// A handler that writes nothing is an empty 200, presented as available.
	rec = serve(chainFor(s, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}), "p"), req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
	if rec.Code != 200 || rec.Header().Get("X-Body-Len") != "0" {
		t.Fatalf("empty handler: %d %v", rec.Code, rec.Header())
	}
}

func TestResponsePhaseBodyUnavailableReasons(t *testing.T) {
	s, hooks := singleV2(t, func(pc *config.PluginConfig) { pc.MaxResponseBody = config.Size(16) })
	long := strings.Repeat("x", 40)
	cases := []struct {
		name, method string
		action       http.Handler
		state        string
		label        string
		body         string
	}{
		{"head", http.MethodHead, act(200, ""), "2", "none", ""},
		{"no content", http.MethodGet, act(204, ""), "2", "none", ""},
		{"not modified", http.MethodGet, act(304, ""), "2", "none", ""},
		{"partial", http.MethodGet, act(206, "abc", "Content-Range", "bytes 0-2/10"), "6", "partial", "abc"},
		{"encoded", http.MethodGet, act(200, "\x1f\x8b", "Content-Encoding", "gzip"), "5", "encoded", "\x1f\x8b"},
		{"sse", http.MethodGet, act(200, "data: 1\n\n", "Content-Type", "text/event-stream"), "4", "streaming", "data: 1\n\n"},
		{"grpc", http.MethodGet, act(200, "\x00", "Content-Type", "application/grpc+proto"), "4", "streaming", "\x00"},
		{"mjpeg", http.MethodGet, act(200, "--f", "Content-Type", "multipart/x-mixed-replace; boundary=f"), "4", "streaming", "--f"},
		{"accel", http.MethodGet, act(200, "s", "X-Accel-Buffering", "no"), "4", "streaming", "s"},
		{"trailer", http.MethodGet, act(200, "t", "Trailer", "X-Checksum"), "4", "streaming", "t"},
		{"declared too large", http.MethodGet, act(200, long, "Content-Length", "40"), "3", "too_large", long},
		{"buffered too large", http.MethodGet, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, long[:10])
			_, _ = io.WriteString(w, long[10:])
		}), "3", "too_large", long},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := hooks.count(hooks.noBody, tc.label)
			rec := serve(chainFor(s, tc.action, "p"), req(tc.method, "/", "X-Op", "echo", "X-Mode", "body"))
			if got := rec.Header().Get("X-Body-State"); got != tc.state {
				t.Fatalf("body state = %q, want %s (%v)", got, tc.state, rec.Header())
			}
			if rec.Header().Get("X-Body-RC") != "-4" || rec.Body.String() != tc.body {
				t.Fatalf("unavailable body must pass through untouched: %v %q", rec.Header(), rec.Body.String())
			}
			if hooks.count(hooks.noBody, tc.label) != before+1 {
				t.Fatalf("no-body metric %q not counted: %+v", tc.label, hooks.noBody)
			}
		})
	}
	// A metadata subscription is never counted as an unavailable body.
	before := len(hooks.noBody)
	serve(chainFor(s, act(200, "x"), "p"), req(http.MethodGet, "/", "X-Op", "echo"))
	if len(hooks.noBody) != before {
		t.Fatal("metadata subscription counted as body unavailable")
	}
}

// flushRecorder counts flushes reaching the underlying writer.
type flushRecorder struct {
	*httptest.ResponseRecorder
	flushes int
}

func (f *flushRecorder) Flush() { f.flushes++; f.ResponseRecorder.Flush() }

func TestResponsePhaseFlushSemantics(t *testing.T) {
	s, _ := singleV2(t)
	flushing := func(ct string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if ct != "" {
				w.Header().Set("Content-Type", ct)
			}
			_, _ = io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			_, _ = io.WriteString(w, "b")
		})
	}
	// A body subscription absorbs flushes of an ordinary (undeclared) stream.
	rec := &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	chainFor(s, flushing(""), "p").ServeHTTP(rec, req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
	if rec.flushes != 0 || rec.Header().Get("X-Body-Len") != "2" || rec.Body.String() != "ab" {
		t.Fatalf("absorbed flush: flushes=%d %v %q", rec.flushes, rec.Header(), rec.Body.String())
	}
	// A declared stream is never buffered: the flush reaches the client.
	rec = &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	chainFor(s, flushing("text/event-stream"), "p").ServeHTTP(rec, req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
	if rec.flushes != 1 || rec.Header().Get("X-Body-State") != "4" || rec.Body.String() != "ab" {
		t.Fatalf("declared stream: flushes=%d %v", rec.flushes, rec.Header())
	}
	// A metadata subscription never delays a flush.
	rec = &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	chainFor(s, flushing(""), "p").ServeHTTP(rec, req(http.MethodGet, "/", "X-Op", "echo"))
	if rec.flushes != 1 || rec.Body.String() != "ab" {
		t.Fatalf("metadata flush: flushes=%d", rec.flushes)
	}
	// A flush before any write commits the status like net/http does.
	rec = &flushRecorder{ResponseRecorder: httptest.NewRecorder()}
	chainFor(s, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.(http.Flusher).Flush() }), "p").
		ServeHTTP(rec, req(http.MethodGet, "/", "X-Op", "echo"))
	if rec.flushes != 1 || rec.Header().Get("X-Seen-Status") != "200" {
		t.Fatalf("early flush: flushes=%d %v", rec.flushes, rec.Header())
	}
}

func TestResponsePhaseRequestsIdentityRepresentation(t *testing.T) {
	s, _ := singleV2(t)
	var seen http.Header
	origin := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		_, _ = io.WriteString(w, "ok")
	})
	r := req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body", "Range", "bytes=0-1", "If-Range", `"e"`, "Accept-Encoding", "gzip")
	serve(chainFor(s, origin, "p"), r)
	for _, h := range identityRequestHeaders {
		if seen.Get(h) != "" {
			t.Errorf("body subscription forwarded %s", h)
		}
		if r.Header.Get(h) == "" {
			t.Errorf("outer layers lost the client's %s", h)
		}
	}
	serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "echo", "Accept-Encoding", "gzip"))
	if seen.Get("Accept-Encoding") != "gzip" {
		t.Error("metadata subscription must not change the request")
	}
}

func TestResponsePhaseStatusMutation(t *testing.T) {
	s, _ := singleV2(t)
	for _, tc := range []struct {
		origin     int
		to, rc     string
		wantStatus int
	}{
		{200, "404", "0", 404},
		{200, "599", "0", 599},
		{200, "204", "-2", 200},
		{200, "205", "-2", 200},
		{200, "304", "-2", 200},
		{200, "199", "-2", 200},
		{200, "600", "-2", 200},
		{204, "200", "-3", 204},
		{304, "200", "-3", 304},
	} {
		for _, mode := range []string{"meta", "body"} {
			rec := serve(chainFor(s, act(tc.origin, ""), "p"), req(http.MethodGet, "/", "X-Op", "set-status", "X-Status-To", tc.to, "X-Mode", mode))
			if rec.Header().Get("X-RC") != tc.rc || rec.Code != tc.wantStatus {
				t.Errorf("%d->%s (%s): rc=%q code=%d, want %s/%d", tc.origin, tc.to, mode, rec.Header().Get("X-RC"), rec.Code, tc.rc, tc.wantStatus)
			}
		}
	}
}

func TestResponsePhaseHeaderMutation(t *testing.T) {
	s, _ := singleV2(t)
	rec := serve(chainFor(s, act(200, "b", "Server", "origin"), "p"), req(http.MethodGet, "/", "X-Op", "headers"))
	if got := rec.Header().Get("X-RCs"); got != "-3,-3,-3,-2,-2,0,0,0,0,-2" {
		t.Fatalf("header rcs = %q", got)
	}
	if v := rec.Header().Values("X-Multi"); len(v) != 2 || v[0] != "1" || v[1] != "2" {
		t.Fatalf("X-Multi = %v", v)
	}
	if rec.Header().Get("Server") != "" || rec.Header().Get("X-Injected") != "" || rec.Header().Get("X-Split") != "" {
		t.Fatalf("header mutation leaked: %v", rec.Header())
	}
	rec = serve(chainFor(s, act(200, "b"), "p"), req(http.MethodGet, "/", "X-Op", "flood"))
	if got := rec.Header().Get("X-RCs"); got != "0,-5" {
		t.Fatalf("flood rcs = %q", got)
	}
}

func TestResponsePhaseReject(t *testing.T) {
	s, hooks := singleV2(t)
	for _, tc := range []struct {
		to   string
		want int
	}{{"", 502}, {"403", 403}, {"200", 502}} {
		for _, mode := range []string{"meta", "body"} {
			w := httptest.NewRecorder()
			w.Header().Set("X-Request-Id", "rid")
			hdr := []string{"X-Op", "reject", "X-Mode", mode}
			if tc.to != "" {
				hdr = append(hdr, "X-Status-To", tc.to)
			}
			chainFor(s, act(200, "secret", "Server", "origin"), "p").ServeHTTP(w, req(http.MethodGet, "/", hdr...))
			if w.Code != tc.want || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "0" {
				t.Fatalf("reject %q/%s: %d %q", tc.to, mode, w.Code, w.Body.String())
			}
			if w.Header().Get("Server") != "" || w.Header().Get("X-Should-Not-Survive") != "" {
				t.Fatalf("reject kept action/guest headers: %v", w.Header())
			}
			if w.Header().Get("X-Request-Id") != "rid" || w.Header().Get("X-Sub-RC") != "0" {
				t.Fatalf("reject dropped headers set before the action: %v", w.Header())
			}
		}
	}
	if hooks.count(hooks.results, "reject") != 6 {
		t.Fatalf("reject count = %+v", hooks.results)
	}
}

func TestResponsePhaseFailuresFailClosed(t *testing.T) {
	s, hooks := singleV2(t, func(pc *config.PluginConfig) { pc.Timeout = config.Duration(100 * time.Millisecond) })
	for _, op := range []string{"trap", "loop", "oob", "wrong-phase", "resp-badresult"} {
		for _, mode := range []string{"meta", "body"} {
			t.Run(op+"/"+mode, func(t *testing.T) {
				before := hooks.count(hooks.results, "error")
				rec := serve(chainFor(s, act(200, "upstream", "Server", "origin"), "p"), req(http.MethodGet, "/", "X-Op", op, "X-Mode", mode))
				if rec.Code != 500 || rec.Body.String() != "plugin error\n" {
					t.Fatalf("%s: %d %q", op, rec.Code, rec.Body.String())
				}
				if rec.Header().Get("X-Partial") != "" || rec.Header().Get("Server") != "" {
					t.Fatalf("%s leaked a partially mutated response: %v", op, rec.Header())
				}
				if hooks.count(hooks.results, "error") != before+1 {
					t.Fatalf("%s: error not counted", op)
				}
			})
		}
	}
	if hooks.panics == 0 {
		t.Fatal("traps/timeouts not counted as contained panics")
	}
	// The plugin keeps serving after every failure.
	rec := serve(chainFor(s, act(200, "ok"), "p"), req(http.MethodGet, "/", "X-Op", "echo"))
	if rec.Code != 200 || rec.Header().Get("X-Seen-Status") != "200" {
		t.Fatalf("plugin did not recover: %d", rec.Code)
	}
}

func TestResponsePhaseCancelledRequestFails(t *testing.T) {
	s, hooks := singleV2(t)
	ctx, cancel := context.WithCancel(context.Background())
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		cancel()
		_, _ = io.WriteString(w, "late")
	})
	rec := serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body").WithContext(ctx))
	if rec.Code != 500 || hooks.count(hooks.results, "error") != 1 {
		t.Fatalf("cancelled: %d %+v", rec.Code, hooks.results)
	}
}

func TestRequestPhaseV2Contract(t *testing.T) {
	s, hooks := singleV2(t)
	called := false
	origin := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { called = true; _, _ = io.WriteString(w, "o") })
	for _, tc := range []struct {
		op, header, want string
		status           int
	}{
		{"req-wrong-phase", "X-RC", "-7", 200},
		{"req-state-big", "X-RC", "-5", 200},
		{"req-bad-mode", "X-RC", "-2", 200},
		{"req-only", "X-V2-Request", "1", 200},
		{"stop", "", "", 418},
		{"req-badresult", "", "", 500},
		{"req-oob", "", "", 500},
	} {
		called = false
		rec := serve(chainFor(s, origin, "p"), req(http.MethodGet, "/", "X-Op", tc.op))
		if rec.Code != tc.status || (tc.header != "" && rec.Header().Get(tc.header) != tc.want) {
			t.Fatalf("%s: %d %v", tc.op, rec.Code, rec.Header())
		}
		if called != (tc.status == 200) {
			t.Fatalf("%s: action called = %v", tc.op, called)
		}
	}
	if n := hooks.count(hooks.results, "continue") + hooks.count(hooks.results, "error"); n != 0 {
		t.Fatalf("unsubscribed requests ran the response hook: %+v", hooks.results)
	}
	if hooks.count(hooks.reqs, "error") != 2 {
		t.Fatalf("request errors = %+v", hooks.reqs)
	}
}

func TestHandlerTypeV2CannotSubscribe(t *testing.T) {
	m, _ := v2Manager(t, nil)
	s := buildSet(t, m, map[string]config.PluginConfig{"h": v2cfg(func(pc *config.PluginConfig) { pc.Type = "handler" })})
	rec := serve(s.Handler("h"), req(http.MethodGet, "/", "X-Op", "echo"))
	if rec.Header().Get("X-Sub-RC") != "-8" {
		t.Fatalf("handler subscribe rc = %q", rec.Header().Get("X-Sub-RC"))
	}
}

func TestResponsePhaseNestingOrder(t *testing.T) {
	m, _ := v2Manager(t, nil)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"outer": v2cfg(withOp("echo")),
		"inner": v2cfg(withOp("big")),
	})
	rec := serve(chainFor(s, act(200, "hello"), "outer", "inner"), req(http.MethodGet, "/", "X-Mode", "body", "X-Size", "3"))
	if rec.Header().Get("X-Body-Len") != "3" || rec.Body.Len() != 3 || rec.Header().Get("Content-Length") != "3" {
		t.Fatalf("outer did not see inner's output: %v %q", rec.Header(), rec.Body.String())
	}
	// Inner rejects: outer sees Jul's 502, never the origin body.
	s2 := buildSet(t, m, map[string]config.PluginConfig{
		"outer": v2cfg(withOp("echo")),
		"inner": v2cfg(withOp("reject")),
	})
	rec = serve(chainFor(s2, act(200, "secret"), "outer", "inner"), req(http.MethodGet, "/", "X-Mode", "body"))
	if rec.Code != 502 || rec.Header().Get("X-Seen-Status") != "502" || rec.Header().Get("X-Body-Len") != "0" {
		t.Fatalf("outer after inner reject: %d %v", rec.Code, rec.Header())
	}
}

func TestResponsePhaseStateIsPerRequest(t *testing.T) {
	s, _ := singleV2(t, func(pc *config.PluginConfig) { pc.MaxInvocations = 3 })
	h := chainFor(s, act(200, "x"), "p")
	if got := serve(h, req(http.MethodGet, "/", "X-Op", "echo", "X-State", "first")).Header().Get("X-State"); got != "first" {
		t.Fatalf("state = %q", got)
	}
	if got := serve(h, req(http.MethodGet, "/", "X-Op", "echo")).Header().Get("X-State"); got != "" {
		t.Fatalf("state leaked into the next request: %q", got)
	}
	var wg sync.WaitGroup
	var bad atomic.Int32
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				want := fmt.Sprintf("g%d-%d", g, i)
				rec := serve(h, req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body", "X-State", want))
				if rec.Header().Get("X-State") != want || rec.Body.String() != "x" {
					bad.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("%d requests saw another request's state", bad.Load())
	}
}

// TestResponsePhasePinsRequestGeneration: a request whose hook ran on one
// generation finishes its response phase on that generation's module even when
// a newer generation is built and serving meanwhile.
func TestResponsePhasePinsRequestGeneration(t *testing.T) {
	m, _ := v2Manager(t, nil)
	oldGen := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("echo"))})
	release := make(chan struct{})
	entered := make(chan struct{})
	blocking := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "old")
	})
	done := make(chan *httptest.ResponseRecorder)
	go func() { done <- serve(chainFor(oldGen, blocking, "p"), req(http.MethodGet, "/")) }()
	<-entered
	newGen := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("upper"))})
	if rec := serve(chainFor(newGen, act(200, "new"), "p"), req(http.MethodGet, "/", "X-Mode", "body")); rec.Body.String() != "NEW" {
		t.Fatalf("new generation: %q", rec.Body.String())
	}
	close(release)
	rec := <-done
	if rec.Header().Get("X-Seen-Status") != "200" || rec.Body.String() != "old" {
		t.Fatalf("in-flight request switched generation: %v %q", rec.Header(), rec.Body.String())
	}
}

func TestResponsePhaseV1V2Replacement(t *testing.T) {
	m, _ := v2Manager(t, nil)
	v1 := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("header-inject")})
	v2 := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("echo"))})
	back := buildSet(t, m, map[string]config.PluginConfig{"p": pcfg("v1-current-header-inject")})
	if v1.ResponsePoint("p") != nil || back.ResponsePoint("p") != nil {
		t.Fatal("a v1 plugin installed a response point")
	}
	for i, s := range []*Set{v1, v2, back} {
		rec := serve(chainFor(s, act(200, "b"), "p"), req(http.MethodGet, "/"))
		v2Seen := rec.Header().Get("X-Seen-Status") == "200"
		v1Seen := rec.Header().Get("X-Plugin") == "header-inject"
		if rec.Code != 200 || v2Seen == v1Seen || v2Seen != (i == 1) {
			t.Fatalf("generation %d: %d %v", i, rec.Code, rec.Header())
		}
	}
}

// TestResponsePhaseUpgradeIsObservedReadOnly drives a real hijack: the hook
// runs after the protocol switch and every mutation reports COMMITTED.
func TestResponsePhaseUpgradeIsObservedReadOnly(t *testing.T) {
	kv := newMemKV()
	m, hooks := v2Manager(t, kv)
	s := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("committed"), func(pc *config.PluginConfig) { pc.KV = true })})
	upgrade := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		conn, brw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: test\r\nConnection: Upgrade\r\n\r\n")
		_ = brw.Flush()
		_ = conn.Close()
	})
	srv := httptest.NewServer(chainFor(s, upgrade, "p"))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: test\r\nX-Mode: body\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil || resp.StatusCode != 101 {
		t.Fatalf("upgrade response: %v %v", resp, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if v, ok := kv.Get("p\x00committed"); ok {
			if string(v) != "101 -6 7" {
				t.Fatalf("post-commit view = %q", v)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("post-commit hook never ran")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if hooks.count(hooks.noBody, "upgraded") != 1 {
		t.Fatalf("upgraded not counted: %+v", hooks.noBody)
	}

	// WriteHeader(101) is the same committed, read-only observation.
	rec := serve(chainFor(s, act(101, ""), "p"), req(http.MethodGet, "/"))
	if rec.Code != 101 {
		t.Fatalf("101 = %d", rec.Code)
	}
	// Only CONTINUE is valid after commitment.
	s2 := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("reject"))})
	before := hooks.count(hooks.results, "error")
	rec = serve(chainFor(s2, act(101, ""), "p"), req(http.MethodGet, "/"))
	if rec.Code != 101 || hooks.count(hooks.results, "error") != before+1 {
		t.Fatalf("post-commit reject: %d %+v", rec.Code, hooks.results)
	}
}

func TestResponsePhaseInterimAndNotReached(t *testing.T) {
	s, hooks := singleV2(t)
	interim := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Link", "</a.css>; rel=preload")
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "x")
	})
	rec := serve(chainFor(s, interim, "p"), req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
	if rec.Header().Get("X-Seen-Status") != "200" || rec.Body.String() != "x" {
		t.Fatalf("interim: %v", rec.Header())
	}
	// A denial before the response point (here: a layer that answers itself)
	// is never presented to the hook.
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) })
	}
	before := hooks.count(hooks.results, "continue")
	h := s.Middleware("p")(deny(s.ResponsePoint("p")(act(200, "x"))))
	if rec := serve(h, req(http.MethodGet, "/", "X-Op", "echo")); rec.Code != 401 || rec.Header().Get("X-Seen-Status") != "" {
		t.Fatalf("denial presented: %d %v", rec.Code, rec.Header())
	}
	if hooks.count(hooks.results, "continue") != before {
		t.Fatal("hook ran for a response that never reached the response point")
	}
	// Without a subscription the response point is a pass-through.
	if rec := serve(s.ResponsePoint("p")(act(200, "x")), req(http.MethodGet, "/")); rec.Body.String() != "x" {
		t.Fatalf("pass-through: %q", rec.Body.String())
	}
}

// TestResponsePhaseAbsorbsInducedAbort: after a reject the action is
// cancelled; its ErrAbortHandler is absorbed, a genuine one is not.
func TestResponsePhaseAbsorbsInducedAbort(t *testing.T) {
	s, _ := singleV2(t)
	aborting := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		<-r.Context().Done()
		panic(http.ErrAbortHandler)
	})
	rec := serve(chainFor(s, aborting, "p"), req(http.MethodGet, "/", "X-Op", "reject"))
	if rec.Code != 502 {
		t.Fatalf("induced abort: %d", rec.Code)
	}
	defer func() {
		if rec := recover(); rec != http.ErrAbortHandler {
			t.Fatalf("genuine abort not propagated: %v", rec)
		}
	}()
	serve(chainFor(s, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(http.ErrAbortHandler) }), "p"),
		req(http.MethodGet, "/", "X-Op", "echo", "X-Mode", "body"))
}

func TestClassifyResponse(t *testing.T) {
	h := http.Header{"Content-Encoding": {"identity"}, "Content-Length": {"bogus"}}
	if got := classifyResponse(responseModeBody, http.MethodGet, 200, h, 10); got != bodyAvailable {
		t.Fatalf("identity/bogus length = %d", got)
	}
	h = http.Header{"Content-Encoding": {"identity, br"}}
	if got := classifyResponse(responseModeBody, http.MethodGet, 200, h, 10); got != bodyEncoded {
		t.Fatalf("stacked encoding = %d", got)
	}
	if got := bodyStateLabel(bodyAvailable); got != "" {
		t.Fatalf("available has a label: %q", got)
	}
}
