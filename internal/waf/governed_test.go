// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/inspection"
	"jul/internal/reqdeadline"
)

func governedLimits(mode inspection.Mode) inspection.Limits {
	return inspection.Limits{
		Mode:                   mode,
		MaxBytes:               1 << 30,
		MaxResponseHeaderBytes: 1 << 20,
		EvaluationSlots:        1,
		EvaluationQueueTimeout: 50 * time.Millisecond,
		BaseChargeBytes:        16 << 10,
	}
}

func newGovernor(t *testing.T, limits inspection.Limits) *inspection.Governor {
	t.Helper()
	g, err := inspection.New(limits, inspection.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func governedFirewall(t *testing.T, cfg config.WAFConfig, g *inspection.Governor, hooks Hooks) *Firewall {
	t.Helper()
	applyTestDefaults(&cfg)
	cfg.Enabled = true
	fw, err := New(context.Background(), cfg, Options{Governor: g, Hooks: hooks})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fw.Close() })
	return fw
}

func assertDrained(t *testing.T, g *inspection.Governor) {
	t.Helper()
	s := g.Snapshot()
	if s.ActiveLeases != 0 || s.ActiveEvaluations != 0 || s.Usage != ([len(s.Usage)]uint64{}) {
		t.Fatalf("governor not drained: %+v", s)
	}
}

type outcomeRecorder struct {
	mu       sync.Mutex
	outcomes []string
}

func (o *outcomeRecorder) hook(_, outcome string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.outcomes = append(o.outcomes, outcome)
}

func (o *outcomeRecorder) get() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.outcomes...)
}

func TestGovernedWAFChargesAndReleasesTransaction(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
	var during inspection.Snapshot
	var leaseSeen bool
	var gotBody string
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		during = g.Snapshot()
		leaseSeen = inspection.LeaseFrom(r.Context()) != nil
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	body := "a=1&b=2&c=hello"
	req := httptest.NewRequest(http.MethodPost, "/submit?x=1&y=2", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK || gotBody != body {
		t.Fatalf("status %d body %q, want 200 and the original body", rr.Code, gotBody)
	}
	if !leaseSeen || during.ActiveLeases != 1 {
		t.Fatalf("origin did not run under the WAF's lease: %+v", during)
	}
	if during.Usage[inspection.WAFStateBytes] == 0 || during.Usage[inspection.BufferBytes] == 0 ||
		during.Usage[inspection.AuditBytes] < inspection.WAFR5FixedBytes {
		t.Fatalf("transaction not charged while the origin ran: %+v", during.Usage)
	}
	assertDrained(t, g)
}

func TestGovernedWAFReusesOuterLease(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
	lease, ctx, owner, err := g.AcquireLease(context.Background())
	if err != nil || !owner {
		t.Fatal(err)
	}
	var inner *inspection.Lease
	var leases uint64
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inner = inspection.LeaseFrom(r.Context())
		leases = g.Snapshot().ActiveLeases
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	h.ServeHTTP(httptest.NewRecorder(), req)
	if inner != lease || leases != 1 {
		t.Fatalf("WAF did not reuse the outer lease (same=%v, leases=%d)", inner == lease, leases)
	}
	// The WAF released its own charges; only the outer base remains.
	s := g.Snapshot()
	if s.ActiveLeases != 1 || s.Usage[inspection.WAFStateBytes] != 0 || s.Usage[inspection.AuditBytes] != 0 || s.Usage[inspection.BufferBytes] != 0 {
		t.Fatalf("WAF charges outlived its transaction: %+v", s)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	assertDrained(t, g)
}

func TestGovernedWAFRefusesBeforeTransaction(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.MaxBytes = limits.BaseChargeBytes + inspection.WAFR5FixedBytes - 1
	g := newGovernor(t, limits)
	rec := &outcomeRecorder{}
	fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{OnTransaction: rec.hook})
	reached := false
	h := fw.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=1")))
	if rr.Code != http.StatusServiceUnavailable || rr.Header().Get("Retry-After") != "1" || rr.Header().Get("Connection") != "close" {
		t.Fatalf("R5 refusal = %d %v, want 503 Retry-After: 1 Connection: close", rr.Code, rr.Header())
	}
	if reached || len(rec.get()) != 0 {
		t.Fatalf("refused request reached the origin (%v) or created a transaction (%v)", reached, rec.get())
	}
	assertDrained(t, g)
}

func TestGovernedWAFSlotSaturation(t *testing.T) {
	for _, mode := range []inspection.Mode{inspection.Enforce, inspection.Observe} {
		t.Run(string(mode), func(t *testing.T) {
			g := newGovernor(t, governedLimits(mode))
			rec := &outcomeRecorder{}
			fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{OnTransaction: rec.hook})
			holder, _, _, err := g.AcquireLease(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			slot, _, err := g.TryEvaluation(context.Background(), holder)
			if err != nil {
				t.Fatal(err)
			}
			reached := false
			h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				_, _ = io.Copy(io.Discard, r.Body)
			}))
			// An unread body forbids waiting: the slot is only try-acquired.
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=1"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rr := httptest.NewRecorder()
			start := time.Now()
			h.ServeHTTP(rr, req)
			if mode == inspection.Enforce {
				if rr.Code != http.StatusServiceUnavailable || reached {
					t.Fatalf("saturated enforce = %d (origin reached %v), want 503 without the origin", rr.Code, reached)
				}
				if time.Since(start) > time.Second {
					t.Fatal("try-acquire waited")
				}
				if got := rec.get(); len(got) != 1 || got[0] != OutcomeInspectionRefused {
					t.Fatalf("outcomes = %v, want [%s]", got, OutcomeInspectionRefused)
				}
			} else if rr.Code != http.StatusOK || !reached {
				t.Fatalf("saturated observe = %d (origin reached %v), want unchanged 200", rr.Code, reached)
			}
			_ = slot.Release()
			_ = holder.Close()
			assertDrained(t, g)
		})
	}
}

