// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package observability

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"jul/internal/redact"
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

// ─── DynamicHandler / NewDynamicLogger format hot-reload (#91) ──────────────

func TestNewDynamicLoggerStartsInRequestedFormat(t *testing.T) {
	var buf bytes.Buffer
	logger, _, _ := NewDynamicLogger(&buf, "info", "json")
	logger.Info("hello")
	if !json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("expected valid JSON, got %q", buf.String())
	}
}

func TestSetFormatSwapsTextToJSONToText(t *testing.T) {
	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(&buf, "info", "text")

	logger.Info("first")
	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("expected text (non-JSON) output, got %q", buf.String())
	}
	buf.Reset()

	setFormat("json")
	logger.Info("second")
	line := bytes.TrimSpace(buf.Bytes())
	if !json.Valid(line) {
		t.Fatalf("expected valid JSON after swap to json, got %q", line)
	}
	if !strings.Contains(string(line), `"msg":"second"`) {
		t.Fatalf("expected msg=second, got %q", line)
	}
	buf.Reset()

	setFormat("text")
	logger.Info("third")
	if json.Valid(bytes.TrimSpace(buf.Bytes())) {
		t.Fatalf("expected text output after swap back to text, got %q", buf.String())
	}
	if !strings.Contains(buf.String(), "third") {
		t.Fatalf("expected msg=third, got %q", buf.String())
	}
}

// TestSetFormatPreservesLevel proves log level stays independently
// hot-reloadable before and after a format swap (#91 acceptance criterion).
func TestSetFormatPreservesLevel(t *testing.T) {
	var buf bytes.Buffer
	logger, setLevel, setFormat := NewDynamicLogger(&buf, "warn", "text")

	logger.Info("suppressed before swap")
	if buf.Len() != 0 {
		t.Fatalf("info should be suppressed at warn level, got %q", buf.String())
	}

	setFormat("json")
	logger.Info("still suppressed after format swap")
	if buf.Len() != 0 {
		t.Fatalf("info should still be suppressed after format swap, got %q", buf.String())
	}

	setLevel("info")
	logger.Info("now visible")
	if !strings.Contains(buf.String(), "now visible") {
		t.Fatalf("expected info to be visible after level change, got %q", buf.String())
	}
}

// TestDynamicHandlerWithAttrsFollowsSwap proves a logger derived via With(...)
// before a format swap keeps its attributes and observes the new format.
func TestDynamicHandlerWithAttrsFollowsSwap(t *testing.T) {
	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(&buf, "info", "text")
	derived := logger.With("component", "cache")

	setFormat("json")
	derived.Info("evicted")

	var rec map[string]any
	line := bytes.TrimSpace(buf.Bytes())
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", line, err)
	}
	if rec["component"] != "cache" {
		t.Fatalf("expected component=cache attribute to survive the swap, got %v", rec)
	}
	if rec["msg"] != "evicted" {
		t.Fatalf("expected msg=evicted, got %v", rec)
	}
}

// TestDynamicHandlerWithGroupFollowsSwap proves a nested WithGroup logger
// created before a format swap still nests correctly afterward.
func TestDynamicHandlerWithGroupFollowsSwap(t *testing.T) {
	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(&buf, "info", "text")
	derived := logger.WithGroup("request").With("id", "abc123")

	setFormat("json")
	derived.Info("handled")

	var rec map[string]any
	line := bytes.TrimSpace(buf.Bytes())
	if err := json.Unmarshal(line, &rec); err != nil {
		t.Fatalf("expected valid JSON, got %q: %v", line, err)
	}
	group, ok := rec["request"].(map[string]any)
	if !ok {
		t.Fatalf("expected a nested \"request\" group, got %v", rec)
	}
	if group["id"] != "abc123" {
		t.Fatalf("expected request.id=abc123, got %v", rec)
	}
}

