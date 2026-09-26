// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type failingWriter struct{ err error }

func (f failingWriter) Write(p []byte) (int, error) { return 0, f.err }

// An access-log sink that cannot write (ENOSPC, EIO) must be visible on the
// process log, once per interval, instead of silently dropping every line.
func TestAccessLogWriteFailuresAreReported(t *testing.T) {
	var logs bytes.Buffer
	w := &failureReportingWriter{w: failingWriter{err: errors.New("no space left on device")}, log: slog.New(slog.NewTextHandler(&logs, nil)), sink: "file"}
	access := slog.New(accessHandler(w, "json"))
	for i := 0; i < 100; i++ {
		access.Info("request", "status", 200)
	}
	out := logs.String()
	if n := strings.Count(out, "access log write failed"); n != 1 {
		t.Fatalf("%d failure reports for 100 failed writes, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, "no space left on device") || !strings.Contains(out, "failed_writes=1") || !strings.Contains(out, "level=ERROR") {
		t.Fatalf("report lacks the error, count or level: %s", out)
	}
	if w.failures.Load() != 100 {
		t.Fatalf("counted %d failures, want 100", w.failures.Load())
	}

	ok := &failureReportingWriter{w: &bytes.Buffer{}, log: slog.New(slog.NewTextHandler(&logs, nil))}
	logs.Reset()
	if _, err := ok.Write([]byte("line\n")); err != nil || logs.Len() != 0 || ok.failures.Load() != 0 {
		t.Fatalf("a successful write was reported: err=%v log=%q", err, logs.String())
	}
	silent := &failureReportingWriter{w: failingWriter{err: errors.New("x")}}
	if _, err := silent.Write([]byte("x")); err == nil {
		t.Fatal("write error was swallowed")
	}
}
