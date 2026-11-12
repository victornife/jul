// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"

	"jul/internal/config"
)

// bodyGuestMode selects what a hand-assembled body guest's handle_request does.
type bodyGuestMode int

const (
	// guestEcho reads the request body and stops with it as the response.
	guestEcho bodyGuestMode = iota
	// guestReadContinue reads the request body and continues.
	guestReadContinue
	// guestReject imports read_request_body but stops with 403 without
	// calling it: a conditional reader taking its no-read branch.
	guestReject
	// guestReadSpin reads the body, then loops until the call deadline.
	guestReadSpin
	// guestContinue continues without importing read_request_body.
	guestContinue
)

// bodyGuest assembles a minimal module for abi whose handle_request follows
// mode. Imports use the real host signatures, so negotiation and static
// eligibility see exactly what a compiled SDK guest would declare.
func bodyGuest(abi string, mode bodyGuestMode) []byte {
	host := hostModuleV1
	if abi == ABIJulV2 {
		host = hostModuleV2
	}
	types := [][]byte{
		{0x60, 0x02, i32, i32, 0x01, i32}, // 0 read_request_body
		{0x60, 0x02, i32, i32, 0x00},      // 1 write_response_body
		{0x60, 0x01, i32, 0x00},           // 2 set_response_status
		{0x60, 0x00, 0x01, i32},           // 3 handle_request
		{0x60, 0x00, 0x00},                // 4 v2 marker
	}
	imp := func(name string, ty byte) []byte {
		return append(append(wasmName(host), wasmName(name)...), 0x00, ty)
	}
	imports := [][]byte{imp("write_response_body", 1), imp("set_response_status", 2)}
	const writeIdx, statusIdx = 0, 1
	readIdx := byte(0)
	if mode != guestContinue {
		readIdx = byte(len(imports))
		imports = append(imports, imp("read_request_body", 0))
	}
	n := byte(len(imports))
	read := []byte{0x41, 0x80, 0x08, 0x41, 0x80, 0xa0, 0x07, 0x10, readIdx, 0x21, 0x00} // local0 = read(1024, 118784)
	var body []byte
	switch mode {
	case guestEcho:
		body = append(read, 0x41, 0x80, 0x08, 0x20, 0x00, 0x10, writeIdx, 0x41, 0x00)
	case guestReadContinue:
		body = append(read, 0x41, 0x01)
	case guestReject:
		body = []byte{0x41, 0x93, 0x03, 0x10, statusIdx, 0x41, 0x00}
	case guestReadSpin:
		body = append(read, 0x03, 0x40, 0x0c, 0x00, 0x0b, 0x41, 0x00)
	case guestContinue:
		body = []byte{0x41, 0x01}
	}
	fb := append([]byte{0x01, 0x01, i32}, body...)
	fb = append(fb, 0x0b)
	funcs := [][]byte{{0x03}}
	exports := [][]byte{append(wasmName(exportHandleRequest), 0x00, n)}
	code := [][]byte{append(leb(len(fb)), fb...)}
	if abi == ABIJulV2 {
		funcs = append(funcs, []byte{0x04})
		exports = append(exports, append(wasmName(exportABIV2Marker), 0x00, n+1))
		code = append(code, []byte{0x02, 0x00, 0x0b})
	}
	out := append([]byte(nil), wasmHeader...)
	out = append(out, wasmSection(1, types)...)
	out = append(out, wasmSection(2, imports)...)
	out = append(out, wasmSection(3, funcs)...)
	out = append(out, wasmSection(5, [][]byte{{0x00, 0x02}})...)
	out = append(out, wasmSection(7, exports)...)
	return append(out, wasmSection(10, code)...)
}

func bodyGuestConfig(abi string, mode bodyGuestMode, opts ...func(*config.PluginConfig)) config.PluginConfig {
	all := append([]func(*config.PluginConfig){
		inlineSrc(bodyGuest(abi, mode)),
		func(pc *config.PluginConfig) {
			pc.Path = ""
			pc.ABI = abi
			pc.Timeout = config.Duration(100 * time.Millisecond)
		},
	}, opts...)
	return pcfg("", all...)
}

