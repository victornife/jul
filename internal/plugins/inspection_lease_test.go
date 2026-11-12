// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build wasmplugins

package plugins

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/reqdeadline"
)

var bothABIs = []string{ABIJulV1, ABIJulV2}

// TestRequestLeaseSpansChainAndDownstream: the outermost plugin acquires the
// one request lease, inner plugins (body and non-body) reuse it, a body
// prepared once is charged once, and the charge lives while the downstream
// handler runs. The lease closes when the outermost layer returns.
func TestRequestLeaseSpansChainAndDownstream(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			m, hooks, g, _ := govManager(t, testLimits())
			s := buildSet(t, m, map[string]config.PluginConfig{
				"a": bodyGuestConfig(abi, guestReadContinue),
				"n": bodyGuestConfig(abi, guestContinue),
				"b": bodyGuestConfig(abi, guestReadContinue),
			})
			var seen inspection.Snapshot
			var lease *inspection.Lease
			var got []byte
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen, lease = g.Snapshot(), inspection.LeaseFrom(r.Context())
				got, _ = io.ReadAll(r.Body)
			})
			h := s.Middleware("a")(s.Middleware("n")(s.Middleware("b")(next)))
			body := strings.Repeat("x", 5000)
			rec := serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
			if rec.Code != http.StatusOK || string(got) != body {
				t.Fatalf("status %d, downstream read %d bytes", rec.Code, len(got))
			}
			base := testLimits().BaseChargeBytes
			if seen.ActiveLeases != 1 || lease == nil || seen.Usage[inspection.BaseBytes] != base {
				t.Fatalf("downstream saw leases=%d lease=%v base=%d, want one lease", seen.ActiveLeases, lease != nil, seen.Usage[inspection.BaseBytes])
			}
			// Declared 5000 bytes: one array of 5001, charged once for both readers.
			if seen.Usage[inspection.BufferBytes] != 5001 {
				t.Fatalf("downstream buffer charge = %d, want 5001 (one prepared body)", seen.Usage[inspection.BufferBytes])
			}
			if inv, _, _, _, _ := hooks.snapshot(); inv != 3 {
				t.Fatalf("invocations = %d, want 3", inv)
			}
			assertDrained(t, g)
		})
	}
}

// TestRequestLeaseClosesOnEveryOutcome covers terminal handlers, Stop, and
// guest failure, in both ABIs.
func TestRequestLeaseClosesOnEveryOutcome(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			m, _, g, _ := govManager(t, testLimits())
			s := buildSet(t, m, map[string]config.PluginConfig{
				"echo": bodyGuestConfig(abi, guestEcho),
				"rej":  bodyGuestConfig(abi, guestReject),
				"spin": bodyGuestConfig(abi, guestReadSpin),
			})
			var hits atomic.Int64
			cases := []struct {
				name string
				h    http.Handler
				code int
			}{
				{"handler", s.Handler("echo"), http.StatusOK},
				{"middleware stop", s.Middleware("echo")(echoNext(&hits)), http.StatusOK},
				{"middleware reject", s.Middleware("rej")(echoNext(&hits)), http.StatusForbidden},
				{"guest timeout", s.Middleware("spin")(echoNext(&hits)), http.StatusInternalServerError},
				{"handler timeout", s.Handler("spin"), http.StatusInternalServerError},
			}
			for _, tc := range cases {
				rec := serve(tc.h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("payload")))
				if rec.Code != tc.code {
					t.Fatalf("%s: status %d, want %d", tc.name, rec.Code, tc.code)
				}
				assertDrained(t, g)
			}
			if hits.Load() != 0 {
				t.Fatalf("origin reached %d times on Stop/error paths", hits.Load())
			}
		})
	}
}