func TestGovernedWAFPartialProcessingForwardsWholeBody(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{
		RequestBodyLimit: config.Size(64),
		InlineRules:      "SecRequestBodyLimitAction ProcessPartial",
	}, g, Hooks{})
	var got string
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
	}))
	body := strings.Repeat("0123456789", 20)
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK || got != body {
		t.Fatalf("partial processing = %d, forwarded %d of %d bytes", rr.Code, len(got), len(body))
	}
	assertDrained(t, g)
}

func TestGovernedWAFBodyLimitStays413(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{RequestBodyLimit: config.Size(64)}, g, Hooks{})
	h := fw.Middleware()(newOKHandler())
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", 64))))
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("body at the limit = %d, want 413", rr.Code)
	}
	assertDrained(t, g)
}

func TestGovernedWAFUnsupportedDeadlineFailsClosed(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.BodyTimeout = time.Second
	g := newGovernor(t, limits)
	fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
	reached := false
	h := fw.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true }))
	rr := httptest.NewRecorder() // applies no read deadline
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/", strings.NewReader("a=1")))
	if rr.Code != http.StatusInternalServerError || reached {
		t.Fatalf("unsupported deadline = %d (origin reached %v), want 500 without the origin", rr.Code, reached)
	}
	assertDrained(t, g)
}

func TestGovernedWAFRejectsUnsupportedAuditWriter(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	dir := t.TempDir()
	for name, rules := range map[string]string{
		"writer":    "SecAuditLogType Concurrent\nSecAuditLogStorageDir " + dir + "\nSecAuditLog " + filepath.Join(dir, "a.log"),
		"formatter": "SecAuditLog " + filepath.Join(dir, "b.log") + "\nSecAuditLogFormat OCSF",
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config.WAFConfig{Enabled: true, InlineRules: rules}
			applyTestDefaults(&cfg)
			if _, err := New(context.Background(), cfg, Options{Governor: g}); err == nil {
				t.Fatal("governed WAF accepted an unsupported audit configuration")
			}
			fw, err := New(context.Background(), cfg, Options{})
			if err != nil {
				t.Fatalf("ungoverned WAF rejected it: %v", err)
			}
			_ = fw.Close()
		})
	}
}

const auditRules = `SecAuditEngine On
SecAuditLogParts ABCFHKZ
SecRule ARGS:attack "@streq yes" "id:4100,phase:2,pass,log,msg:'attack'"`