func randomBody(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func digest(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// pacedBody delivers data in chunks with a gap after each, so the whole
// upload takes several plugin timeouts to arrive.
func pacedBody(data []byte, chunk int, gap time.Duration) io.Reader {
	pr, pw := io.Pipe()
	go func() {
		for off := 0; off < len(data); off += chunk {
			end := min(off+chunk, len(data))
			if _, err := pw.Write(data[off:end]); err != nil {
				return
			}
			time.Sleep(gap)
		}
		_ = pw.Close()
	}()
	return pr
}

// bodyHooks records the manager callbacks the #699 tests assert on.
type bodyHooks struct {
	mu      sync.Mutex
	results map[string]int
	durs    []time.Duration
	fails   []string
	panics  int
	waits   int
	failed  chan string
}

func (h *bodyHooks) invoked(_, result string, d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.results[result]++
	h.durs = append(h.durs, d)
}

func (h *bodyHooks) fail(_, reason string) {
	h.mu.Lock()
	h.fails = append(h.fails, reason)
	h.mu.Unlock()
	select {
	case h.failed <- reason:
	default:
	}
}

func (h *bodyHooks) snapshot() (invocations int, results map[string]int, fails []string, panics, waits int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, n := range h.results {
		invocations += n
	}
	return invocations, maps(h.results), slices.Clone(h.fails), h.panics, h.waits
}

func maps(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// awaitFail returns the next preparation-failure reason, failing the test if
// none arrives promptly: the transport or retirement must end a blocked read.
func (h *bodyHooks) awaitFail(t *testing.T) string {
	t.Helper()
	select {
	case r := <-h.failed:
		return r
	case <-time.After(5 * time.Second):
		t.Fatal("body preparation was not terminated within 5s")
		return ""
	}
}

func bodyManager(t *testing.T) (*Manager, *bodyHooks) {
	t.Helper()
	h := &bodyHooks{results: map[string]int{}, failed: make(chan string, 16)}
	m, err := NewManager(Options{Inspection: testGovernor(t),
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
	return m, h
}

// echoNext answers 200 with the exact bytes it read and counts its calls.
func echoNext(hits *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var b []byte
		var err error
		if r.Body != nil {
			b, err = io.ReadAll(r.Body)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("X-Next-Len", itoa(len(b)))
		w.Header().Set("X-Next-CL", itoa(int(r.ContentLength)))
		for k, v := range r.Trailer {
			w.Header().Set("X-Next-Trailer-"+k, strings.Join(v, ","))
		}
		_, _ = w.Write(b)
	})
}

func preparing(p *plugin) int {
	p.prepMu.Lock()
	defer p.prepMu.Unlock()
	return len(p.preparing)
}

// waitFor polls cond, which observes state another goroutine is driving.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// countingBody counts bytes read from, and closes of, an original body.
type countingBody struct {
	r      io.Reader
	n      atomic.Int64
	closed atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingBody) Close() error { c.closed.Add(1); return nil }

// TestSlowUploadDoesNotConsumeGuestTimeout is the #699 reproducer: a
// legitimate 64 KiB body that takes four 100 ms plugin timeouts to arrive must
// reach the guest and the next handler intact, and the guest call itself must
// stay well inside its timeout.
func TestSlowUploadDoesNotConsumeGuestTimeout(t *testing.T) {
	data := randomBody(t, 64<<10)
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		for _, tc := range []struct {
			name    string
			mode    bodyGuestMode
			handler bool
		}{
			{"middleware echo", guestEcho, false},
			{"middleware continue", guestReadContinue, false},
			{"handler echo", guestEcho, true},
		} {
			t.Run(abi+"/"+tc.name, func(t *testing.T) {
				m, hooks := bodyManager(t)
				s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, tc.mode)})
				var hits atomic.Int64
				h := s.Middleware("g")(echoNext(&hits))
				if tc.handler {
					h = s.Handler("g")
				}
				srv := httptest.NewServer(h)
				defer srv.Close()

				for _, paced := range []bool{false, true} {
					hits.Store(0)
					var rd io.Reader = bytes.NewReader(data)
					if paced {
						rd = pacedBody(data, 4<<10, 25*time.Millisecond)
					}
					start := time.Now()
					resp, err := http.Post(srv.URL, "application/octet-stream", rd)
					if err != nil {
						t.Fatal(err)
					}
					got, _ := io.ReadAll(resp.Body)
					_ = resp.Body.Close()
					elapsed := time.Since(start)
					wantHits := int64(0)
					if tc.mode == guestReadContinue {
						wantHits = 1
					}
					t.Logf("paced=%v status=%d elapsed=%s next=%d", paced, resp.StatusCode, elapsed.Round(time.Millisecond), hits.Load())
					if resp.StatusCode != http.StatusOK || digest(got) != digest(data) || hits.Load() != wantHits {
						t.Fatalf("paced=%v: status=%d len=%d exact=%v next=%d, want 200 with the exact body and next=%d",
							paced, resp.StatusCode, len(got), digest(got) == digest(data), hits.Load(), wantHits)
					}
					if paced && elapsed < 300*time.Millisecond {
						t.Fatalf("paced upload arrived in %s; it must exceed the guest timeout to reproduce #699", elapsed)
					}
				}
				n, results, fails, panics, _ := hooks.snapshot()
				if n != 2 || results["error"] != 0 || len(fails) != 0 || panics != 0 {
					t.Fatalf("invocations=%d results=%v fails=%v panics=%d, want 2 successful guest calls", n, results, fails, panics)
				}
				for _, d := range hooks.durs {
					if d >= 100*time.Millisecond {
						t.Fatalf("guest call took %s: upload arrival leaked into the guest timeout", d)
					}
				}
				assertNoSlotLeak(t, s.plugins["g"])
			})
		}
	}
}