// TestLeaseAdmissionOverloadDoesNoWork: with the budget held by another
// request, a new request is refused 503 before its body is read, before any
// instance or guest, and without reaching the origin or a panic metric.
func TestLeaseAdmissionOverloadDoesNoWork(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			m, hooks, g, rec := govManager(t, limitsWith(0))
			s := buildSet(t, m, map[string]config.PluginConfig{
				"r": bodyGuestConfig(abi, guestReadContinue),
				"n": bodyGuestConfig(abi, guestContinue),
			})
			holder, _, _, err := g.AcquireLease(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var hits atomic.Int64
			for _, name := range []string{"r", "n"} {
				body := &countingBody{r: strings.NewReader("abc")}
				r := httptest.NewRequest(http.MethodPost, "/", nil)
				r.Body = body
				got := serve(s.Middleware(name)(echoNext(&hits)), r)
				if got.Code != http.StatusServiceUnavailable || got.Header().Get("Retry-After") != "1" ||
					strings.TrimSpace(got.Body.String()) != inspection.ReasonOverloaded {
					t.Fatalf("%s: %d %q %q", name, got.Code, got.Header().Get("Retry-After"), got.Body.String())
				}
				if body.n.Load() != 0 {
					t.Fatalf("%s: body read before admission", name)
				}
			}
			inv, _, fails, panics, waits := hooks.snapshot()
			if inv != 0 || panics != 0 || waits != 0 || len(fails) != 0 || hits.Load() != 0 {
				t.Fatalf("refused requests did work: inv=%d panics=%d waits=%d fails=%v origin=%d", inv, panics, waits, fails, hits.Load())
			}
			if rj, _ := rec.snapshot(); rj["bytes/enforce"] != 2 {
				t.Fatalf("rejections = %v", rj)
			}
			_ = holder.Close()
			assertDrained(t, g)
		})
	}
}

// TestBodyChargeOverloadMidRead: the body's growth is refused part way
// through an unknown-length upload; the request fails 503 before the guest
// runs, and the partial buffer is refunded.
func TestBodyChargeOverloadMidRead(t *testing.T) {
	m, hooks, g, _ := govManager(t, limitsWith(4096))
	s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
	var hits atomic.Int64
	r := httptest.NewRequest(http.MethodPost, "/", io.MultiReader(strings.NewReader(strings.Repeat("y", 10000))))
	r.ContentLength = -1
	got := serve(s.Middleware("r")(echoNext(&hits)), r)
	if got.Code != http.StatusServiceUnavailable || got.Header().Get("Retry-After") != "1" {
		t.Fatalf("status %d", got.Code)
	}
	inv, _, fails, panics, _ := hooks.snapshot()
	if inv != 0 || panics != 0 || len(fails) != 0 || hits.Load() != 0 {
		t.Fatalf("inv=%d panics=%d fails=%v origin=%d", inv, panics, fails, hits.Load())
	}
	assertDrained(t, g)
}

// TestOverloadGRPCMapping: a gRPC request refused for overload gets a
// trailers-only UNAVAILABLE, not an HTTP 503.
func TestOverloadGRPCMapping(t *testing.T) {
	m, _, g, _ := govManager(t, limitsWith(0))
	s := buildSet(t, m, map[string]config.PluginConfig{"n": bodyGuestConfig(ABIJulV2, guestContinue)})
	holder, _, _, _ := g.AcquireLease(t.Context())
	defer holder.Close()
	r := httptest.NewRequest(http.MethodPost, "/svc/M", strings.NewReader(""))
	r.ProtoMajor, r.ProtoMinor, r.Proto = 2, 0, "HTTP/2.0"
	r.Header.Set("Content-Type", "application/grpc")
	got := serve(s.Middleware("n")(http.NotFoundHandler()), r)
	if got.Code != http.StatusOK || got.Header().Get("Grpc-Status") != "14" {
		t.Fatalf("status %d grpc-status %q", got.Code, got.Header().Get("Grpc-Status"))
	}
}