func TestGovernedWAFWritesAuditRecord(t *testing.T) {
	for _, format := range []string{"Native", "JSON"} {
		t.Run(format, func(t *testing.T) {
			g := newGovernor(t, governedLimits(inspection.Enforce))
			target := filepath.Join(t.TempDir(), "audit.log")
			fw := governedFirewall(t, config.WAFConfig{
				InlineRules: auditRules + "\nSecAuditLogFormat " + format + "\nSecAuditLog " + target,
			}, g, Hooks{})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("attack=yes"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			fw.Middleware()(newOKHandler()).ServeHTTP(httptest.NewRecorder(), req)
			out, err := os.ReadFile(target)
			if err != nil || !strings.Contains(string(out), "attack") {
				t.Fatalf("audit record not written: %v %q", err, out)
			}
			assertDrained(t, g)
			// Close releases the file handle (Windows cannot remove an open file).
			f := fw.audit.writer.closer.(*os.File)
			if err := fw.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Write([]byte("x")); !errors.Is(err, os.ErrClosed) {
				t.Fatalf("audit file still open after Close: %v", err)
			}
		})
	}
}

func TestGovernedWAFDropsAuditRecordOverBudget(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.MaxBytes = 256 << 20
	var drops []string
	g, err := inspection.New(limits, inspection.Hooks{OnAuditDrop: func(reason string) { drops = append(drops, reason) }})
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "audit.log")
	fw := governedFirewall(t, config.WAFConfig{InlineRules: auditRules + "\nSecAuditLog " + target}, g, Hooks{})
	filler, _, _, err := g.AcquireLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Leave room for the transaction's remaining work but not for an
		// audit record carrying a 1 MiB response header (part F).
		var used uint64
		for _, u := range g.Snapshot().Usage {
			used += u
		}
		if _, err := filler.Charge(inspection.BufferBytes, limits.MaxBytes-used-(256<<10)); err != nil {
			t.Error(err)
		}
		w.Header().Set("X-Pad", strings.Repeat("p", 1<<20))
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("attack=yes"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("audit budget refusal changed the response: %d", rr.Code)
	}
	if len(drops) != 1 || drops[0] != inspection.AuditDropBudget {
		t.Fatalf("drops = %v, want [%s]", drops, inspection.AuditDropBudget)
	}
	if out, _ := os.ReadFile(target); len(out) != 0 {
		t.Fatalf("dropped record was written: %q", out)
	}
	_ = filler.Close()
	assertDrained(t, g)
}

type blockingWriter struct {
	release chan struct{}
	mu      sync.Mutex
	records []string
}

func (b *blockingWriter) Write(p []byte) (int, error) {
	<-b.release
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(p) > 1 {
		b.records = append(b.records, string(p))
	}
	return len(p), nil
}

// TestGovernedWAFAbandonsBlockedAuditWriter checks the writer deadline: the
// request completes, the abandoned charge stays visible until the write
// returns, and the written record is a detached copy that a later
// transaction (reusing the pooled one) cannot change.
func TestGovernedWAFAbandonsBlockedAuditWriter(t *testing.T) {
	old := auditWriteTimeout
	auditWriteTimeout = 20 * time.Millisecond
	t.Cleanup(func() { auditWriteTimeout = old })
	g := newGovernor(t, governedLimits(inspection.Enforce))
	fw := governedFirewall(t, config.WAFConfig{
		InlineRules: auditRules + "\nSecAuditLog " + filepath.Join(t.TempDir(), "audit.log"),
	}, g, Hooks{})
	blocker := &blockingWriter{release: make(chan struct{})}
	fw.audit.writer.mu.Lock()
	fw.audit.writer.out = blocker
	fw.audit.writer.mu.Unlock()

	serve := func(marker string) {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("attack=yes&marker="+marker))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		done := make(chan struct{})
		go func() {
			fw.Middleware()(newOKHandler()).ServeHTTP(httptest.NewRecorder(), req)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("a blocked audit writer held the request past its deadline")
		}
	}
	serve("first")
	s := g.Snapshot()
	if s.AuditOutput.Abandoned != 1 || s.AuditOutput.AbandonedBytes == 0 || s.Usage[inspection.AuditBytes] == 0 {
		t.Fatalf("abandoned write not visible after the request: %+v", s)
	}
	serve("second")
	close(blocker.release)
	deadline := time.Now().Add(5 * time.Second)
	for g.Snapshot().AuditOutput.Abandoned != 0 {
		if time.Now().After(deadline) {
			t.Fatal("abandoned write was never refunded")
		}
		time.Sleep(time.Millisecond)
	}
	blocker.mu.Lock()
	records := append([]string(nil), blocker.records...)
	blocker.mu.Unlock()
	if len(records) != 2 {
		t.Fatalf("got %d records, want 2", len(records))
	}
	for _, rec := range records {
		if strings.Contains(rec, "marker=first") == strings.Contains(rec, "marker=second") {
			t.Fatalf("record mixes or lacks transactions: %q", rec)
		}
	}
	assertDrained(t, g)
}

