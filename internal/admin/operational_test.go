package admin

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