// TestObserveMatchesEnforceDecisions runs the same fixed governor states
// under both modes: observe records exactly the would-reject decisions that
// enforce turns into 503, and otherwise serves the request unchanged.
func TestObserveMatchesEnforceDecisions(t *testing.T) {
	type outcome struct {
		code   int
		reject map[string]int
	}
	run := func(mode inspection.Mode, extra uint64, held bool, body string) outcome {
		m, _, g, rec := govManager(t, limitsWith(extra, func(l *inspection.Limits) { l.Mode = mode }))
		s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
		if held {
			l, _, _, _ := g.AcquireLease(t.Context())
			defer l.Close()
		}
		var hits atomic.Int64
		got := serve(s.Middleware("r")(echoNext(&hits)), httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		rj, _ := rec.snapshot()
		return outcome{got.Code, rj}
	}
	states := []struct {
		name  string
		extra uint64
		held  bool
		body  string
	}{
		{"fits", 1 << 20, false, "small"},
		{"base saturated", 0, true, "small"},
		{"body exceeds", 64, false, strings.Repeat("z", 1000)},
	}
	for _, st := range states {
		enf := run(inspection.Enforce, st.extra, st.held, st.body)
		obs := run(inspection.Observe, st.extra, st.held, st.body)
		enfCount, obsCount := 0, 0
		for k, v := range enf.reject {
			if !strings.HasSuffix(k, "/enforce") {
				t.Fatalf("%s: enforce recorded %s", st.name, k)
			}
			enfCount += v
		}
		for k, v := range obs.reject {
			if !strings.HasSuffix(k, "/observe") {
				t.Fatalf("%s: observe recorded %s", st.name, k)
			}
			obsCount += v
		}
		if obs.code != http.StatusOK {
			t.Fatalf("%s: observe status %d, want 200", st.name, obs.code)
		}
		wantEnf := http.StatusOK
		if enfCount > 0 {
			wantEnf = http.StatusServiceUnavailable
		}
		if enf.code != wantEnf {
			t.Fatalf("%s: enforce status %d with %d rejections", st.name, enf.code, enfCount)
		}
		// Enforce stops at its first refusal; observe records the same first
		// decision and continues, possibly recording later would-rejects.
		if (enfCount > 0) != (obsCount > 0) || obsCount < enfCount {
			t.Fatalf("%s: enforce rejections %v, observe %v", st.name, enf.reject, obs.reject)
		}
	}
}

// deadlineWriter is a ResponseWriter supporting SetReadDeadline: a past
// deadline fails the request body's pending and future reads, as the
// transports do.
type deadlineWriter struct {
	*httptest.ResponseRecorder
	mu   sync.Mutex
	body *stallBody
	sets []time.Time
}

func (w *deadlineWriter) SetReadDeadline(t time.Time) error {
	w.mu.Lock()
	w.sets = append(w.sets, t)
	w.mu.Unlock()
	if !t.IsZero() && !t.After(time.Now()) {
		w.body.fail(os.ErrDeadlineExceeded)
	}
	return nil
}

// stallBody delivers its data, then blocks until released or failed.
type stallBody struct {
	data    *bytes.Reader
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	err     error
	failed  chan struct{}
}

func newStallBody(data string) *stallBody {
	return &stallBody{data: bytes.NewReader([]byte(data)), release: make(chan struct{}), failed: make(chan struct{})}
}

func (b *stallBody) fail(err error) {
	b.once.Do(func() {
		b.mu.Lock()
		b.err = err
		b.mu.Unlock()
		close(b.failed)
	})
}

func (b *stallBody) Read(p []byte) (int, error) {
	if b.data.Len() > 0 {
		return b.data.Read(p)
	}
	select {
	case <-b.release:
		return 0, io.EOF
	case <-b.failed:
		b.mu.Lock()
		defer b.mu.Unlock()
		return 0, b.err
	}
}

func (b *stallBody) Close() error { return nil }

// TestBodyDeadlineOutcomes: an enforced body_timeout aborts a stalled upload
// with 408 (H1 Connection: close) before any guest runs; observe lets it
// finish and counts would_timeout once; 0s arms nothing; and a writer
// without read-deadline support fails closed only in enforce mode.
func TestBodyDeadlineOutcomes(t *testing.T) {
	const timeout = 50 * time.Millisecond
	type tc struct {
		name      string
		mode      inspection.Mode
		timeout   time.Duration
		supported bool
		code      int
		metrics   []string
	}
	cases := []tc{
		{"enforce timeout", inspection.Enforce, timeout, true, http.StatusRequestTimeout, []string{"h1/timeout"}},
		{"observe would_timeout", inspection.Observe, timeout, true, http.StatusOK, []string{"h1/would_timeout"}},
		{"disabled", inspection.Enforce, 0, true, http.StatusOK, nil},
		{"enforce unsupported", inspection.Enforce, timeout, false, http.StatusInternalServerError, []string{"h1/unsupported"}},
		// Observe counts the missing capability and, independently, the read
		// that outlasted the unapplied body_timeout (ADR 0032 observe table).
		{"observe unsupported", inspection.Observe, timeout, false, http.StatusOK, []string{"h1/unsupported", "h1/would_timeout"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, hooks, g, rec := govManager(t, limitsWith(1<<20, func(l *inspection.Limits) {
				l.Mode, l.BodyTimeout = c.mode, c.timeout
			}))
			s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
			var hits atomic.Int64
			body := newStallBody("part")
			r := httptest.NewRequest(http.MethodPost, "/", nil)
			r.Body, r.ContentLength = body, -1
			var w http.ResponseWriter = httptest.NewRecorder()
			dw := &deadlineWriter{ResponseRecorder: w.(*httptest.ResponseRecorder), body: body}
			if c.supported {
				w = dw
			}
			// A supported writer's upload stalls until the deadline aborts it;
			// otherwise it completes after the timeout has passed.
			if c.code != http.StatusRequestTimeout {
				go func() { time.Sleep(2 * timeout); close(body.release) }()
			}
			s.Middleware("r")(echoNext(&hits)).ServeHTTP(w, r)
			if dw.Code != c.code {
				t.Fatalf("status %d, want %d", dw.Code, c.code)
			}
			_, timeouts := rec.snapshot()
			want := map[string]int{}
			for _, k := range c.metrics {
				want[k] = 1
			}
			if !mapsEqual(timeouts, want) {
				t.Fatalf("body timeout metrics %v, want %v", timeouts, want)
			}
			inv, _, fails, panics, _ := hooks.snapshot()
			if c.code == http.StatusOK {
				if inv != 1 || hits.Load() != 1 {
					t.Fatalf("served request: inv=%d origin=%d", inv, hits.Load())
				}
			} else if inv != 0 || hits.Load() != 0 || panics != 0 {
				t.Fatalf("refused request did work: inv=%d origin=%d panics=%d", inv, hits.Load(), panics)
			}
			if c.code == http.StatusRequestTimeout {
				if dw.Header().Get("Connection") != "close" || len(fails) != 0 {
					t.Fatalf("408: Connection=%q fails=%v", dw.Header().Get("Connection"), fails)
				}
			}
			// Inspection never sets a future deadline, so a server deadline
			// can never be extended or cleared by it.
			for _, d := range dw.sets {
				if d.IsZero() || d.After(time.Now()) {
					t.Fatalf("inspection set deadline %v", d)
				}
			}
			assertDrained(t, g)
		})
	}
}

// TestRetirementAbortIsStickyAndAttributed: retiring a plugin while its
// upload stalls aborts the read through the request's deadline owner; the
// request fails as retired, not as a body timeout, and the abort survives
// the owner's later phases.
func TestRetirementAbortIsStickyAndAttributed(t *testing.T) {
	m, hooks, g, rec := govManager(t, limitsWith(1<<20, func(l *inspection.Limits) { l.BodyTimeout = time.Hour }))
	s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
	body := newStallBody("part")
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.Body, r.ContentLength = body, -1
	dw := &deadlineWriter{ResponseRecorder: httptest.NewRecorder(), body: body}
	owner := reqdeadline.New(dw, reqdeadline.ProtocolOf(r))
	r = r.WithContext(reqdeadline.With(r.Context(), owner))
	var hits atomic.Int64
	done := make(chan struct{})
	go func() { defer close(done); s.Middleware("r")(echoNext(&hits)).ServeHTTP(dw, r) }()
	waitFor(t, "preparing", func() bool { return preparing(s.plugins["r"]) == 1 })
	s.Close()
	if reason := hooks.awaitFail(t); reason != bodyFailRetired {
		t.Fatalf("fail reason %q, want retired", reason)
	}
	<-done
	owner.End()
	if dw.Code != http.StatusInternalServerError || !owner.Aborted() || hits.Load() != 0 {
		t.Fatalf("status %d aborted=%v origin=%d", dw.Code, owner.Aborted(), hits.Load())
	}
	if _, timeouts := rec.snapshot(); len(timeouts) != 0 {
		t.Fatalf("retirement counted as body timeout: %v", timeouts)
	}
	assertDrained(t, g)
}

// TestHostOverloadWinsOverProvokedTrap: a guest that ignores a refused host
// allocation and traps still fails as overload: 503, invocation error, no
// panic, nothing flushed.
func TestHostOverloadWinsOverProvokedTrap(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			g := hostGuest{abi: abi, fns: []hostFn{hfWriteBody, hfStatus}, pages: 3,
				body: seq(call(1, 201), call(0, 0, 100000), []byte{opUnreachable})}
			m, hooks, gov, _ := govManager(t, limitsWith(1000))
			s := buildSet(t, m, map[string]config.PluginConfig{"w": g.config(func(pc *config.PluginConfig) {
				pc.MaxResponseBody = config.Size(1 << 20)
			})})
			got := serve(s.Handler("w"), httptest.NewRequest(http.MethodGet, "/", nil))
			if got.Code != http.StatusServiceUnavailable || got.Header().Get("Retry-After") != "1" {
				t.Fatalf("status %d", got.Code)
			}
			_, results, _, panics, _ := hooks.snapshot()
			if panics != 0 || results["error"] != 1 {
				t.Fatalf("panics=%d results=%v", panics, results)
			}
			assertDrained(t, gov)
		})
	}
}

