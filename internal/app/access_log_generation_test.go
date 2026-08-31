// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

// These tests pin #98's per-generation access-log sink lifecycle: a candidate
// sink set is built and validated before Publish, committed atomically with
// the new handler generation, and the previous generation's file resources
// close only after its own requests have been served — proven here via
// HandlerFactory.Prepare/commitFn/retirePrev directly, without a full process
// boot or network bind (matching this file's sibling factory_test.go).
package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"jul/internal/config"
)

// accessLogTarget is config.ProxyTarget plus an [observability.access_log]
// block, for tests that need to observe what a real request writes.
func accessLogTarget(t *testing.T, al config.AccessLogConfig) *config.Config {
	t.Helper()
	cfg := config.ProxyTarget("127.0.0.1:9001", ":0")
	cfg.Observability.AccessLog = al
	return cfg
}

// commitAndServe prepares cfg's generation, commits it, serves one GET / and
// returns the retire callback for the previous generation (nil on the first
// commit). The caller must invoke it once the previous generation should stop
// being relied on (simulating drain).
func commitAndServe(t *testing.T, f *HandlerFactory, cfg *config.Config) func() {
	t.Helper()
	handlers, _, commitFn, abortFn, err := f.Prepare(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	_, retirePrev := commitFn()
	abortFn() // safe no-op after commit

	h := handlers[":0"]
	if h == nil {
		for _, hh := range handlers {
			h = hh
			break
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	if retirePrev == nil {
		return func() {}
	}
	return retirePrev
}

func TestAccessLogHotAppliesFileSink(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	cfg := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "text"})

	retire := commitAndServe(t, f, cfg)
	retire()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	if !strings.Contains(string(data), "method=GET") {
		t.Errorf("file sink missing the served request: %q", data)
	}
}

func TestAccessLogCandidateRejectsUnknownSinkBeforePublish(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	cfg := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"bogus"}})
	_, _, _, _, err := f.Prepare(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected Prepare to reject an unknown access-log sink")
	}
	// The mutex must still be released: a subsequent Prepare must proceed.
	good := config.ProxyTarget("127.0.0.1:9001", ":0")
	if _, _, _, abortFn, err := f.Prepare(context.Background(), good); err != nil {
		t.Fatalf("Prepare after a rejected candidate failed: %v", err)
	} else {
		abortFn()
	}
}

func TestAccessLogCandidateAbortLeavesNoRealFile(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	// "file" then an unknown sink: the file sink's writability probe must not
	// touch the real path, and the whole candidate must be rejected before
	// Publish (#98's fix for the pre-existing ensureWritable side effect).
	cfg := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file", "bogus"}, File: path})
	if _, _, _, _, err := f.Prepare(context.Background(), cfg); err == nil {
		t.Fatal("expected Prepare to reject the candidate")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a rejected candidate created the real access-log file: stat err = %v", err)
	}
}

func TestAccessLogFilePathSwitchLeavesOldFileIntact(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	dir := t.TempDir()
	pathA := filepath.Join(dir, "a.log")
	pathB := filepath.Join(dir, "b.log")

	cfgA := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file"}, File: pathA})
	retireA := commitAndServe(t, f, cfgA)

	dataA1, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read a.log: %v", err)
	}
	if !strings.Contains(string(dataA1), "method=GET") {
		t.Fatalf("a.log missing the first request: %q", dataA1)
	}

	cfgB := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file"}, File: pathB})
	retireB := commitAndServe(t, f, cfgB)
	// Only now does the previous (A) generation retire, simulating its
	// in-flight requests having drained.
	retireA()

	dataA2, err := os.ReadFile(pathA)
	if err != nil {
		t.Fatalf("read a.log after switch: %v", err)
	}
	if string(dataA1) != string(dataA2) {
		t.Fatalf("a.log changed after the path switch to b.log:\nbefore: %q\nafter:  %q", dataA1, dataA2)
	}
	dataB, err := os.ReadFile(pathB)
	if err != nil {
		t.Fatalf("read b.log: %v", err)
	}
	if !strings.Contains(string(dataB), "method=GET") {
		t.Fatalf("b.log missing the second request: %q", dataB)
	}
	retireB()
}

func TestAccessLogFormatSwitchOnSamePath(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")

	cfgText := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "text"})
	retireText := commitAndServe(t, f, cfgText)

	cfgJSON := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"file"}, File: path, Format: "json"})
	retireJSON := commitAndServe(t, f, cfgJSON)
	retireText()
	retireJSON()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	if !strings.Contains(string(data), "method=GET") {
		t.Errorf("missing text-format record: %q", data)
	}
	if !strings.Contains(string(data), `"method":"GET"`) {
		t.Errorf("missing json-format record: %q", data)
	}
}

func TestAccessLogDisabledOpensNoFileAndCommitsCleanly(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "access.log")
	cfg := accessLogTarget(t, config.AccessLogConfig{Enabled: config.Bool(false), Sinks: []string{"file"}, File: path})

	retire := commitAndServe(t, f, cfg)
	retire()

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("disabled access log created %q: %v", path, err)
	}
}

func TestAccessLogLogTailReceivesRecordsAcrossGenerations(t *testing.T) {
	f, cleanup := minimalFactory(t)
	defer cleanup()

	before := f.AccessLogTail.Snapshot(0)

	cfg1 := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"stdout"}})
	retire1 := commitAndServe(t, f, cfg1)
	retire1()

	cfg2 := accessLogTarget(t, config.AccessLogConfig{Sinks: []string{"stdout"}})
	retire2 := commitAndServe(t, f, cfg2)
	retire2()

	after := f.AccessLogTail.Snapshot(0)
	if len(after) < len(before)+2 {
		t.Fatalf("LogTail recorded %d new entries across two generations, want at least 2 (before=%d after=%d)",
			len(after)-len(before), len(before), len(after))
	}
}
