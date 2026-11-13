// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build waf

package waf

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/corazawaf/coraza/v3/experimental/plugins/plugintypes"
	"github.com/corazawaf/coraza/v3/types"

	"jul/internal/config"
	"jul/internal/inspection"
)

func TestJSONEscapedLenBoundsEncoding(t *testing.T) {
	for _, s := range []string{"plain", "q\"\\\n\r\t", "\x01<>&", "é€😀", "\xff\xfe", "\u2028\u2029", "mixed \xffé\u2028<"} {
		b, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := jsonEscapedLen(s), uint64(len(b)-2); got < want {
			t.Errorf("jsonEscapedLen(%q) = %d, below the %d bytes encoding/json writes", s, got, want)
		}
	}
}

func TestUngovernedWAFWritesAuditRecord(t *testing.T) {
	target := filepath.Join(t.TempDir(), "audit.log")
	fw := governedFirewall(t, config.WAFConfig{InlineRules: auditRules + "\nSecAuditLog " + target}, nil, Hooks{})
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("attack=yes"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	fw.Middleware()(newOKHandler()).ServeHTTP(httptest.NewRecorder(), req)
	out, err := os.ReadFile(target)
	if err != nil || !strings.Contains(string(out), "attack") {
		t.Fatalf("ungoverned audit record not written: %v %q", err, out)
	}
}

func TestAuditWriterStandardStreams(t *testing.T) {
	for target, want := range map[string]*os.File{"/dev/stdout": os.Stdout, "/dev/stderr": os.Stderr} {
		w := &auditWriter{}
		if err := w.Init(plugintypes.AuditLogConfig{Target: target}); err != nil {
			t.Fatal(err)
		}
		if w.out != want || w.closer != nil || w.Close() != nil {
			t.Fatalf("%s: writer %v closer %v", target, w.out, w.closer)
		}
	}
	if classifyFormatter(nil) != formatterNone {
		t.Fatal("nil formatter not classified as none")
	}
}

func TestAuditRelevantMirrorsRelevantOnly(t *testing.T) {
	re := regexp.MustCompile(`^5`)
	for _, tc := range []struct {
		engine types.AuditEngineStatus
		re     *regexp.Regexp
		status string
		want   bool
	}{
		{types.AuditEngineOff, nil, "500", false},
		{types.AuditEngineOn, re, "200", true},
		{types.AuditEngineRelevantOnly, nil, "200", true},
		{types.AuditEngineRelevantOnly, re, "503", true},
		{types.AuditEngineRelevantOnly, re, "200", false},
	} {
		if got := auditRelevant(txAuditState{engine: tc.engine}, tc.re, tc.status); got != tc.want {
			t.Errorf("auditRelevant(%v, %v, %s) = %v, want %v", tc.engine, tc.re, tc.status, got, tc.want)
		}
	}
}

func TestWAFReservationsGrowAndReconcile(t *testing.T) {
	g := newGovernor(t, governedLimits(inspection.Enforce))
	lease, _, _, err := g.AcquireLease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := &wafAdmission{gov: g, lease: lease}
	for _, n := range []uint64{4096, 1024, 8192} {
		if err := a.reserveState(&a.r1, &a.r1Amount, n); err != nil {
			t.Fatal(err)
		}
	}
	if got := g.Snapshot().Usage[inspection.WAFStateBytes]; got != 8192 || a.r1Amount != 8192 {
		t.Fatalf("cumulative reservation = %d (amount %d), want 8192: it grows, never shrinks", got, a.r1Amount)
	}

	ac := &auditTxCtx{gov: g, lease: lease, precharged: 1000}
	ac.reconcileConstruction(500)
	if ac.overran || len(ac.extra) != 0 {
		t.Fatal("construction within the estimate was charged again")
	}
	ac.reconcileConstruction(3000)
	if !ac.overran || g.Snapshot().Usage[inspection.AuditBytes] != 2000 {
		t.Fatalf("overrun not charged: overran=%v usage=%d", ac.overran, g.Snapshot().Usage[inspection.AuditBytes])
	}
	ac.release()
	a.release()
	_ = lease.Close()
	assertDrained(t, g)
}