// TestGeneratedResponseChargedAndBounded: write_response_body growth is
// charged and released after the flush; a length over max_response_body
// still fails as the plugin's 500 without charging anything.
func TestGeneratedResponseChargedAndBounded(t *testing.T) {
	for _, abi := range bothABIs {
		t.Run(abi, func(t *testing.T) {
			payload := []byte("0123456789")
			ok := hostGuest{abi: abi, fns: []hostFn{hfWriteBody, hfStatus}, data: map[int32][]byte{64: payload},
				body: seq(call(0, 64, 10), call(0, 64, 10), call(0, 64, 10), i32c(0))}
			huge := hostGuest{abi: abi, fns: []hostFn{hfWriteBody, hfStatus},
				body: seq(call(0, 64, -1), i32c(0))}
			m, hooks, gov, rec := govManager(t, limitsWith(1<<20))
			s := buildSet(t, m, map[string]config.PluginConfig{
				"ok":   ok.config(func(pc *config.PluginConfig) { pc.MaxResponseBody = 64 }),
				"huge": huge.config(func(pc *config.PluginConfig) { pc.MaxResponseBody = 64 }),
			})
			got := serve(s.Handler("ok"), httptest.NewRequest(http.MethodGet, "/", nil))
			if got.Code != http.StatusOK || got.Body.String() != strings.Repeat(string(payload), 3) {
				t.Fatalf("ok: %d %q", got.Code, got.Body.String())
			}
			assertDrained(t, gov)
			got = serve(s.Handler("huge"), httptest.NewRequest(http.MethodGet, "/", nil))
			if got.Code != http.StatusInternalServerError {
				t.Fatalf("huge: %d", got.Code)
			}
			if rj, _ := rec.snapshot(); len(rj) != 0 {
				t.Fatalf("an over-limit length reached the governor: %v", rj)
			}
			if _, _, _, panics, _ := hooks.snapshot(); panics != 0 {
				t.Fatalf("panics = %d", panics)
			}
			assertDrained(t, gov)
		})
	}
}

