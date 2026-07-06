// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
)

// ── /api/observability/logs (Phase 4g) ──────────────────────────────────────

func TestLogsEndpointReturnsEntriesAndRespectsLimit(t *testing.T) {
	var gotLimit int
	s := newTestServer(t, config.AdminConfig{}, Deps{
		RecentLogs: func(limit int) []observability.LogEntry {
			gotLimit = limit
			return []observability.LogEntry{{Method: "GET", Path: "/x", Status: 200}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/logs?limit=5", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotLimit != 5 {
		t.Errorf("limit = %d, want 5", gotLimit)
	}
	var got []observability.LogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/x" {
		t.Fatalf("got %+v, want one /x entry", got)
	}
}

func TestLogsEndpointDefaultLimit(t *testing.T) {
	var gotLimit int
	s := newTestServer(t, config.AdminConfig{}, Deps{
		RecentLogs: func(limit int) []observability.LogEntry {
			gotLimit = limit
			return nil
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/logs", nil))
	if gotLimit != logTailDefaultLimit {
		t.Errorf("limit = %d, want default %d", gotLimit, logTailDefaultLimit)
	}
}

func TestLogsEndpointNilHookReturnsEmptyArray(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/logs", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("body = %q, want []", rr.Body.String())
	}
}

func TestLogsEndpointMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/observability/logs", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

func TestLogsStreamUnavailableWithoutHook(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/logs/stream", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestLogsStreamEmitsBacklogThenLive(t *testing.T) {
	live := make(chan observability.LogEntry, 4)
	var cancelled atomic.Bool
	s := newTestServer(t, config.AdminConfig{}, Deps{
		RecentLogs: func(limit int) []observability.LogEntry {
			// Snapshot is newest-first; the handler replays it oldest-first.
			return []observability.LogEntry{{Method: "GET", Path: "/a", Status: 200, Time: time.Now()}}
		},
		SubscribeLogs: func() (<-chan observability.LogEntry, func()) {
			return live, func() { cancelled.Store(true) }
		},
	})
	srv := httptest.NewServer(s.routes())
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Safety net so a stuck stream never hangs the suite.
	time.AfterFunc(5*time.Second, cancel)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/observability/logs/stream", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	readEvent := func() (string, observability.LogEntry) {
		t.Helper()
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				t.Fatalf("read frame: %v", err)
			}
			line = strings.TrimRight(line, "\r\n")
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			var ev struct {
				Type string          `json:"type"`
				Data json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("decode frame: %v", err)
			}
			var e observability.LogEntry
			if len(ev.Data) > 0 {
				_ = json.Unmarshal(ev.Data, &e)
			}
			return ev.Type, e
		}
	}

	if typ, _ := readEvent(); typ != "connected" {
		t.Fatalf("first frame type = %q, want connected", typ)
	}
	if typ, e := readEvent(); typ != "log" || e.Path != "/a" {
		t.Fatalf("backlog frame = %q %q, want log /a", typ, e.Path)
	}

	live <- observability.LogEntry{Method: "GET", Path: "/b", Status: 201, Time: time.Now()}
	if typ, e := readEvent(); typ != "log" || e.Path != "/b" {
		t.Fatalf("live frame = %q %q, want log /b", typ, e.Path)
	}

	cancel()
	// Give the handler a moment to observe the cancellation and unsubscribe.
	deadline := time.Now().Add(2 * time.Second)
	for !cancelled.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cancelled.Load() {
		t.Error("stream did not unsubscribe on disconnect")
	}
}