func TestGovernedWAFSlowBodyTimesOut(t *testing.T) {
	limits := governedLimits(inspection.Enforce)
	limits.BodyTimeout = 100 * time.Millisecond
	g := newGovernor(t, limits)
	fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{})
	reached := make(chan struct{}, 1)
	srv := httptest.NewUnstartedServer(fw.Middleware()(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		reached <- struct{}{}
	})))
	srv.Config.ConnContext = reqdeadline.ConnContext
	srv.Start()
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 100\r\n\r\na=1")
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestTimeout {
		t.Fatalf("slow body = %d, want 408", resp.StatusCode)
	}
	select {
	case <-reached:
		t.Fatal("slow body reached the origin")
	default:
	}
	waitDrained(t, g)
}

func TestGovernedWAFHijackEndsTransactionOwnership(t *testing.T) {
	for _, tc := range []struct {
		name     string
		req      string
		released bool
	}{
		{"upgrade", "GET /ws HTTP/1.1\r\nHost: x\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n", true},
		// The unread inspected body is still the transaction's buffer.
		{"undrained_body", "POST /ws HTTP/1.1\r\nHost: x\r\nContent-Type: application/x-www-form-urlencoded\r\nContent-Length: 3\r\n\r\na=1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGovernor(t, governedLimits(inspection.Enforce))
			rec := &outcomeRecorder{}
			fw := governedFirewall(t, config.WAFConfig{CRSEnabled: true}, g, Hooks{OnTransaction: rec.hook})
			type observed struct {
				snap     inspection.Snapshot
				outcomes []string
			}
			seen := make(chan observed, 1)
			srv := httptest.NewUnstartedServer(fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				conn, brw, err := http.NewResponseController(w).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				// The tunnel is still open: ownership must already be over.
				seen <- observed{g.Snapshot(), rec.get()}
				if tc.released {
					// A committed exchange bypasses the transaction entirely.
					w.WriteHeader(http.StatusOK)
					if _, err := w.Write([]byte("x")); err != http.ErrHijacked {
						t.Errorf("write after hijack = %v, want ErrHijacked", err)
					}
					_ = http.NewResponseController(w).Flush()
				}
				_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
				_ = brw.Flush()
			})))
			srv.Config.ConnContext = reqdeadline.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.Start()
			defer srv.Close()
			conn, err := net.Dial("tcp", srv.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_, _ = io.WriteString(conn, tc.req)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusSwitchingProtocols {
				t.Fatalf("status %d, want 101", resp.StatusCode)
			}
			o := <-seen
			s := o.snap
			idle := s.ActiveLeases == 0 && s.ActiveEvaluations == 0 &&
				s.Usage[inspection.WAFStateBytes] == 0 && s.Usage[inspection.BufferBytes] == 0
			if idle != tc.released {
				t.Fatalf("ownership released at hijack = %v, want %v: %+v", idle, tc.released, s)
			}
			if tc.released && len(o.outcomes) != 1 {
				t.Fatalf("phase 5 did not run at the commit point: %v", o.outcomes)
			}
			waitDrained(t, g)
			if got := rec.get(); len(got) != 1 {
				t.Fatalf("outcomes %v, want exactly one", got)
			}
		})
	}
}

func TestWAFEngineOffForwardsWholeBody(t *testing.T) {
	cfg := config.WAFConfig{
		RequestBodyLimit: config.Size(1 << 20),
		InlineRules:      `SecRule REQUEST_URI "@beginsWith /off" "id:9001,phase:1,pass,nolog,ctl:ruleEngine=Off"`,
	}
	body := strings.Repeat("a=1&", 50_000) // larger than the 32 KiB read buffer
	for _, governed := range []bool{false, true} {
		var g *inspection.Governor
		if governed {
			g = newGovernor(t, governedLimits(inspection.Enforce))
		}
		fw := governedFirewall(t, cfg, g, Hooks{})
		var got string
		h := fw.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
		}))
		req := httptest.NewRequest(http.MethodPost, "/off", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK || got != body {
			t.Fatalf("governed=%v: status %d, forwarded %d of %d bytes", governed, rr.Code, len(got), len(body))
		}
		if governed {
			assertDrained(t, g)
		}
	}
}
