// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
	"jul/internal/middleware"
	"jul/internal/redact"
	"jul/internal/versionid"
)

type outcomeTestHarness struct {
	fw             *Firewall
	inv            *InvestigationStore
	mu             sync.Mutex
	loggedEvents   []string
	txOutcomes     []string
	invCollections []string
}

func newOutcomeTestHarness(t *testing.T, cfg config.WAFConfig) *outcomeTestHarness {
	t.Helper()
	applyTestDefaults(&cfg)
	inv := NewInvestigationStore(64, 15*time.Minute)
	h := &outcomeTestHarness{inv: inv}
	fw, err := New(context.Background(), cfg, Options{
		Versions:      versionid.Process(),
		Investigation: inv,
		Hooks: Hooks{
			OnEvent: func(action, ruleID string) {
				h.mu.Lock()
				h.loggedEvents = append(h.loggedEvents, action+":"+ruleID)
				h.mu.Unlock()
			},
			OnTransaction: func(mode, outcome string) {
				h.mu.Lock()
				h.txOutcomes = append(h.txOutcomes, mode+":"+outcome)
				h.mu.Unlock()
			},
			OnInvestigationEvent: func(result string) {
				h.mu.Lock()
				h.invCollections = append(h.invCollections, result)
				h.mu.Unlock()
			},
		},
	})
	if err != nil {
		t.Fatalf("New Firewall: %v", err)
	}
	h.fw = fw
	return h
}

func (h *outcomeTestHarness) snapshot() ([]string, []string, []string, InvestigationSnapshot) {
	h.mu.Lock()
	ev := append([]string(nil), h.loggedEvents...)
	tx := append([]string(nil), h.txOutcomes...)
	ic := append([]string(nil), h.invCollections...)
	h.mu.Unlock()
	return ev, tx, ic, h.inv.Snapshot(time.Now().UTC(), InvestigationFilter{})
}

