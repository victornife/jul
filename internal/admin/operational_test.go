// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/observability"
)

// ── /api/admin/health (Milestone 5.7) ────────────────────────────────────────

func TestConsoleHealthReportsCounts(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	// Drive a few admin requests so the console-health observer records them.
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/healthz", nil))
	}

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/admin/health", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out struct {
		Status   string `json:"status"`
		Requests int64  `json:"requests"`
		SSEConns int    `json:"sse_conns"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Status != "ok" {
		t.Errorf("status = %q, want ok", out.Status)
	}
	if out.Requests < 3 {
		t.Errorf("requests = %d, want >= 3", out.Requests)
	}
}

func TestConsoleHealthMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/admin/health", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// ── /api/admin/client-errors (Milestone 5.7) ─────────────────────────────────

func TestClientErrorReportRedactsAndStores(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	h := s.routes()

	body := `{"message":"Authorization: Bearer leaked","source":"https://x/app.js?token=abc","line":12,"col":4}`
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/client-errors", strings.NewReader(body)))
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}

	errs := s.health.recentClientErrors()
	if len(errs) != 1 {
		t.Fatalf("stored errors = %d, want 1", len(errs))
	}
	if errs[0].Message != "[redacted]" {
		t.Errorf("message = %q, want redacted", errs[0].Message)
	}
	if strings.Contains(errs[0].Source, "token") {
		t.Errorf("source %q must not carry query string", errs[0].Source)
	}
}

func TestClientErrorRejectsBadJSON(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/admin/client-errors", strings.NewReader("{not json")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ── /api/observability/* hooks (Milestones 5.1, 5.2, 5.5, 5.6) ───────────────

func TestRequestSamplesEndpoint(t *testing.T) {
	want := []observability.RequestSample{{Method: "GET", Path: "/x", Status: 200}}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		RequestSamples: func() []observability.RequestSample { return want },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/requests", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got []observability.RequestSample
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].Path != "/x" {
		t.Fatalf("got %+v, want one /x sample", got)
	}
}

func TestRequestSamplesEndpointNilHookReturnsEmptyArray(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/requests", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if strings.TrimSpace(rr.Body.String()) != "[]" {
		t.Fatalf("body = %q, want []", rr.Body.String())
	}
}

func TestFailingRoutesEndpointRespectsLimit(t *testing.T) {
	var gotLimit int
	s := newTestServer(t, config.AdminConfig{}, Deps{
		FailingRoutes: func(n int) []observability.RouteFailure {
			gotLimit = n
			return []observability.RouteFailure{{Path: "/bad", Status5xx: 1}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/failing-routes?limit=5", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if gotLimit != 5 {
		t.Errorf("limit forwarded = %d, want 5", gotLimit)
	}
}

func TestUpstreamHistoryEndpoint(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		UpstreamHealthHistory: func() []observability.BackendHealthHistory {
			return []observability.BackendHealthHistory{{Pool: "p", Backend: "b", Healthy: true}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/upstream-history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

func TestCertHistoryEndpoint(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		CertRenewalHistory: func() []observability.CertRenewalHistory {
			return []observability.CertRenewalHistory{{Domain: "x.test", DaysLeft: 30}}
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/cert-history", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
}

// ── /api/observability/timeline (Milestone 5.4) ──────────────────────────────

func TestTimelineMergesAdminAndRuntimeEvents(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		UpstreamHealthHistory: func() []observability.BackendHealthHistory {
			return []observability.BackendHealthHistory{{
				Pool: "p", Backend: "b",
				Recent: []observability.HealthEvent{{Time: time.Now().Add(-time.Minute), Healthy: false}},
			}}
		},
		CertRenewalHistory: func() []observability.CertRenewalHistory {
			return []observability.CertRenewalHistory{{
				Domain: "x.test",
				Recent: []observability.CertRenewalEvent{{Time: time.Now().Add(-2 * time.Minute), Success: true}},
			}}
		},
	})
	// Seed an admin lifecycle event.
	s.recordTimeline("config", "apply", "info", "applied", "snap1")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/observability/timeline", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var events []TimelineEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// One admin + one upstream + one cert event, newest-first.
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3", len(events))
	}
	if !events[0].Time.After(events[1].Time) && !events[0].Time.Equal(events[1].Time) {
		t.Error("timeline must be sorted newest-first")
	}
}

// ── /api/audit and export (Milestone 6.6) ────────────────────────────────────

func TestAuditRecordAndFilter(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	s.recordAudit("config.apply", "config", "success", "ok", "10.0.0.1")
	s.recordAudit("config.rollback", "config", "failure", "boom", "10.0.0.2")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/audit?result=failure", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var events []AuditEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 1 || events[0].Operation != "config.rollback" {
		t.Fatalf("filtered events = %+v, want one rollback failure", events)
	}
}

func TestAuditRedactsSensitiveDetail(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	s.recordAudit("auth.fail", "", "failure", "Authorization: Bearer sekret", "10.0.0.3")
	events := s.audit.snapshot("", "", 0)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Detail != "[redacted]" {
		t.Errorf("detail = %q, want redacted", events[0].Detail)
	}
}

func TestAuditExportCSV(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	s.recordAudit("config.apply", "config", "success", "applied", "10.0.0.4")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/audit/export?format=csv", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content-type = %q, want text/csv", ct)
	}
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 2 { // header + 1 record
		t.Fatalf("csv rows = %d, want 2", len(rows))
	}
	if rows[0][3] != "operation" {
		t.Errorf("header[3] = %q, want operation", rows[0][3])
	}
}

func TestAuditExportJSONDefault(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	s.recordAudit("config.reload", "config", "success", "", "10.0.0.5")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/audit/export", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q, want application/json", ct)
	}
	var events []AuditEvent
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
}

// ── apply/rollback/reload emit timeline + audit (Milestones 5.4 + 6.6) ───────

func TestApplyEmitsTimelineAndAudit(t *testing.T) {
	s, _ := v2WriteServer(t)
	body := validTOML(t, "./public", ":8080")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", rr.Code)
	}
	// Timeline recorded an apply event.
	tl := s.timeline.snapshot()
	if len(tl) == 0 || tl[0].Type != "apply" {
		t.Fatalf("timeline = %+v, want an apply event", tl)
	}
	// Audit recorded a successful apply.
	au := s.audit.snapshot("config.apply", "success", 0)
	if len(au) != 1 {
		t.Fatalf("audit apply success = %d, want 1", len(au))
	}
}

func TestApplyResponseIncludesReloadBlock(t *testing.T) {
	s, _ := v2WriteServer(t)
	body := validTOML(t, "./public", ":8080")

	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", strings.NewReader(string(body))))
	if rr.Code != http.StatusOK {
		t.Fatalf("apply status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["ok"] != true {
		t.Errorf("ok = %v, want true", out["ok"])
	}
	if out["mode"] != "hot" {
		t.Errorf("mode = %v, want hot", out["mode"])
	}
	if out["version"] == "" {
		t.Error("expected version in response")
	}
}

// TestConcurrentAdminAppliesSerialize (R9-14.3) proves that concurrent
// POST /api/config/apply requests are serialized by applyMu and that both
// leave causal audit/timeline records. The first apply holds the write lock
// until released; the second cannot enter ApplyConfigRaw until then, so the
// final persisted config is the second apply's.
func TestConcurrentAdminAppliesSerialize(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "server.toml")
	initial := validTOML(t, "./public", ":8080")
	if err := os.WriteFile(cfgPath, initial, 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var enteredOnce sync.Once

	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(cfgPath) },
		ApplyConfigRaw: func(data []byte, mode string) (ConfigApplyResult, error) {
			c, err := config.Parse(data)
			if err != nil {
				return ConfigApplyResult{OK: false, Mode: mode}, err
			}
			if err := config.Validate(c); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode}, err
			}
			if err := os.WriteFile(cfgPath, data, 0o644); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode}, err
			}
			enteredOnce.Do(func() { entered <- struct{}{} })
			<-release
			return ConfigApplyResult{OK: true, Mode: mode, Version: configVersion(data), ServingVersion: configVersion(data)}, nil
		},
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(cfgPath)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
	}
	s := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps)

	bodyA := validTOML(t, "./public-a", ":8080")
	bodyB := validTOML(t, "./public-b", ":8080")

	type result struct {
		status int
		err    error
	}
	apply := func(body []byte) <-chan result {
		ch := make(chan result, 1)
		go func() {
			rr := httptest.NewRecorder()
			s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/apply", bytes.NewReader(body)))
			ch <- result{status: rr.Code}
		}()
		return ch
	}

	doneA := apply(bodyA)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first apply did not enter ApplyConfigRaw")
	}

	doneB := apply(bodyB)
	select {
	case <-doneB:
		t.Fatal("second apply completed while first still held the lock")
	case <-time.After(200 * time.Millisecond):
		// Expected: B is blocked waiting for applyMu.
	}

	close(release)

	var resA, resB result
	select {
	case resA = <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("first apply did not complete")
	}
	select {
	case resB = <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("second apply did not complete")
	}

	if resA.status != http.StatusOK {
		t.Fatalf("first apply status = %d, want 200", resA.status)
	}
	if resB.status != http.StatusOK {
		t.Fatalf("second apply status = %d, want 200", resB.status)
	}

	// Final file must be B's body because B wrote after A.
	disk, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read disk: %v", err)
	}
	if !bytes.Contains(disk, []byte("public-b")) {
		t.Fatalf("final config should be from second apply; got:\n%s", disk)
	}

	// Both applies recorded audit events in causal order (A before B).
	au := s.audit.snapshot("config.apply", "success", 0)
	if len(au) != 2 {
		t.Fatalf("audit apply successes = %d, want 2", len(au))
	}
	// snapshot returns newest-first, so au[1] is A and au[0] is B.
	if !au[0].Time.After(au[1].Time) {
		t.Error("audit events are not in causal order")
	}

	// Timeline also records both apply events.
	tl := s.timeline.snapshot()
	var applyEvents int
	for _, ev := range tl {
		if ev.Type == "apply" {
			applyEvents++
		}
	}
	if applyEvents != 2 {
		t.Fatalf("timeline apply events = %d, want 2", applyEvents)
	}
}

// ── /api/config/pending-restart (P2-04) ──────────────────────────────────────

func TestPendingRestartNoDep(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/pending-restart", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pending, _ := out["pending"].(bool); pending {
		t.Error("pending should be false when PendingRestart dep is nil")
	}
}

func TestPendingRestartNoneActive(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		PendingRestart: func() *PendingRestartStatus { return nil },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/pending-restart", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if pending, _ := out["pending"].(bool); pending {
		t.Error("pending should be false when PendingRestart returns nil")
	}
}

func TestPendingRestartActive(t *testing.T) {
	status := &PendingRestartStatus{
		Managed:          true,
		Staged:           true,
		StagedVersion:    "v2",
		ServingVersion:   "v1",
		Subsystems:       []string{"cache"},
		DiscardAvailable: true,
		Inconsistent:     false,
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		PendingRestart: func() *PendingRestartStatus { return status },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/pending-restart", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out struct {
		Pending bool                  `json:"pending"`
		Status  *PendingRestartStatus `json:"status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Pending {
		t.Error("pending should be true when PendingRestart returns a status")
	}
	if out.Status == nil {
		t.Fatal("status should be set")
	}
	if out.Status.StagedVersion != "v2" {
		t.Errorf("staged_version = %q, want v2", out.Status.StagedVersion)
	}
}

func TestPendingRestartMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/pending-restart", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// ── /api/config/pending-restart/discard (P2-04) ───────────────────────────────

func TestDiscardPendingRestartNoDep(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/pending-restart/discard", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501 when dep is nil", rr.Code)
	}
}

func TestDiscardPendingRestartSuccess(t *testing.T) {
	called := false
	s := newTestServer(t, config.AdminConfig{}, Deps{
		DiscardPendingRestart: func() (ConfigApplyResult, error) {
			called = true
			return ConfigApplyResult{OK: true, Mode: "hot", Message: "Planned restart discarded."}, nil
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/pending-restart/discard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if !called {
		t.Error("DiscardPendingRestart dep was not called")
	}
}

func TestDiscardPendingRestartConflict(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{
		DiscardPendingRestart: func() (ConfigApplyResult, error) {
			return ConfigApplyResult{}, errDiscardFailed
		},
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/pending-restart/discard", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 on discard failure", rr.Code)
	}
}

var errDiscardFailed = &discardError{"discard check failed"}

type discardError struct{ msg string }

func (e *discardError) Error() string { return e.msg }

func TestDiscardPendingRestartMethodNotAllowed(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/pending-restart/discard", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
}

// ── /api/runtime/overview pending_restart_status (P2-04) ─────────────────────

func TestRuntimeOverviewIncludesPendingRestartStatus(t *testing.T) {
	status := &PendingRestartStatus{
		Managed:          true,
		Staged:           true,
		StagedVersion:    "staged-v",
		Subsystems:       []string{"cache"},
		DiscardAvailable: true,
	}
	s := newTestServer(t, config.AdminConfig{}, Deps{
		PendingRestart: func() *PendingRestartStatus { return status },
	})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var out struct {
		PendingRestartStatus *PendingRestartStatus `json:"pending_restart_status"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.PendingRestartStatus == nil {
		t.Fatal("pending_restart_status should be set when PendingRestart dep returns a value")
	}
	if out.PendingRestartStatus.StagedVersion != "staged-v" {
		t.Errorf("staged_version = %q, want staged-v", out.PendingRestartStatus.StagedVersion)
	}
}