// Eligibility is the static read_request_body import: only modules that can
// call it have bodies prepared.
func TestBodyEligibilityFollowsStaticImport(t *testing.T) {
	m := testManager(t)
	fixtures := map[string]bool{
		"header-inject": false, "kv-counter": false, "request-block": false,
		"testguest-loop": false, "testguest-panic": false, "v1-current-header-inject": false,
		"v2-redact": false, "v2-status-header": false, "testguest-v2": true,
	}
	cfg := map[string]config.PluginConfig{}
	for name := range fixtures {
		pc := pcfg("", fixtureSrc(name))
		if strings.HasPrefix(name, "v2-") || name == "testguest-v2" {
			pc.ABI = ABIJulV2
		}
		cfg[name] = pc
	}
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		for mode, want := range map[bodyGuestMode]bool{guestEcho: true, guestReject: true, guestContinue: false} {
			cfg[abi+itoa(int(mode))] = bodyGuestConfig(abi, mode)
			fixtures[abi+itoa(int(mode))] = want
		}
	}
	s := buildSet(t, m, cfg)
	for name, want := range fixtures {
		if got := s.plugins[name].readsBody; got != want {
			t.Errorf("%s readsBody = %v, want %v", name, got, want)
		}
	}
}

// A module without the import keeps the pass-through path: next runs before
// any byte of the body has arrived and streams the original body.
func TestNonReaderStreamsWithoutPreparation(t *testing.T) {
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		t.Run(abi, func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestContinue)})
			pr, pw := io.Pipe()
			orig := &countingBody{r: pr}
			entered := make(chan struct{})
			var got []byte
			var sameBody bool
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				sameBody = r.Body == io.ReadCloser(orig) && orig.n.Load() == 0
				close(entered)
				got, _ = io.ReadAll(r.Body)
			})
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body, r.ContentLength = orig, -1
			done := make(chan struct{})
			go func() { defer close(done); serve(s.Middleware("g")(next), r) }()
			<-entered
			_, _ = pw.Write([]byte("streamed"))
			_ = pw.Close()
			<-done
			n, _, fails, _, _ := hooks.snapshot()
			if !sameBody || string(got) != "streamed" || n != 1 || len(fails) != 0 || preparing(s.plugins["g"]) != 0 {
				t.Fatalf("sameBody=%v got=%q invocations=%d fails=%v", sameBody, got, n, fails)
			}
		})
	}
}

// A module that imports read_request_body but rejects without calling it is
// still prepared: the static policy cannot see the branch. Its rejection waits
// for the body, and a body over its max_request_body answers 500 instead of
// the guest's decision. This is the documented compatibility consequence.
func TestConditionalReaderIsPreparedBeforeEarlyReject(t *testing.T) {
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		t.Run(abi, func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestReject, func(pc *config.PluginConfig) {
				pc.MaxRequestBody = config.Size(16)
			})})
			p := s.plugins["g"]
			h := s.Middleware("g")(http.NotFoundHandler())

			pr, pw := io.Pipe()
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body, r.ContentLength = pr, -1
			var rec *httptest.ResponseRecorder
			done := make(chan struct{})
			go func() { defer close(done); rec = serve(h, r) }()
			_, _ = pw.Write([]byte("part"))
			waitFor(t, "preparation in progress", func() bool { return preparing(p) == 1 })
			if n, _, _, _, _ := hooks.snapshot(); n != 0 {
				t.Fatal("guest ran before the conditional reader's body arrived")
			}
			_ = pw.Close()
			<-done
			if rec.Code != http.StatusForbidden {
				t.Fatalf("prepared early reject = %d, want the guest's 403", rec.Code)
			}

			rec = serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 17))))
			n, _, fails, _, _ := hooks.snapshot()
			if rec.Code != http.StatusInternalServerError || n != 1 || !slices.Equal(fails, []string{bodyFailTooLarge}) {
				t.Fatalf("oversize to conditional reader = %d invocations=%d fails=%v, want 500 too_large without a guest call", rec.Code, n, fails)
			}
		})
	}
}