func TestWAFTransactionOutcomesTable(t *testing.T) {
	t.Run("clean_request_in_block_mode", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule ARGS:attack "@streq 1" "id:1001,phase:1,deny,status:403,log,msg:'blocked'"`,
		})
		var originHits atomic.Int64
		handler := th.fw.RouteMiddleware(RouteAttribution{RouteID: "r-clean", Scope: "global"}, func() uint64 { return 7 })(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				// Attempt to spoof X-Jul-WAF-Event from origin; WAF must strip it.
				w.Header().Set(HeaderWAFEvent, "wev_forged_by_origin")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "clean-ok")
			}),
		)

		req := httptest.NewRequest(http.MethodGet, "http://app.example.test/hello?name=alice", nil)
		req.Header.Set(HeaderWAFEvent, "wev_forged_by_client")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK || originHits.Load() != 1 {
			t.Fatalf("status=%d originHits=%d, want 200, 1", rr.Code, originHits.Load())
		}
		if got := rr.Header().Get(HeaderWAFEvent); got != "" {
			t.Fatalf("clean response must not carry %s, got %q", HeaderWAFEvent, got)
		}
		ev, tx, ic, snap := th.snapshot()
		if len(ev) != 0 || len(ic) != 0 || snap.Retained != 0 {
			t.Fatalf("unexpected events for clean request: ev=%v ic=%v retained=%d", ev, ic, snap.Retained)
		}
		if len(tx) != 1 || tx[0] != "block:clean" {
			t.Fatalf("txOutcomes = %v, want [block:clean]", tx)
		}
	})

	t.Run("non_disruptive_logged_match_in_block_mode_is_matched_observed", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule ARGS:comment "@contains select" "id:1002,phase:2,pass,log,msg:'observed SQL word'"`,
		})
		var originHits atomic.Int64
		handler := th.fw.RouteMiddleware(RouteAttribution{RouteID: "r-obs", Scope: "override"}, func() uint64 { return 9 })(
			http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.WriteHeader(http.StatusCreated)
			}),
		)

		req := httptest.NewRequest(http.MethodPost, "http://app.example.test/comments", strings.NewReader("comment=please+select+an+option"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusCreated || originHits.Load() != 1 {
			t.Fatalf("status=%d originHits=%d, want 201, 1", rr.Code, originHits.Load())
		}
		if rr.Header().Get(HeaderWAFEvent) != "" {
			t.Fatalf("non-interrupted response must not emit %s", HeaderWAFEvent)
		}
		ev, tx, _, snap := th.snapshot()
		if len(ev) != 1 || ev[0] != "block:1002" {
			t.Fatalf("loggedEvents = %v, want [block:1002] (preserving jul_waf_events_total semantics)", ev)
		}
		if len(tx) != 1 || tx[0] != "block:matched_observed" {
			t.Fatalf("txOutcomes = %v, want [block:matched_observed]", tx)
		}
		if len(snap.Events) != 1 {
			t.Fatalf("expected 1 investigation event, got %d", len(snap.Events))
		}
		rec := snap.Events[0]
		if rec.Outcome != OutcomeMatchedObserved || rec.WouldInterrupt || !rec.OriginReached || rec.OriginStatus != 201 || rec.ClientStatus != 201 {
			t.Fatalf("unexpected investigation record: %+v", rec)
		}
		if len(rec.MatchedRules) != 1 || rec.MatchedRules[0].RuleID != 1002 || len(rec.MatchedRules[0].MatchedTargets) == 0 || rec.MatchedRules[0].MatchedTargets[0] != "ARGS:comment" {
			t.Fatalf("unexpected matched rules: %+v", rec.MatchedRules)
		}
	})

	t.Run("detect_mode_disruptive_match_records_would_interrupt", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "detect",
			InlineRules: `SecRule ARGS:comment "@contains attack" "id:1003,phase:2,deny,status:403,log,msg:'would block'"`,
		})
		var originHits atomic.Int64
		handler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		req := httptest.NewRequest(http.MethodPost, "http://app.example.test/post", strings.NewReader("comment=attack"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK || originHits.Load() != 1 {
			t.Fatalf("detect mode status=%d originHits=%d, want 200, 1", rr.Code, originHits.Load())
		}
		_, tx, _, snap := th.snapshot()
		if len(tx) != 1 || tx[0] != "detect:matched_observed" {
			t.Fatalf("txOutcomes = %v, want [detect:matched_observed]", tx)
		}
		if len(snap.Events) != 1 || !snap.Events[0].WouldInterrupt || snap.Events[0].InterruptedRuleID != 1003 {
			t.Fatalf("expected WouldInterrupt=true and InterruptedRuleID=1003, got %+v", snap.Events)
		}
	})

	t.Run("nolog_interruption_and_drop_action_are_captured", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			BlockStatus: 403,
			InlineRules: strings.Join([]string{
				`SecRule REQUEST_URI "@beginsWith /silent-deny" "id:1004,phase:1,deny,status:403,nolog"`,
				`SecRule REQUEST_URI "@beginsWith /silent-drop" "id:1005,phase:1,drop,nolog"`,
			}, "\n"),
		})
		var originHits atomic.Int64
		handler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		for _, path := range []string{"/silent-deny", "/silent-drop"} {
			req := httptest.NewRequest(http.MethodGet, "http://app.example.test"+path, nil)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403", path, rr.Code)
			}
			supportID := rr.Header().Get(HeaderWAFEvent)
			if !strings.HasPrefix(supportID, "wev_") {
				t.Fatalf("%s missing %s header: %q", path, HeaderWAFEvent, supportID)
			}
		}
		if originHits.Load() != 0 {
			t.Fatalf("originHits = %d, want 0", originHits.Load())
		}
		ev, tx, _, snap := th.snapshot()
		if len(ev) != 0 {
			t.Fatalf("nolog rules must not increment jul_waf_events_total, got %v", ev)
		}
		if len(tx) != 2 || tx[0] != "block:interrupted_request" || tx[1] != "block:interrupted_request" {
			t.Fatalf("txOutcomes = %v, want 2 block:interrupted_request", tx)
		}
		if len(snap.Events) != 2 {
			t.Fatalf("expected 2 investigation events, got %d", len(snap.Events))
		}
		if snap.Events[0].InterruptedRuleID != 1005 || snap.Events[0].InterruptedAction != "drop" || !snap.Events[0].SupportHeaderEmitted {
			t.Fatalf("unexpected drop event: %+v", snap.Events[0])
		}
		if snap.Events[1].InterruptedRuleID != 1004 || len(snap.Events[1].MatchedRules) != 1 || snap.Events[1].MatchedRules[0].Logged {
			t.Fatalf("unexpected nolog deny event: %+v", snap.Events[1])
		}
	})

	t.Run("origin_403_is_never_attributed_to_waf_interruption", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:    true,
			Mode:       "block",
			CRSEnabled: true,
		})
		var originHits atomic.Int64
		handler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originHits.Add(1)
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, "application-rbac-denied")
		}))

		req := httptest.NewRequest(http.MethodGet, "http://app.example.test/admin/users", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden || originHits.Load() != 1 {
			t.Fatalf("status=%d originHits=%d", rr.Code, originHits.Load())
		}
		if rr.Header().Get(HeaderWAFEvent) != "" {
			t.Fatalf("origin 403 must not emit %s", HeaderWAFEvent)
		}
		_, tx, _, snap := th.snapshot()
		if len(tx) != 1 || tx[0] != "block:clean" || snap.Retained != 0 {
			t.Fatalf("origin 403 must record block:clean and 0 WAF events, got tx=%v retained=%d", tx, snap.Retained)
		}
	})
}

