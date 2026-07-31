// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package egress

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// captureHandler is a minimal slog.Handler that records emitted records so a
// test can assert on level and attributes without parsing formatted output.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

func attrMap(r slog.Record) map[string]string {
	m := make(map[string]string)
	r.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.String()
		return true
	})
	return m
}

func TestBlockLoggerIgnoresAllows(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	bl.observe(Decision{Subsystem: SubsystemAuth, Result: ResultAllow, Host: "idp.example.com"})
	if got := len(h.snapshot()); got != 0 {
		t.Fatalf("allow decision logged %d records, want 0", got)
	}
}

func TestBlockLoggerLogsFields(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	bl.observe(Decision{
		Subsystem: SubsystemDiscovery,
		Result:    ResultBlock,
		Reason:    ReasonHostNotAllowed,
		Host:      "svc.internal",
		IP:        "10.0.0.9",
	})
	recs := h.snapshot()
	if len(recs) != 1 {
		t.Fatalf("records = %d, want 1", len(recs))
	}
	if recs[0].Level != slog.LevelWarn {
		t.Errorf("level = %v, want WARN for discovery", recs[0].Level)
	}
	m := attrMap(recs[0])
	if m["subsystem"] != SubsystemDiscovery || m["host"] != "svc.internal" ||
		m["reason"] != string(ReasonHostNotAllowed) || m["resolved_ip"] != "10.0.0.9" {
		t.Errorf("attrs = %v, missing/incorrect subsystem/host/reason/resolved_ip", m)
	}
}

func TestBlockLoggerOmitsRedundantIP(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	// An IP-literal block carries Host == IP; the resolved_ip attr is redundant.
	bl.observe(Decision{Subsystem: SubsystemACME, Result: ResultBlock, Reason: ReasonIPNotAllowed, Host: "10.0.0.1", IP: "10.0.0.1"})
	m := attrMap(h.snapshot()[0])
	if _, ok := m["resolved_ip"]; ok {
		t.Errorf("resolved_ip should be omitted when equal to host, got %v", m)
	}
}

func TestBlockLoggerPluginLogsInfo(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	bl.observe(Decision{Subsystem: SubsystemPlugin, Result: ResultBlock, Reason: ReasonHostNotAllowed, Host: "evil.example"})
	recs := h.snapshot()
	if len(recs) != 1 || recs[0].Level != slog.LevelInfo {
		t.Fatalf("plugin block level = %v, want INFO", recs)
	}
}

func TestBlockLoggerRateLimitsIdentical(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	now := time.Unix(0, 0)
	bl.now = func() time.Time { return now }

	d := Decision{Subsystem: SubsystemAuth, Result: ResultBlock, Reason: ReasonHostNotAllowed, Host: "idp.example.com", IP: "10.0.0.9"}
	bl.observe(d)
	bl.observe(d) // identical, within window → suppressed
	if got := len(h.snapshot()); got != 1 {
		t.Fatalf("identical blocks logged %d, want 1 (rate-limited)", got)
	}

	// A different reason for the same host is a distinct event → logged.
	bl.observe(Decision{Subsystem: SubsystemAuth, Result: ResultBlock, Reason: ReasonMixedDNS, Host: "idp.example.com", IP: "10.0.0.9"})
	if got := len(h.snapshot()); got != 2 {
		t.Fatalf("distinct-reason block logged %d, want 2", got)
	}

	// After the window elapses, the first event logs again.
	now = now.Add(bl.window + time.Second)
	bl.observe(d)
	if got := len(h.snapshot()); got != 3 {
		t.Fatalf("post-window block logged %d, want 3", got)
	}
}

func TestBlockLoggerBoundsMemory(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	bl.max = 8
	for i := 0; i < bl.max*3; i++ {
		bl.observe(Decision{Subsystem: SubsystemDiscovery, Result: ResultBlock, Reason: ReasonHostNotAllowed, Host: string(rune('a'+i%26)) + ".example"})
	}
	bl.mu.Lock()
	n := len(bl.last)
	bl.mu.Unlock()
	if n > bl.max {
		t.Errorf("tracker holds %d keys, want <= max %d", n, bl.max)
	}
}

func TestBlockLoggerNilIsNoop(t *testing.T) {
	obs := NewBlockLogObserver(nil)
	// Must not panic.
	obs(Decision{Subsystem: SubsystemAuth, Result: ResultBlock, Reason: ReasonHostNotAllowed, Host: "x"})
}

func TestBlockLoggerConcurrent(t *testing.T) {
	h := &captureHandler{}
	bl := newBlockLogger(slog.New(h))
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			bl.observe(Decision{Subsystem: SubsystemPlugin, Result: ResultBlock, Reason: ReasonHostNotAllowed, Host: string(rune('a'+i%8)) + ".example"})
		}(i)
	}
	wg.Wait()
}
