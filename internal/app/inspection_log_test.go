// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package app

import (
	"log/slog"
	"strings"
	"testing"

	"jul/internal/inspection"
)

// TestInspectionRejectionLogIsThrottledAndBounded: the overload diagnostic is
// one line per interval carrying only the class, mode and limit (ADR 0032 §9).
func TestInspectionRejectionLogIsThrottledAndBounded(t *testing.T) {
	var buf strings.Builder
	log := inspectionRejectionLog(slog.New(slog.NewTextHandler(&buf, nil)), func() uint64 { return 4096 })
	for range 5 {
		log(inspection.ByteSaturated, inspection.Enforce)
	}
	log(inspection.ByteSaturated, inspection.Observe)
	out := buf.String()
	if n := strings.Count(out, "\n"); n != 1 {
		t.Fatalf("%d lines in one interval, want 1:\n%s", n, out)
	}
	want := `level=WARN msg="inspection budget saturated; request rejected" class=bytes mode=enforce max_buffered_bytes=4096` + "\n"
	if !strings.HasSuffix(out, want) {
		t.Fatalf("got %q, want suffix %q", out, want)
	}

	var observed strings.Builder
	inspectionRejectionLog(slog.New(slog.NewTextHandler(&observed, nil)), func() uint64 { return 1 })(inspection.ByteSaturated, inspection.Observe)
	if !strings.Contains(observed.String(), "observe mode would reject") {
		t.Fatalf("observe line: %q", observed.String())
	}
	inspectionRejectionLog(nil, func() uint64 { return 1 })(inspection.ByteSaturated, inspection.Enforce)
}