func TestWAFRequestVsResponseInterruptionAndBodyErrors(t *testing.T) {
	t.Run("response_header_and_response_body_interruption_after_origin", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			BlockStatus:       403,
			ResponseBodyCheck: true,
			InlineRules: strings.Join([]string{
				`SecResponseBodyAccess On`,
				`SecResponseBodyMimeType text/plain`,
				`SecRule RESPONSE_HEADERS:X-Leak-Header "@streq leak" "id:2001,phase:3,deny,status:403,log"`,
				`SecRule RESPONSE_BODY "@contains SECRET_STACK_TRACE" "id:2002,phase:4,deny,status:403,log"`,
			}, "\n"),
		})

		var originHits atomic.Int64
		handler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			originHits.Add(1)
			if r.URL.Path == "/header-leak" {
				w.Header().Set("X-Leak-Header", "leak")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "should-not-be-sent")
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "prefix SECRET_STACK_TRACE suffix")
		}))

		// Phase 3 response header interruption.
		req1 := httptest.NewRequest(http.MethodGet, "http://app.example.test/header-leak", nil)
		rr1 := httptest.NewRecorder()
		handler.ServeHTTP(rr1, req1)
		if rr1.Code != http.StatusForbidden || rr1.Body.Len() != 0 {
			t.Fatalf("phase 3 block = %d body=%q", rr1.Code, rr1.Body.String())
		}
		evID1 := rr1.Header().Get(HeaderWAFEvent)
		if !strings.HasPrefix(evID1, "wev_") {
			t.Fatalf("phase 3 block missing %s", HeaderWAFEvent)
		}

		// Phase 4 response body interruption (buffered before flush -> 0 bytes delivered).
		req2 := httptest.NewRequest(http.MethodGet, "http://app.example.test/body-leak", nil)
		rr2 := httptest.NewRecorder()
		handler.ServeHTTP(rr2, req2)
		if rr2.Code != http.StatusForbidden || strings.Contains(rr2.Body.String(), "SECRET_STACK_TRACE") {
			t.Fatalf("phase 4 block = %d body=%q", rr2.Code, rr2.Body.String())
		}
		evID2 := rr2.Header().Get(HeaderWAFEvent)
		if !strings.HasPrefix(evID2, "wev_") {
			t.Fatalf("phase 4 block missing %s", HeaderWAFEvent)
		}

		// Clean buffered response body passes through writeBufferedResponseBodyToDownstream, ReadFrom, and Flush.
		cleanHandler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			if rf, ok := w.(io.ReaderFrom); ok {
				_, _ = rf.ReadFrom(strings.NewReader("clean-buffered-response-body"))
			}
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}))
		req3 := httptest.NewRequest(http.MethodGet, "http://app.example.test/clean-resp", nil)
		rr3 := httptest.NewRecorder()
		cleanHandler.ServeHTTP(rr3, req3)
		if rr3.Code != http.StatusOK || rr3.Body.String() != "clean-buffered-response-body" {
			t.Fatalf("clean buffered response = %d %q", rr3.Code, rr3.Body.String())
		}
		if len(th.fw.RawPolicyDigest()) != 64 {
			t.Fatalf("RawPolicyDigest = %q, want 64-char hex", th.fw.RawPolicyDigest())
		}

		if originHits.Load() != 2 {
			t.Fatalf("originHits = %d, want 2 (origin ran before response-phase intervention)", originHits.Load())
		}
		_, tx, _, snap := th.snapshot()
		if len(tx) != 3 || tx[0] != "block:interrupted_response" || tx[1] != "block:interrupted_response" || tx[2] != "block:clean" {
			t.Fatalf("txOutcomes = %v", tx)
		}
		for _, ev := range snap.Events {
			if ev.Outcome != OutcomeInterruptedResponse || !ev.OriginReached || ev.OriginStatus != 200 || ev.ClientStatus != 403 || ev.PartialResponseDelivered {
				t.Fatalf("unexpected response interruption event: %+v", ev)
			}
		}
	})

	t.Run("partial_response_delivery_and_websocket_hijack", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			ResponseBodyCheck: true,
			InlineRules: strings.Join([]string{
				`SecResponseBodyAccess On`,
				`SecResponseBodyMimeType text/plain`,
				`SecResponseBodyLimit 16`,
				`SecResponseBodyLimitAction ProcessPartial`,
				`SecRule REQUEST_HEADERS:X-Observe "@streq 1" "id:2099,phase:1,pass,log"`,
			}, "\n"),
		})
		handler := th.fw.RouteMiddleware(RouteAttribution{RouteID: "r-partial", ServerNames: []string{"app.example.test"}}, func() uint64 { return 5 })(
			http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/ws" {
					w.WriteHeader(http.StatusSwitchingProtocols)
					if hj, ok := w.(http.Hijacker); ok {
						conn, _, err := hj.Hijack()
						if err == nil && conn != nil {
							_ = conn.Close()
						}
					}
					return
				}
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("0123456789abcdef-beyond-16-byte-limit"))
			}),
		)

		// ProcessPartial response exceeding 16 bytes streams to downstream.
		req := httptest.NewRequest(http.MethodGet, "http://app.example.test:8080/partial", nil)
		req.Header.Set("X-Observe", "1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || rr.Body.String() != "0123456789abcdef-beyond-16-byte-limit" {
			t.Fatalf("partial stream = %d %q", rr.Code, rr.Body.String())
		}

		// WebSocket upgrade sets Hijacked=true on the transaction event (tested synchronously via hijackerRecorder).
		hjRec := &hijackerResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
		wsReq := httptest.NewRequest(http.MethodGet, "http://app.example.test/ws", nil)
		wsReq.Header.Set("X-Observe", "1")
		wsReq.TransferEncoding = []string{"chunked"}
		handler.ServeHTTP(hjRec, wsReq)
		_, _, _, snap := th.snapshot()
		if len(snap.Events) < 2 || !snap.Events[0].Hijacked {
			t.Fatalf("expected newest event Hijacked=true, got %+v", snap.Events)
		}

		// Test failed Hijack error propagation.
		hjErrRec := &hijackerResponseRecorder{ResponseRecorder: httptest.NewRecorder(), hijackErr: errors.New("hijack denied")}
		handler.ServeHTTP(hjErrRec, httptest.NewRequest(http.MethodGet, "http://app.example.test/ws", nil))

		// Test Pusher + Hijacker response writer wrapping, Flush before WriteHeader, and WriteResponseBody Reject interruption.
		thReject := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:           true,
			Mode:              "block",
			ResponseBodyCheck: true,
			InlineRules: strings.Join([]string{
				`SecResponseBodyAccess On`,
				`SecResponseBodyMimeType text/plain`,
				`SecResponseBodyLimit 8`,
				`SecResponseBodyLimitAction Reject`,
			}, "\n"),
		})
		rejectHandler := thReject.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/flush-first" {
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("response-exceeding-8-bytes"))
			// Second write after interruption is a no-op returning len(b).
			_, _ = w.Write([]byte("extra"))
		}))
		rrRej := &pusherHijackerRecorder{hijackerResponseRecorder: hijackerResponseRecorder{ResponseRecorder: httptest.NewRecorder()}}
		rejectHandler.ServeHTTP(rrRej, httptest.NewRequest(http.MethodGet, "http://app.example.test/resp-limit", nil))
		if rrRej.Code != http.StatusInternalServerError {
			t.Fatalf("response body limit reject status = %d, want 500", rrRej.Code)
		}
		rrFlush := &pusherOnlyRecorder{ResponseRecorder: httptest.NewRecorder()}
		rejectHandler.ServeHTTP(rrFlush, httptest.NewRequest(http.MethodGet, "http://app.example.test/flush-first", nil))
		if rrFlush.Code != http.StatusOK {
			t.Fatalf("flush-first status = %d, want 200", rrFlush.Code)
		}

		// Helper unit checks for helper edge branches.
		if (*Firewall)(nil).RawPolicyDigest() != "" {
			t.Fatal("nil Firewall RawPolicyDigest must be empty")
		}
		if interruptionStatus(nil, 0) != http.StatusForbidden {
			t.Fatal("interruptionStatus(nil, 0) must default to 403")
		}
		if isBodyLimitInterruption(nil) {
			t.Fatal("isBodyLimitInterruption(nil) must be false")
		}
		if classifyRuleSource(900000, "") != "generated" || classifyRuleSource(1, "/etc/rules.conf") != "external" {
			t.Fatal("unexpected classifyRuleSource")
		}
		if requestTargetURI(nil) != "/" {
			t.Fatal("requestTargetURI(nil) must be /")
		}

		// Direct unit check of extractMatchedRules when interruptedID was not in matched slice.
		synthRules, synthTotal, _ := extractMatchedRules(nil, &types.Interruption{RuleID: 4444, Action: "deny", Status: 403}, nil)
		if synthTotal != 1 || len(synthRules) != 1 || synthRules[0].RuleID != 4444 {
			t.Fatalf("unexpected synthesized rule: %+v", synthRules)
		}

		// Test implicit 200 (handler writes neither header nor body) when phase 3 interrupts on status 200,
		// and Flush after WriteHeader on a non-buffered response.
		thImplicit := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule RESPONSE_STATUS "@streq 200" "id:2088,phase:3,deny,status:403,log"`,
		})
		implicitHandler := thImplicit.fw.Middleware()(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
			// Return without calling WriteHeader or Write -> finishResponse triggers WriteHeader(200) -> interrupted in phase 3.
		}))
		rrImp := httptest.NewRecorder()
		implicitHandler.ServeHTTP(rrImp, httptest.NewRequest(http.MethodGet, "http://app.example.test/implicit", nil))
		if rrImp.Code != http.StatusForbidden {
			t.Fatalf("implicit 200 phase 3 block status = %d, want 403", rrImp.Code)
		}

		// Non-buffered response calling WriteHeader, WriteHeader again (no-op), Write, and Flush.
		thStream := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:     true,
			Mode:        "block",
			InlineRules: `SecRule ARGS:nope "@streq 1" "id:2089,phase:1,deny,status:403"`,
		})
		streamHandler := thStream.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			w.WriteHeader(http.StatusOK) // duplicate WriteHeader is ignored
			_, _ = w.Write([]byte("chunk"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}))
		rrStr := httptest.NewRecorder()
		streamHandler.ServeHTTP(rrStr, httptest.NewRequest(http.MethodGet, "http://app.example.test/stream", nil))
		if rrStr.Code != http.StatusAccepted || !rrStr.Flushed {
			t.Fatalf("stream response = %d flushed=%v", rrStr.Code, rrStr.Flushed)
		}

		// Real SSE header-flush-before-first-write test over TCP: an SSE origin that
		// flushes headers and waits before writing its first body frame must not stall
		// connection establishment.
		clientGotHeaders := make(chan struct{})
		sseLiveHandler := thStream.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
			select {
			case <-clientGotHeaders:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte("data: live-event\n\n"))
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		}))
		sseSrv := httptest.NewServer(sseLiveHandler)
		defer sseSrv.Close()
		sseResp, err := sseSrv.Client().Get(sseSrv.URL + "/events")
		if err != nil {
			t.Fatalf("live sse Get failed: %v", err)
		}
		if sseResp.StatusCode != http.StatusOK || !strings.HasPrefix(sseResp.Header.Get("Content-Type"), "text/event-stream") {
			_ = sseResp.Body.Close()
			t.Fatalf("live sse headers = %d %q", sseResp.StatusCode, sseResp.Header.Get("Content-Type"))
		}
		close(clientGotHeaders)
		sseBody, err := io.ReadAll(sseResp.Body)
		_ = sseResp.Body.Close()
		if err != nil || string(sseBody) != "data: live-event\n\n" {
			t.Fatalf("live sse body = %q, err=%v", string(sseBody), err)
		}
	})

	t.Run("request_body_limit_413_and_read_error_400_and_json_parse_error", func(t *testing.T) {
		th := newOutcomeTestHarness(t, config.WAFConfig{
			Enabled:          true,
			Mode:             "block",
			CRSEnabled:       true,
			RequestBodyLimit: config.Size(64),
		})
		var originHits atomic.Int64
		handler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			originHits.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

		// 1. Body exceeding 64-byte RequestBodyLimit is rejected with 413 (OutcomeRejectedBodyLimit).
		reqOversize := httptest.NewRequest(http.MethodPost, "http://app.example.test/upload", strings.NewReader(strings.Repeat("A", 128)))
		reqOversize.Header.Set("Content-Type", "text/plain")
		rrOversize := httptest.NewRecorder()
		handler.ServeHTTP(rrOversize, reqOversize)
		if rrOversize.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize body status = %d, want 413", rrOversize.Code)
		}

		// 2. Broken request body reader returns 400 Bad Request (OutcomeInspectionError), never silent 200 OK.
		reqBroken := httptest.NewRequest(http.MethodPost, "http://app.example.test/upload", io.NopCloser(iotestErrReader{err: errors.New("socket reset")}))
		reqBroken.Header.Set("Content-Type", "text/plain")
		rrBroken := httptest.NewRecorder()
		handler.ServeHTTP(rrBroken, reqBroken)
		if rrBroken.Code != http.StatusBadRequest {
			t.Fatalf("broken body status = %d, want 400", rrBroken.Code)
		}

		// 3. MaxBytesError from an outer reader returns 413 (OutcomeRejectedBodyLimit).
		reqMaxBytes := httptest.NewRequest(http.MethodPost, "http://app.example.test/upload", io.NopCloser(iotestErrReader{err: &http.MaxBytesError{Limit: 32}}))
		reqMaxBytes.Header.Set("Content-Type", "text/plain")
		rrMaxBytes := httptest.NewRecorder()
		handler.ServeHTTP(rrMaxBytes, reqMaxBytes)
		if rrMaxBytes.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("MaxBytesError status = %d, want 413", rrMaxBytes.Code)
		}

		// 4. Malformed JSON triggers Coraza rule 200002 (status 400, error_category=request_body_parse_error).
		reqBadJSON := httptest.NewRequest(http.MethodPost, "http://app.example.test/api", strings.NewReader(`{"broken":`))
		reqBadJSON.Header.Set("Content-Type", "application/json")
		rrBadJSON := httptest.NewRecorder()
		handler.ServeHTTP(rrBadJSON, reqBadJSON)
		if rrBadJSON.Code != http.StatusBadRequest {
			t.Fatalf("malformed JSON status = %d, want 400", rrBadJSON.Code)
		}

		if originHits.Load() != 0 {
			t.Fatalf("originHits = %d, want 0 across all 4 rejected requests", originHits.Load())
		}

		_, tx, _, snap := th.snapshot()
		wantTx := []string{
			"block:rejected_body_limit",
			"block:inspection_error",
			"block:rejected_body_limit",
			"block:interrupted_request",
		}
		if len(tx) != len(wantTx) {
			t.Fatalf("txOutcomes = %v, want %v", tx, wantTx)
		}
		for i := range wantTx {
			if tx[i] != wantTx[i] {
				t.Fatalf("txOutcomes[%d] = %q, want %q", i, tx[i], wantTx[i])
			}
		}
		if snap.Events[0].InterruptedRuleID != 200002 || snap.Events[0].ErrorCategory != "request_body_parse_error" {
			t.Fatalf("expected rule 200002 parse error event, got %+v", snap.Events[0])
		}
	})
}

type iotestErrReader struct{ err error }

func (r iotestErrReader) Read([]byte) (int, error) { return 0, r.err }

func TestWAFConcurrentTransactionsAndCanaryPrivacy(t *testing.T) {
	previous := redact.Global()
	redact.Install(redact.NewState([]string{"canary-configured-secret"}, 4))
	t.Cleanup(func() { redact.Install(previous) })

	var logBuf bytes.Buffer
	var logMu sync.Mutex
	safeWriter := syncWriter{mu: &logMu, w: &logBuf}
	logger := slog.New(slog.NewJSONHandler(safeWriter, nil))

	inv := NewInvestigationStore(256, 15*time.Minute)
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "block",
		CRSEnabled:  true,
		InlineRules: `SecRule ARGS:comment "@contains TRIGGER_BLOCK" "id:3001,phase:2,deny,status:403,log,msg:'blocked comment=%{ARGS.comment}',tag:'custom-block'"`,
	}
	applyTestDefaults(&cfg)
	fw, err := New(context.Background(), cfg, Options{
		Logger:        logger,
		Versions:      versionid.Process(),
		Investigation: inv,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	hA := middleware.RequestID()(fw.RouteMiddleware(RouteAttribution{RouteID: "route-a", Scope: "global"}, func() uint64 { return 11 })(newOKHandler()))
	hB := middleware.RequestID()(fw.RouteMiddleware(RouteAttribution{RouteID: "route-b", Scope: "override"}, func() uint64 { return 22 })(newOKHandler()))

	// Concurrent barrier: 16 goroutines sending mixed clean and blocked requests
	// with identical client-supplied X-Request-ID to verify zero cross-attribution.
	start := make(chan struct{})
	var wg sync.WaitGroup
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			for iter := 0; iter < 10; iter++ {
				if (id+iter)%2 == 0 {
					body := "comment=TRIGGER_BLOCK_canary_body_val&api_key=canary_query_secret"
					req := httptest.NewRequest(http.MethodPost, "http://app.example.test/submit/canary-configured-secret?token=canary_url_secret", strings.NewReader(body))
					req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
					req.Header.Set("Cookie", "session=canary_cookie_secret")
					req.Header.Set("Authorization", "Bearer canary_bearer_secret")
					req.Header.Set("X-Request-ID", "client-shared-id")
					rr := httptest.NewRecorder()
					if id%4 == 0 {
						hA.ServeHTTP(rr, req)
					} else {
						hB.ServeHTTP(rr, req)
					}
					if rr.Code != http.StatusForbidden {
						t.Errorf("blocked request status = %d, want 403", rr.Code)
					}
					if !strings.HasPrefix(rr.Header().Get(HeaderWAFEvent), "wev_") {
						t.Errorf("missing %s header", HeaderWAFEvent)
					}
				} else {
					req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://app.example.test/clean/%d", iter), nil)
					req.Header.Set("X-Request-ID", "client-shared-id")
					rr := httptest.NewRecorder()
					hA.ServeHTTP(rr, req)
					if rr.Code != http.StatusOK {
						t.Errorf("clean request status = %d, want 200", rr.Code)
					}
					if rr.Header().Get(HeaderWAFEvent) != "" {
						t.Errorf("clean request unexpectedly emitted %s", HeaderWAFEvent)
					}
				}
			}
		}(g)
	}
	close(start)
	wg.Wait()

	snap := inv.Snapshot(time.Now().UTC(), InvestigationFilter{})
	if snap.Retained != 80 {
		t.Fatalf("retained blocked events = %d, want 80", snap.Retained)
	}

	seenEventIDs := make(map[string]struct{}, len(snap.Events))
	for _, ev := range snap.Events {
		if _, dup := seenEventIDs[ev.EventID]; dup {
			t.Fatalf("duplicate server EventID %q despite shared client X-Request-ID", ev.EventID)
		}
		seenEventIDs[ev.EventID] = struct{}{}
		if ev.RequestIDHint != "client-shared-id" {
			t.Fatalf("RequestIDHint = %q, want client-shared-id", ev.RequestIDHint)
		}
		if (ev.Route.RouteID == "route-a" && ev.Generation != 11) || (ev.Route.RouteID == "route-b" && ev.Generation != 22) {
			t.Fatalf("cross-attributed route/generation: route=%q gen=%d", ev.Route.RouteID, ev.Generation)
		}
	}

	// Verify canary secrets never appear in serialized investigation snapshot or structured logs.
	snapBytes, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("Marshal snapshot: %v", err)
	}
	combinedSinks := string(snapBytes) + "\n" + logBuf.String()
	for _, canary := range []string{
		"TRIGGER_BLOCK_canary_body_val",
		"canary_query_secret",
		"canary_url_secret",
		"canary_cookie_secret",
		"canary_bearer_secret",
		"canary-configured-secret",
	} {
		if strings.Contains(combinedSinks, canary) {
			t.Fatalf("canary secret %q leaked into WAF investigation snapshot or logs", canary)
		}
	}
}

type syncWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (s syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

type hijackerResponseRecorder struct {
	*httptest.ResponseRecorder
	hijackErr error
}

func (h *hijackerResponseRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h.hijackErr != nil {
		return nil, nil, h.hijackErr
	}
	c1, c2 := net.Pipe()
	_ = c2.Close()
	return c1, bufio.NewReadWriter(bufio.NewReader(c1), bufio.NewWriter(c1)), nil
}

type pusherOnlyRecorder struct {
	*httptest.ResponseRecorder
}

func (p *pusherOnlyRecorder) Push(_ string, _ *http.PushOptions) error { return nil }

type pusherHijackerRecorder struct {
	hijackerResponseRecorder
}

func (p *pusherHijackerRecorder) Push(_ string, _ *http.PushOptions) error { return nil }

func TestWAFProcessPartialLiveTCPStreamingFlush(t *testing.T) {
	// Item 2 Subcase B: Barriered live-TCP ProcessPartial test.
	// Write beyond a small SecResponseBodyLimit, call Flush(), keep the handler
	// blocked on a barrier, and verify the client receives the flushed chunk
	// over TCP before the handler barrier is released.
	th := newOutcomeTestHarness(t, config.WAFConfig{
		Enabled:           true,
		Mode:              "block",
		CRSEnabled:        false,
		ResponseBodyCheck: true,
		InlineRules: strings.Join([]string{
			"SecResponseBodyAccess On",
			"SecResponseBodyMimeType text/plain",
			"SecResponseBodyLimit 64",
			"SecResponseBodyLimitAction ProcessPartial",
		}, "\n"),
	})

	clientGotFirstChunk := make(chan struct{})
	liveHandler := th.fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		// First write (32 bytes) stays inside the 64-byte inspection buffer;
		// premature Flush() before crossing the limit must NOT flush uninspected bytes.
		_, _ = w.Write([]byte(strings.Repeat("A", 32)))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		// Second write (64 bytes, total 96 > 64 limit) crosses SecResponseBodyLimit
		// under ProcessPartial, releasing the 64-byte inspected prefix + 32-byte overflow
		// and enabling subsequent Flush() calls.
		_, _ = w.Write([]byte(strings.Repeat("B", 64)))
		// Third write (16 bytes) after buffer release + Flush() while handler remains blocked.
		_, _ = w.Write([]byte("STREAM_CHUNK_01\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-clientGotFirstChunk:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte("FINAL_TAIL\n"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
	}))

	srv := httptest.NewServer(liveHandler)
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/partial-stream")
	if err != nil {
		t.Fatalf("GET /partial-stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	wantPrefix := strings.Repeat("A", 32) + strings.Repeat("B", 64) + "STREAM_CHUNK_01\n"
	prefixBuf := make([]byte, len(wantPrefix))
	if _, err := io.ReadFull(resp.Body, prefixBuf); err != nil {
		t.Fatalf("ReadFull before handler release failed: %v", err)
	}
	if string(prefixBuf) != wantPrefix {
		t.Fatalf("client-received prefix while handler blocked = %q, want %q", string(prefixBuf), wantPrefix)
	}

	// Now unblock the handler so it writes FINAL_TAIL.
	close(clientGotFirstChunk)
	tailBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll tail: %v", err)
	}
	if string(tailBytes) != "FINAL_TAIL\n" {
		t.Fatalf("tail = %q, want FINAL_TAIL\\n", string(tailBytes))
	}
}

func TestWAFInvestigationRetainedMemoryDetach(t *testing.T) {
	// Item 9: Complete 256 requests with unique large (128 KiB) accepted request
	// targets (32 MiB total input path backing storage), retain all 256 events in
	// InvestigationStore, drop all request references, force GC, and verify that
	// retained heap growth stays bounded (< 4 MiB) rather than pinning the 32 MiB
	// of request target buffers.
	cfg := config.WAFConfig{
		Enabled:     true,
		Mode:        "detect",
		CRSEnabled:  false,
		InlineRules: `SecRule REQUEST_URI "@rx ^/large/" "id:3901,phase:1,pass,log,msg:'large path observed',tag:'mem-test'"`,
	}
	applyTestDefaults(&cfg)
	inv := NewInvestigationStore(MaxInvestigationEvents, 15*time.Minute)
	fw, err := New(context.Background(), cfg, Options{
		Versions:      versionid.Process(),
		Investigation: inv,
	})
	if err != nil {
		t.Fatalf("New Firewall: %v", err)
	}
	handler := fw.Middleware()(newOKHandler())

	runtime.GC()
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	func() {
		for i := 0; i < MaxInvestigationEvents; i++ {
			// Construct a unique 128 KiB path made of short safe segments ("/large/a/a/...")
			// so SanitizePath takes the !needsRedact fast-path truncation rather than
			// rebuilding through strings.Builder, plus a unique 128 KiB X-Request-ID header.
			var sb strings.Builder
			sb.Grow(132 * 1024)
			sb.WriteString("/large")
			sb.WriteString(strings.Repeat("/a", 64*1024))
			fmt.Fprintf(&sb, "/tail-%04d", i)
			largePath := sb.String()

			var hsb strings.Builder
			hsb.Grow(130 * 1024)
			fmt.Fprintf(&hsb, "req-%04d-", i)
			hsb.WriteString(strings.Repeat("r", 128*1024))
			largeReqID := hsb.String()

			req := httptest.NewRequest(http.MethodGet, "http://app.example.test"+largePath, nil)
			req.Header.Set("X-Request-ID", largeReqID)
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("iter %d status = %d, want 200", i, rr.Code)
			}
		}
	}()

	runtime.GC()
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)

	snap := inv.Snapshot(time.Now().UTC(), InvestigationFilter{Limit: MaxInvestigationEvents})
	if snap.Retained != MaxInvestigationEvents {
		t.Fatalf("snap.Retained = %d, want %d", snap.Retained, MaxInvestigationEvents)
	}
	for idx, ev := range snap.Events {
		if !strings.HasPrefix(ev.Path, "/large/a/a/") || len(ev.Path) > 256 {
			t.Fatalf("event[%d] path = %q (len=%d), want fast-path truncated /large/a/a/...", idx, ev.Path, len(ev.Path))
		}
		if len(ev.RequestIDHint) > 128 {
			t.Fatalf("event[%d] RequestIDHint len = %d > 128", idx, len(ev.RequestIDHint))
		}
		if b, err := json.Marshal(ev); err != nil || len(b) > MaxEventBytes {
			t.Fatalf("event[%d] json len = %d (err=%v) > MaxEventBytes (%d)", idx, len(b), err, MaxEventBytes)
		}
	}
	var retainedGrowth int64
	if after.HeapInuse > before.HeapInuse {
		retainedGrowth = int64(after.HeapInuse - before.HeapInuse)
	}
	// 256 events * (128 KiB path + 128 KiB header) would be ~64 MiB if backing
	// arrays were pinned; detached strings must keep retained heap growth well under 4 MiB.
	const maxAllowedRetainedBytes = 4 << 20
	if retainedGrowth > maxAllowedRetainedBytes {
		t.Fatalf("retained HeapInuse growth = %d bytes (> %d bytes); large request path/header backing allocations were not detached", retainedGrowth, maxAllowedRetainedBytes)
	}
	runtime.KeepAlive(inv)
}

func BenchmarkWAFInvestigationLargePathRecord(b *testing.B) {
	store := NewInvestigationStore(256, 15*time.Minute)
	largePath := "/large" + strings.Repeat("/a", 32*1024)
	largeHint := "req-bench-" + strings.Repeat("r", 64*1024)
	ev := TransactionEvent{
		Outcome:       OutcomeMatchedObserved,
		Mode:          "detect",
		Method:        "GET",
		Host:          "app.example.test",
		Path:          largePath,
		RequestIDHint: largeHint,
		Route: RouteAttribution{
			RouteID:     "rt-bench",
			Listen:      ":8443",
			PathPattern: "/large",
			Scope:       "global",
		},
		MatchedRules: []MatchedRuleSummary{
			{RuleID: 942100, Phase: 2, SourceClass: "embedded", Tags: []string{"attack-sqli"}, MatchedTargets: []string{"ARGS:q"}},
		},
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Record(ev)
	}
}