// TestRequestStateOverwriteKeepsOneCharge: repeated set_request_state calls
// hold one state charge; it stays charged while the subscription can reach
// it and is refunded with the request.
func TestRequestStateOverwriteKeepsOneCharge(t *testing.T) {
	state := bytes.Repeat([]byte("s"), 4000)
	g := hostGuest{abi: ABIJulV2, fns: []hostFn{hfState, hfSubscribe}, data: map[int32][]byte{256: state}, respond: true,
		body: seq(call(0, 256, 4000), []byte{opDrop}, call(0, 256, 3000), []byte{opDrop},
			call(0, 256, 4000), []byte{opDrop}, call(1, 0), []byte{opDrop}, i32c(1))}
	// Room for one state only: three simultaneous copies would be refused.
	m, hooks, gov, rec := govManager(t, limitsWith(5000))
	s := buildSet(t, m, map[string]config.PluginConfig{"st": g.config()})
	var during inspection.Snapshot
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = gov.Snapshot()
		w.WriteHeader(http.StatusNoContent)
	})
	h := chainFor(s, next, "st")
	got := serve(h, httptest.NewRequest(http.MethodGet, "/", nil))
	if got.Code != http.StatusNoContent {
		t.Fatalf("status %d", got.Code)
	}
	if during.Usage[inspection.BufferBytes] != 4000 {
		t.Fatalf("state charge during downstream = %d, want 4000", during.Usage[inspection.BufferBytes])
	}
	if rj, _ := rec.snapshot(); len(rj) != 0 {
		t.Fatalf("overwrite refused: %v", rj)
	}
	if _, _, _, panics, _ := hooks.snapshot(); panics != 0 {
		t.Fatal("panic")
	}
	assertDrained(t, gov)
}