// Nil, empty, exact-limit and limit+1 bodies, with known, unknown and
// dishonest lengths, on both ABIs.
func TestPreparedBodyLimits(t *testing.T) {
	const limit = 16
	exact, over := strings.Repeat("a", limit), strings.Repeat("b", limit+1)
	cases := []struct {
		name     string
		body     *string
		cl       int64
		status   int
		reason   string
		wantRead int64
	}{
		{"nil", nil, 0, 200, "", 0},
		{"empty", ptr(""), 0, 200, "", 0},
		{"exact known", &exact, limit, 200, "", limit},
		{"exact chunked", &exact, -1, 200, "", limit},
		{"over chunked", &over, -1, 500, bodyFailTooLarge, limit + 1},
		{"over dishonest short length", &over, 4, 500, bodyFailTooLarge, limit + 1},
		{"over declared", &over, limit + 1, 500, bodyFailTooLarge, 0},
	}
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		for _, tc := range cases {
			t.Run(abi+"/"+tc.name, func(t *testing.T) {
				m, hooks := bodyManager(t)
				s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestReadContinue, func(pc *config.PluginConfig) {
					pc.MaxRequestBody = config.Size(limit)
				})})
				var hits atomic.Int64
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				orig := &countingBody{}
				switch {
				case tc.body == nil:
					r.Body = nil
				case *tc.body == "":
					r.Body = http.NoBody
				default:
					orig.r = strings.NewReader(*tc.body)
					r.Body = orig
				}
				r.ContentLength = tc.cl
				rec := serve(s.Middleware("g")(echoNext(&hits)), r)
				n, _, fails, panics, _ := hooks.snapshot()
				if rec.Code != tc.status || orig.n.Load() != tc.wantRead || panics != 0 {
					t.Fatalf("status=%d read=%d panics=%d, want %d read=%d", rec.Code, orig.n.Load(), panics, tc.status, tc.wantRead)
				}
				if tc.reason != "" {
					if n != 0 || hits.Load() != 0 || !slices.Equal(fails, []string{tc.reason}) {
						t.Fatalf("failure invoked guest=%d next=%d fails=%v", n, hits.Load(), fails)
					}
					return
				}
				want := ""
				if tc.body != nil {
					want = *tc.body
				}
				if n != 1 || hits.Load() != 1 || rec.Body.String() != want || len(fails) != 0 {
					t.Fatalf("invocations=%d next=%d body=%q fails=%v", n, hits.Load(), rec.Body.String(), fails)
				}
				if tc.body != nil && *tc.body != "" && (rec.Header().Get("X-Next-CL") != itoa(len(want)) || orig.closed.Load() != 0) {
					t.Fatalf("Content-Length=%s closes=%d, want %d and the original body left for its owner", rec.Header().Get("X-Next-CL"), orig.closed.Load(), len(want))
				}
			})
		}
	}
}

func ptr[T any](v T) *T { return &v }

// A chain prepares once: a later plugin reuses the unconsumed prepared body
// without reading or copying it again, yet enforces its own smaller limit.
func TestPreparedBodyChainReuseAndPerPluginLimits(t *testing.T) {
	m, hooks := bodyManager(t)
	lim := func(n int) func(*config.PluginConfig) {
		return func(pc *config.PluginConfig) { pc.MaxRequestBody = config.Size(n) }
	}
	s := buildSet(t, m, map[string]config.PluginConfig{
		"big":   bodyGuestConfig(ABIJulV1, guestReadContinue, lim(64)),
		"small": bodyGuestConfig(ABIJulV2, guestReadContinue, lim(16)),
	})
	var hits atomic.Int64
	run := func(names []string, body string) (*httptest.ResponseRecorder, *countingBody) {
		r := httptest.NewRequest(http.MethodPost, "/", nil)
		orig := &countingBody{r: strings.NewReader(body)}
		r.Body, r.ContentLength = orig, -1
		return serve(chainFor(s, echoNext(&hits), names...), r), orig
	}

	rec, orig := run([]string{"big", "small"}, "0123456789")
	if rec.Code != 200 || rec.Body.String() != "0123456789" || orig.n.Load() != 10 || hits.Load() != 1 {
		t.Fatalf("chain within limits: status=%d body=%q read=%d next=%d", rec.Code, rec.Body.String(), orig.n.Load(), hits.Load())
	}
	for _, names := range [][]string{{"big", "small"}, {"small", "big"}} {
		hooks.mu.Lock()
		hooks.fails = nil
		hooks.mu.Unlock()
		rec, _ = run(names, strings.Repeat("z", 20))
		_, _, fails, _, _ := hooks.snapshot()
		if rec.Code != 500 || hits.Load() != 1 || !slices.Equal(fails, []string{bodyFailTooLarge}) {
			t.Fatalf("%v over the smaller limit: status=%d next=%d fails=%v", names, rec.Code, hits.Load(), fails)
		}
	}
}