// TestDynamicHandlerInterleavedGroupsAndAttrs proves ops replay in the exact
// call order (WithGroup then WithAttrs then WithGroup again), matching slog's
// documented nesting contract, both before and after a format swap.
func TestDynamicHandlerInterleavedGroupsAndAttrs(t *testing.T) {
	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(&buf, "info", "json")
	derived := logger.WithGroup("outer").With("a", 1).WithGroup("inner").With("b", 2)
	derived.Info("nested")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("invalid JSON before swap: %v", err)
	}
	outer, ok := rec["outer"].(map[string]any)
	if !ok || outer["a"] != float64(1) {
		t.Fatalf("expected outer.a=1, got %v", rec)
	}
	inner, ok := outer["inner"].(map[string]any)
	if !ok || inner["b"] != float64(2) {
		t.Fatalf("expected outer.inner.b=2, got %v", rec)
	}

	buf.Reset()
	setFormat("text")
	derived.Info("nested again")
	out := buf.String()
	if !strings.Contains(out, "outer.a=1") || !strings.Contains(out, "outer.inner.b=2") {
		t.Fatalf("expected nested group prefixes in text output, got %q", out)
	}
}

// TestDynamicHandlerNoDuplicateRecordPerCall proves a single log call is
// handled by exactly one delegate, never both old and new.
func TestDynamicHandlerNoDuplicateRecordPerCall(t *testing.T) {
	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(&buf, "info", "text")
	setFormat("json")
	logger.Info("once")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one record, got %d: %q", len(lines), buf.String())
	}
}

// TestDynamicHandlerConcurrentSwapsRaceFree drives concurrent logging on
// several derived handlers while the format is repeatedly swapped. Run with
// -race. Every emitted line must be independently valid JSON or valid text —
// never a mixed/truncated record — and no goroutine may crash.
func TestDynamicHandlerConcurrentSwapsRaceFree(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer
	safeWriter := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	logger, setLevel, setFormat := NewDynamicLogger(safeWriter, "info", "text")

	var wg sync.WaitGroup
	stop := make(chan struct{})

	loggers := []*slog.Logger{
		logger,
		logger.With("component", "a"),
		logger.WithGroup("g").With("component", "b"),
	}
	for _, l := range loggers {
		wg.Add(1)
		go func(l *slog.Logger) {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				l.Info("concurrent message")
			}
		}(l)
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			if i%2 == 0 {
				setFormat("json")
			} else {
				setFormat("text")
			}
			setLevel("info")
		}
		// The swap loop is the only bounded goroutine; once it's done, signal
		// the unbounded logger goroutines above to stop.
		close(stop)
	}()

	wg.Wait()

	// Every emitted line must be independently parseable as either valid JSON
	// or valid non-empty text — never a byte-level mix of both from a torn
	// record.
	mu.Lock()
	defer mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "{") {
			if !json.Valid([]byte(line)) {
				t.Fatalf("torn/invalid JSON record: %q", line)
			}
		} else if !strings.Contains(line, "concurrent message") {
			t.Fatalf("torn/invalid text record: %q", line)
		}
	}
}

// TestDynamicHandlerRedactsAcrossFormatSwap proves a secret registered with
// internal/redact is masked identically in both text and JSON output, and
// stays masked across a format swap (#91 acceptance criterion).
func TestDynamicHandlerRedactsAcrossFormatSwap(t *testing.T) {
	const secret = "super-secret-token-value-xyz"
	redact.Add(secret)
	t.Cleanup(func() { redact.Replace(map[string]struct{}{}) })

	var buf bytes.Buffer
	logger, _, setFormat := NewDynamicLogger(redact.Writer(&buf), "info", "text")

	logger.Info("connecting", "token", secret)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("secret leaked in text output: %q", buf.String())
	}
	buf.Reset()

	setFormat("json")
	logger.Info("connecting", "token", secret)
	if strings.Contains(buf.String(), secret) {
		t.Fatalf("secret leaked in JSON output after swap: %q", buf.String())
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