// TestResponseReplacementOverloadIsDelivered: a response callback whose
// replacement copy is refused answers through the inspection overload
// response (503, or gRPC UNAVAILABLE), not the plugin failure body.
func TestResponseReplacementOverloadIsDelivered(t *testing.T) {
	for _, grpc := range []bool{false, true} {
		m, _, gov, _ := govManager(t, limitsWith(4096+1024))
		s := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("big"))})
		r := req(http.MethodGet, "/", "X-Mode", "body", "X-Size", "8192")
		if grpc {
			r.ProtoMajor, r.Proto = 2, "HTTP/2.0"
			r.Header.Set("Content-Type", "application/grpc")
		}
		got := serve(chainFor(s, act(200, "origin body"), "p"), r)
		if grpc {
			if got.Header().Get("Grpc-Status") != "14" {
				t.Fatalf("grpc: %d %v", got.Code, got.Header())
			}
		} else if got.Code != http.StatusServiceUnavailable || got.Header().Get("Retry-After") != "1" ||
			strings.TrimSpace(got.Body.String()) != inspection.ReasonOverloaded {
			t.Fatalf("http: %d %q", got.Code, got.Body.String())
		}
		assertDrained(t, gov)
	}
	// Within budget the replacement is delivered and refunded.
	m, _, gov, _ := govManager(t, limitsWith(1<<20))
	s := buildSet(t, m, map[string]config.PluginConfig{"p": v2cfg(withOp("upper"))})
	got := serve(chainFor(s, act(200, "origin body"), "p"), req(http.MethodGet, "/", "X-Mode", "body"))
	if got.Code != 200 || got.Body.String() != "ORIGIN BODY" {
		t.Fatalf("upper: %d %q", got.Code, got.Body.String())
	}
	assertDrained(t, gov)
}