// Upload arrival holds no instance: with max_instances = 1, a request still
// receiving its body leaves the only instance idle for another request.
func TestBodyPreparationHoldsNoInstance(t *testing.T) {
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		t.Run(abi, func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestEcho, func(pc *config.PluginConfig) {
				pc.MaxInstances = 1
			})})
			p := s.plugins["g"]
			h := s.Handler("g")

			pr, pw := io.Pipe()
			slow := httptest.NewRequest(http.MethodPost, "/", nil)
			slow.Body, slow.ContentLength = pr, -1
			var slowRec *httptest.ResponseRecorder
			done := make(chan struct{})
			go func() { defer close(done); slowRec = serve(h, slow) }()
			_, _ = pw.Write([]byte("slow-"))
			waitFor(t, "slow upload preparing", func() bool { return preparing(p) == 1 })
			if live, idle := len(p.slots), len(p.pool); live != 1 || idle != 1 {
				t.Fatalf("while uploading: live=%d idle=%d, want the only instance idle", live, idle)
			}
			fast := serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("fast")))
			if fast.Code != 200 || fast.Body.String() != "fast" {
				t.Fatalf("eligible request during a slow upload = %d %q", fast.Code, fast.Body.String())
			}
			_, _ = pw.Write([]byte("done"))
			_ = pw.Close()
			<-done
			_, _, _, _, waits := hooks.snapshot()
			if slowRec.Code != 200 || slowRec.Body.String() != "slow-done" || waits != 0 {
				t.Fatalf("slow upload = %d %q, instance waits=%d", slowRec.Code, slowRec.Body.String(), waits)
			}
			assertNoSlotLeak(t, p)
		})
	}
}

// The guest timeout still contains a CPU-bound guest, and it now starts after
// the body has arrived rather than when the request did.
func TestGuestTimeoutStillContainedAfterPreparation(t *testing.T) {
	data := randomBody(t, 8<<10)
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		t.Run(abi, func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestReadSpin)})
			r := httptest.NewRequest(http.MethodPost, "/", pacedBody(data, 1<<10, 40*time.Millisecond))
			rec := serve(s.Middleware("g")(http.NotFoundHandler()), r)
			n, results, fails, panics, _ := hooks.snapshot()
			if rec.Code != 500 || n != 1 || results["error"] != 1 || panics != 1 || len(fails) != 0 {
				t.Fatalf("status=%d results=%v panics=%d fails=%v, want a contained guest timeout", rec.Code, results, panics, fails)
			}
			if d := hooks.durs[0]; d < 90*time.Millisecond || d > 250*time.Millisecond {
				t.Fatalf("guest call ran %s; the 100 ms timeout must start after the ~320 ms upload", d)
			}
			assertNoSlotLeak(t, s.plugins["g"])
		})
	}
}

// cancelOnEOF cancels its request's context as the body completes.
type cancelOnEOF struct {
	r      io.Reader
	cancel context.CancelFunc
}

func (c cancelOnEOF) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if err == io.EOF {
		c.cancel()
	}
	return n, err
}

func (cancelOnEOF) Close() error { return nil }

