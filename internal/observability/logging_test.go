// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestNewLoggerDefaultsToStderr(t *testing.T) {
	logger := NewLogger(nil, "info", "text")
	if logger == nil {
		t.Fatal("expected non-nil logger")
	}
}

func TestNewLoggerJSONFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "debug", "json")
	logger.Debug("test message")

	out := buf.String()
	if !strings.Contains(out, "test message") {
		t.Fatalf("expected JSON output containing 'test message', got %q", out)
	}
	if !strings.Contains(out, `"level":"DEBUG"`) {
		t.Fatalf("expected JSON level DEBUG, got %q", out)
	}
}

func TestNewLoggerTextFormat(t *testing.T) {
	var buf bytes.Buffer
	logger := NewLogger(&buf, "warn", "text")
	logger.Warn("warn message")

	out := buf.String()
	if !strings.Contains(out, "warn message") {
		t.Fatalf("expected text output containing 'warn message', got %q", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Fatalf("expected text level WARN, got %q", out)
	}
}

func TestParseLevelDebug(t *testing.T) {
	if got := parseLevel("debug"); got != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", got)
	}
}

func TestParseLevelInfoDefault(t *testing.T) {
	if got := parseLevel("info"); got != slog.LevelInfo {
		t.Fatalf("level = %v, want info", got)
	}
	if got := parseLevel(""); got != slog.LevelInfo {
		t.Fatalf("level = %v, want info (default)", got)
	}
	if got := parseLevel("unknown"); got != slog.LevelInfo {
		t.Fatalf("level = %v, want info (default)", got)
	}
}

func TestParseLevelWarn(t *testing.T) {
	for _, in := range []string{"warn", "warning"} {
		if got := parseLevel(in); got != slog.LevelWarn {
			t.Fatalf("level(%q) = %v, want warn", in, got)
		}
	}
}

func TestParseLevelError(t *testing.T) {
	if got := parseLevel("error"); got != slog.LevelError {
		t.Fatalf("level = %v, want error", got)
	}
}

func TestParseLevelCaseInsensitive(t *testing.T) {
	if got := parseLevel("DEBUG"); got != slog.LevelDebug {
		t.Fatalf("level = %v, want debug", got)
	}
	if got := parseLevel("  Info  "); got != slog.LevelInfo {
		t.Fatalf("level = %v, want info", got)
	}
}