// TestMissingLeaseIsAnInvariantFailure: the runtime refuses to run a guest
// for a request without a lease rather than run it ungoverned.
func TestMissingLeaseIsAnInvariantFailure(t *testing.T) {
	m, hooks, _, _ := govManager(t, testLimits())
	s := buildSet(t, m, map[string]config.PluginConfig{"n": bodyGuestConfig(ABIJulV2, guestContinue)})
	if _, _, err := s.plugins["n"].invoke(t.Context(), httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil)); !errors.Is(err, errNoLease) {
		t.Fatalf("err = %v", err)
	}
	if inv, _, _, _, _ := hooks.snapshot(); inv != 0 {
		t.Fatal("guest ran without a lease")
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// TestInstanceWaitHoldsLeaseAndBody: a request whose body has arrived waits
// (finitely) for the only busy instance while holding its lease and its
// charged body; every way the wait ends — acquired, client cancel, wait
// timeout, retirement — releases both, with no slot leak.
func TestInstanceWaitHoldsLeaseAndBody(t *testing.T) {
	for _, end := range []string{"acquired", "cancel", "timeout", "retire"} {
		t.Run(end, func(t *testing.T) {
			m, hooks, g, _ := govManager(t, testLimits())
			s := buildSet(t, m, map[string]config.PluginConfig{"g": bodyGuestConfig(ABIJulV2, guestEcho, func(pc *config.PluginConfig) {
				pc.MaxInstances = 1
				pc.Timeout = config.Duration(300 * time.Millisecond)
			})})
			p := s.plugins["g"]
			busy, err := p.acquire(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("abc")).WithContext(ctx)
			var rec *httptest.ResponseRecorder
			done := make(chan struct{})
			go func() { defer close(done); rec = serve(s.Handler("g"), r) }()
			waitFor(t, "waiting with the body charged", func() bool {
				snap := g.Snapshot()
				return snap.ActiveLeases == 1 && snap.Usage[inspection.BufferBytes] == 4 && preparing(p) == 0
			})
			switch end {
			case "acquired":
				p.release(busy)
			case "cancel":
				cancel()
			case "retire":
				s.Close()
			}
			<-done
			if end != "acquired" && end != "retire" {
				p.release(busy)
			}
			_, _, _, panics, waits := hooks.snapshot()
			switch end {
			case "acquired":
				if rec.Code != http.StatusOK || rec.Body.String() != "abc" || waits != 1 {
					t.Fatalf("acquired: %d %q waits=%d", rec.Code, rec.Body.String(), waits)
				}
			default:
				if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || panics != 0 {
					t.Fatalf("%s: %d panics=%d", end, rec.Code, panics)
				}
			}
			assertDrained(t, g)
			if end != "retire" {
				assertNoSlotLeak(t, p)
			}
		})
	}
}

// TestPublishedLimitsGovernNewDecisionsOnly: a smaller budget published
// while a request holds its prepared body refuses new requests but never
// revokes the admitted one's charges; raising it, or switching to observe,
// admits again. The governor and its counters are the same throughout.
func TestPublishedLimitsGovernNewDecisionsOnly(t *testing.T) {
	m, _, g, rec := govManager(t, limitsWith(1<<20))
	s := buildSet(t, m, map[string]config.PluginConfig{"r": bodyGuestConfig(ABIJulV2, guestReadContinue)})
	entered, release := make(chan struct{}), make(chan struct{})
	var blocking atomic.Bool
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if blocking.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		_, _ = w.Write(b)
	})
	h := s.Middleware("r")(next)
	var first *httptest.ResponseRecorder
	done := make(chan struct{})
	go func() {
		defer close(done)
		first = serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 2000))))
	}()
	<-entered
	held := g.Snapshot()
	if err := g.PublishLimits(limitsWith(100)); err != nil {
		t.Fatal(err)
	}
	if got := g.Snapshot(); got.Usage != held.Usage || got.ActiveLeases != 1 {
		t.Fatalf("publishing revoked charges: %+v -> %+v", held, got)
	}
	if got := serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("b"))); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("new request under the smaller budget: %d", got.Code)
	}
	obs := limitsWith(100, func(l *inspection.Limits) { l.Mode = inspection.Observe })
	if err := g.PublishLimits(obs); err != nil {
		t.Fatal(err)
	}
	if got := serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("c"))); got.Code != http.StatusOK {
		t.Fatalf("observe: %d", got.Code)
	}
	close(release)
	<-done
	if first.Code != http.StatusOK || first.Body.Len() != 2000 {
		t.Fatalf("admitted request: %d %d", first.Code, first.Body.Len())
	}
	if err := g.PublishLimits(limitsWith(1 << 20)); err != nil {
		t.Fatal(err)
	}
	if got := serve(h, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("d"))); got.Code != http.StatusOK {
		t.Fatalf("raised budget: %d", got.Code)
	}
	if rj, _ := rec.snapshot(); rj["bytes/enforce"] != 1 || rj["bytes/observe"] == 0 {
		t.Fatalf("rejections %v", rj)
	}
	assertDrained(t, g)
}