// Preparation failures and cancellation end the request without acquiring
// an instance, invoking the guest or reaching the origin.
func TestPreparationFailuresSkipGuestAndOrigin(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		req    func() *http.Request
	}{
		{"read error", bodyFailReadError, func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body = io.NopCloser(io.MultiReader(strings.NewReader("abc"), errorReader{}))
			return r
		}},
		{"canceled after body", bodyFailCanceled, func() *http.Request {
			ctx, cancel := context.WithCancel(context.Background())
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
			r.Body = cancelOnEOF{r: strings.NewReader("complete"), cancel: cancel}
			return r
		}},
		{"canceled during upload", bodyFailCanceled, func() *http.Request {
			ctx, cancel := context.WithCancel(context.Background())
			pr, pw := io.Pipe()
			r := httptest.NewRequestWithContext(ctx, http.MethodPost, "/", nil)
			r.Body = pr
			go func() {
				_, _ = pw.Write([]byte("part"))
				cancel()
				_ = pw.CloseWithError(context.Canceled) // what the transport does on cancel
			}()
			return r
		}},
	}
	for _, abi := range []string{ABIJulV1, ABIJulV2} {
		for _, tc := range cases {
			t.Run(abi+"/"+tc.name, func(t *testing.T) {
				m, hooks := bodyManager(t)
				s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(abi, guestReadContinue)})
				var hits atomic.Int64
				rec := serve(s.Middleware("g")(echoNext(&hits)), tc.req())
				n, _, fails, panics, waits := hooks.snapshot()
				if rec.Code != 500 || n != 0 || hits.Load() != 0 || panics != 0 || waits != 0 || !slices.Equal(fails, []string{tc.reason}) {
					t.Fatalf("status=%d invocations=%d next=%d panics=%d fails=%v, want 500 %s without a guest call", rec.Code, n, hits.Load(), panics, fails, tc.reason)
				}
				if p := s.plugins["g"]; len(p.pool) != 1 || len(p.slots) != 1 || preparing(p) != 0 {
					t.Fatalf("instance acquired or preparation left registered: idle=%d live=%d preparing=%d", len(p.pool), len(p.slots), preparing(p))
				}
			})
		}
	}
}

// A closed plugin rejects without acquiring from its closed runtime, and that
// is not a guest panic.
func TestClosedPluginRejectsWithoutPanic(t *testing.T) {
	m, hooks := bodyManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"reader": bodyGuestConfig(ABIJulV2, guestReadContinue),
		"plain":  bodyGuestConfig(ABIJulV1, guestContinue),
	})
	_ = s.Close()
	for _, name := range []string{"reader", "plain"} {
		rec := serve(s.Middleware(name)(http.NotFoundHandler()), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x")))
		if rec.Code != 500 {
			t.Fatalf("%s after close = %d, want 500", name, rec.Code)
		}
	}
	n, _, fails, panics, _ := hooks.snapshot()
	if n != 0 || panics != 0 || !slices.Equal(fails, []string{bodyFailRetired}) {
		t.Fatalf("invocations=%d panics=%d fails=%v", n, panics, fails)
	}
	if !errors.Is(func() error { _, err := s.plugins["plain"].acquire(context.Background()); return err }(), errPluginClosed) {
		t.Fatal("acquire on a closed plugin did not report errPluginClosed")
	}
}

// A request preparing its body during a reload stays on the generation that
// accepted it; the new generation serves new requests.
func TestReloadKeepsPreparingRequestOnItsGeneration(t *testing.T) {
	m, _ := bodyManager(t)
	gen1 := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestEcho)})
	h1 := gen1.Handler("g")
	pr, pw := io.Pipe()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body = pr
	var rec *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() { defer close(done); rec = serve(h1, r) }()
	_, _ = pw.Write([]byte("gen1-"))
	waitFor(t, "gen1 preparing", func() bool { return preparing(gen1.plugins["g"]) == 1 })

	gen2 := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestReject)})
	if got := serve(gen2.Handler("g"), httptest.NewRequest(http.MethodPost, "/", strings.NewReader("x"))); got.Code != http.StatusForbidden {
		t.Fatalf("gen2 = %d, want 403", got.Code)
	}
	_, _ = pw.Write([]byte("body"))
	_ = pw.Close()
	<-done
	if rec.Code != 200 || rec.Body.String() != "gen1-body" {
		t.Fatalf("request accepted by gen1 = %d %q, want gen1's echo", rec.Code, rec.Body.String())
	}
}

func TestReadBoundedSizingAndPreparedBody(t *testing.T) {
	rb := func(r io.Reader, hint int64, limit int) ([]byte, error) {
		cb, err := readBounded(r, hint, limit, freeCharge)
		if err != nil {
			return nil, err
		}
		return cb.b, nil
	}
	b, err := rb(strings.NewReader("12345"), 5, 8)
	if err != nil || string(b) != "12345" || cap(b) != 6 {
		t.Fatalf("known length: %q cap=%d err=%v, want exact capacity 6", b, cap(b), err)
	}
	if b, err = rb(strings.NewReader("12345"), 100, 8); err != nil || string(b) != "12345" {
		t.Fatalf("hint over limit: %q %v", b, err)
	}
	big := strings.Repeat("q", 3000)
	if b, err = rb(strings.NewReader(big), -1, 4096); err != nil || string(b) != big {
		t.Fatalf("unknown length growth: len=%d %v", len(b), err)
	}
	if _, err = rb(strings.NewReader(big), -1, 2999); !errors.Is(err, errBodyTooLarge) {
		t.Fatalf("limit+1: %v", err)
	}
	// A declared length is not trusted for allocation: before any byte
	// arrives the buffer offered to the body is at most initialBodyBuffer.
	probe := &firstReadCap{}
	if _, err = rb(probe, 1<<20, 1<<20); err != nil || probe.n > initialBodyBuffer {
		t.Fatalf("declared 1 MiB: first read offered %d bytes (err %v), want <= %d", probe.n, err, initialBodyBuffer)
	}
	known := strings.Repeat("k", 100<<10)
	if b, err = rb(strings.NewReader(known), int64(len(known)), 1<<20); err != nil || string(b) != known || cap(b) != len(known)+1 {
		t.Fatalf("known 100 KiB: len=%d cap=%d %v, want growth to stop at the declared length", len(b), cap(b), err)
	}
	orig := &countingBody{r: strings.NewReader("")}
	pb := newPreparedBody([]byte("replay"), orig)
	var out bytes.Buffer
	if n, err := io.Copy(&out, pb); err != nil || n != 6 || out.String() != "replay" || pb.unread() {
		t.Fatalf("replay: %d %q %v", n, out.String(), err)
	}
	if err := pb.Close(); err != nil || orig.closed.Load() != 1 {
		t.Fatal("Close did not close the original body")
	}
}

// firstReadCap records the buffer size of its first Read and then ends.
type firstReadCap struct{ n int }

func (f *firstReadCap) Read(p []byte) (int, error) {
	if f.n == 0 {
		f.n = len(p)
	}
	return 0, io.EOF
}

// netServer serves h over HTTP/1.1, or HTTP/2 over TLS, with a finite read
// timeout when readTimeout > 0. conns counts accepted connections.
func netServer(t *testing.T, h http.Handler, h2 bool, readTimeout time.Duration) (*httptest.Server, *http.Client, *atomic.Int64) {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ReadTimeout = readTimeout
	var conns atomic.Int64
	srv.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			conns.Add(1)
		}
	}
	if h2 {
		srv.EnableHTTP2 = true
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	c := srv.Client()
	t.Cleanup(c.CloseIdleConnections)
	return srv, c, &conns
}

type netResult struct {
	status int
	proto  int
	body   string
	err    error
}

// startUpload posts a body the test feeds through the returned pipe.
func startUpload(ctx context.Context, c *http.Client, url string) (*io.PipeWriter, <-chan netResult) {
	pr, pw := io.Pipe()
	out := make(chan netResult, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, pr)
		resp, err := c.Do(req)
		if err != nil {
			out <- netResult{err: err}
			return
		}
		b, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		out <- netResult{status: resp.StatusCode, proto: resp.ProtoMajor, body: string(b)}
	}()
	return pw, out
}

func post(c *http.Client, url, body string) netResult {
	resp, err := c.Post(url, "text/plain", strings.NewReader(body))
	if err != nil {
		return netResult{err: err}
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return netResult{status: resp.StatusCode, proto: resp.ProtoMajor, body: string(b)}
}

// Transport controls end a blocked preparation: a client disconnect, and an
// explicitly configured server read timeout. On HTTP/2 both are per stream,
// so another stream on the same connection is unaffected.
func TestPreparationEndsOnTransportControls(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		proto := map[bool]string{false: "h1", true: "h2"}[h2]
		for _, mode := range []string{"disconnect", "read_timeout"} {
			t.Run(proto+"/"+mode, func(t *testing.T) {
				m, hooks := bodyManager(t)
				s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestEcho)})
				mux := http.NewServeMux()
				mux.Handle("/p", s.Handler("g"))
				var hits atomic.Int64
				mux.Handle("/other", echoNext(&hits))
				readTimeout := time.Duration(0)
				if mode == "read_timeout" {
					readTimeout = 300 * time.Millisecond
				}
				srv, c, conns := netServer(t, mux, h2, readTimeout)
				if h2 {
					if r := post(c, srv.URL+"/other", "warm"); r.err != nil || r.proto != 2 {
						t.Fatalf("h2 warm-up: %+v", r)
					}
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pw, res := startUpload(ctx, c, srv.URL+"/p")
				_, _ = pw.Write([]byte("partial"))
				waitFor(t, "upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
				if h2 {
					if r := post(c, srv.URL+"/other", "sibling"); r.err != nil || r.status != 200 || r.body != "sibling" {
						t.Fatalf("sibling stream during preparation: %+v", r)
					}
				}
				start := time.Now()
				if mode == "disconnect" {
					cancel()
				}
				reason := hooks.awaitFail(t)
				if mode == "read_timeout" && reason != bodyFailReadError {
					t.Fatalf("read timeout reason = %s, want read_error", reason)
				}
				if mode == "disconnect" && reason != bodyFailCanceled {
					t.Fatalf("disconnect reason = %s", reason)
				}
				t.Logf("%s ended preparation after %s with reason %s", mode, time.Since(start).Round(time.Millisecond), reason)
				_ = pw.Close()
				<-res
				if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
					t.Fatalf("guest invoked=%d panics=%d after a failed preparation", n, panics)
				}
				if h2 {
					if r := post(c, srv.URL+"/other", "after"); r.err != nil || r.status != 200 || conns.Load() != 1 {
						t.Fatalf("h2 connection after the stream ended: %+v conns=%d, want the same connection", r, conns.Load())
					}
				}
			})
		}
	}
}