// TestConcurrentChargesStayWithinBudget: concurrent mixed uploads on a
// tight enforce budget never take live usage past the budget, each request
// is served exactly or refused with 503, and every charge drains.
func TestConcurrentChargesStayWithinBudget(t *testing.T) {
	lim := limitsWith(64 << 10)
	m, hooks, g, _ := govManager(t, lim)
	s := buildSet(t, m, map[string]config.PluginConfig{
		"a": bodyGuestConfig(ABIJulV2, guestReadContinue),
		"b": bodyGuestConfig(ABIJulV1, guestReadContinue),
	})
	var hits atomic.Int64
	h := s.Middleware("a")(s.Middleware("b")(echoNext(&hits)))
	stop := make(chan struct{})
	var peak atomic.Uint64
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			snap := g.Snapshot()
			peak.Store(max(peak.Load(), snap.Usage[inspection.BaseBytes]+snap.Usage[inspection.BufferBytes]))
		}
	}()
	var wg sync.WaitGroup
	var served, refused atomic.Int64
	for i := range 64 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body := strings.Repeat("c", 1000+(i*997)%20000)
			paced := pacedBody([]byte(body), 4096, time.Millisecond)
			r := httptest.NewRequest(http.MethodPost, "/", paced)
			if i%2 == 0 {
				r.ContentLength = -1
			}
			got := serve(h, r)
			_ = paced.(io.Closer).Close() // a refused upload is never drained
			switch {
			case got.Code == http.StatusOK && got.Body.String() == body:
				served.Add(1)
			case got.Code == http.StatusServiceUnavailable:
				refused.Add(1)
			default:
				t.Errorf("request %d: %d (%d bytes)", i, got.Code, got.Body.Len())
			}
		}()
	}
	wg.Wait()
	close(stop)
	if p := peak.Load(); p > lim.MaxBytes {
		t.Fatalf("peak usage %d over budget %d", p, lim.MaxBytes)
	}
	if served.Load() == 0 || refused.Load() == 0 {
		t.Logf("served=%d refused=%d (contention-dependent)", served.Load(), refused.Load())
	}
	if _, _, _, panics, _ := hooks.snapshot(); panics != 0 || hits.Load() != served.Load() {
		t.Fatalf("panics=%d origin=%d served=%d", panics, hits.Load(), served.Load())
	}
	assertDrained(t, g)
}