// Forced retirement aborts a body read through the request's own read
// deadline: the request fails promptly as retired without touching the closed
// runtime, and on HTTP/2 the connection keeps serving other streams.
func TestForcedRetirementAbortsPreparation(t *testing.T) {
	for _, h2 := range []bool{false, true} {
		t.Run(map[bool]string{false: "h1", true: "h2"}[h2], func(t *testing.T) {
			m, hooks := bodyManager(t)
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV1, guestEcho)})
			mux := http.NewServeMux()
			mux.Handle("/p", s.Handler("g"))
			var hits atomic.Int64
			mux.Handle("/other", echoNext(&hits))
			srv, c, conns := netServer(t, mux, h2, 0)
			if h2 {
				_ = post(c, srv.URL+"/other", "warm")
			}
			pw, res := startUpload(context.Background(), c, srv.URL+"/p")
			defer pw.Close()
			_, _ = pw.Write([]byte("partial"))
			waitFor(t, "upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
			_ = s.Close()
			if reason := hooks.awaitFail(t); reason != bodyFailRetired {
				t.Fatalf("retirement reason = %s", reason)
			}
			if r := <-res; r.err == nil && r.status != 500 {
				t.Fatalf("retired upload answered %d", r.status)
			}
			if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
				t.Fatalf("closed runtime touched: invocations=%d panics=%d", n, panics)
			}
			if h2 {
				if r := post(c, srv.URL+"/other", "after"); r.err != nil || r.status != 200 || conns.Load() != 1 {
					t.Fatalf("h2 after retirement: %+v conns=%d", r, conns.Load())
				}
			}
		})
	}
}

// Trailers survive preparation because the body is read to EOF.
func TestPreparationPreservesTrailers(t *testing.T) {
	m, _ := bodyManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestReadContinue)})
	var hits atomic.Int64
	srv, c, _ := netServer(t, s.Middleware("g")(echoNext(&hits)), false, 0)
	req, _ := http.NewRequest(http.MethodPost, srv.URL, io.MultiReader(strings.NewReader("with-trailer")))
	req.ContentLength = -1
	req.Trailer = http.Header{"X-Sum": {"abc"}}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != "with-trailer" || resp.Header.Get("X-Next-Trailer-X-Sum") != "abc" {
		t.Fatalf("status=%d body=%q trailer=%q", resp.StatusCode, b, resp.Header.Get("X-Next-Trailer-X-Sum"))
	}
}

// A response writer without read-deadline support cannot have its body read
// ended by retirement: the warning is logged, and the request still fails as
// retired, without a guest call, once its client finishes.
func TestRetirementWithoutReadDeadlineSupportIsLogged(t *testing.T) {
	m, hooks := bodyManager(t)
	s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV1, guestEcho)})
	var logs lockedBuffer
	s.plugins["g"].log = slog.New(slog.NewTextHandler(&logs, nil))
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, "/p", pr)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { s.Handler("g").ServeHTTP(rec, req); close(done) }()
	_, _ = pw.Write([]byte("partial"))
	waitFor(t, "upload preparing", func() bool { return preparing(s.plugins["g"]) == 1 })
	_ = s.Close()
	if !strings.Contains(logs.String(), "read deadline unsupported") {
		t.Fatalf("missing warning; log: %s", logs.String())
	}
	_ = pw.Close()
	<-done
	if reason := hooks.awaitFail(t); reason != bodyFailRetired || rec.Code != 500 {
		t.Fatalf("reason=%s status=%d", reason, rec.Code)
	}
	if n, _, _, panics, _ := hooks.snapshot(); n != 0 || panics != 0 {
		t.Fatalf("invocations=%d panics=%d", n, panics)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}
