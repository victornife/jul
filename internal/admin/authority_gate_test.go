// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

// fileOwnedDeps builds a Deps whose Authority reports file_owned and whose
// mutation entry points count invocations, so a test can assert they were
// never reached (ADR 0019 §15: denial happens before any side effect).
type authorityGateCounters struct {
	applyConfigRaw atomic.Int64
	applyConfig    atomic.Int64
	discardPending atomic.Int64
	readConfigRaw  atomic.Int64
	writeConfigRaw atomic.Int64
}

func newFileOwnedTestServer(t *testing.T, counters *authorityGateCounters) *Server {
	t.Helper()
	deps := Deps{
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "file_owned", Source: "default"}
		},
		LoadConfig: func() (*config.Config, error) {
			return config.Parse([]byte(`[[servers]]
listen = ":8080"

[[servers.locations]]
proxy_pass = "http://127.0.0.1:9001"

[servers.locations.match]
type = "prefix"
path = "/"
`))
		},
		ReadConfigRaw: func() ([]byte, error) {
			counters.readConfigRaw.Add(1)
			return []byte(`[[servers]]
listen = ":8080"

[[servers.locations]]
proxy_pass = "http://127.0.0.1:9001"

[servers.locations.match]
type = "prefix"
path = "/"
`), nil
		},
		WriteConfigRaw: func([]byte) error {
			counters.writeConfigRaw.Add(1)
			return nil
		},
		ApplyConfigRaw: func(ApplyRequestContext, []byte, string) (ConfigApplyResult, error) {
			counters.applyConfigRaw.Add(1)
			return ConfigApplyResult{OK: true}, nil
		},
		ApplyConfig: func(ApplyRequestContext, *config.Config, string) (ConfigApplyResult, error) {
			counters.applyConfig.Add(1)
			return ConfigApplyResult{OK: true}, nil
		},
		DiscardPendingRestart: func() (ConfigApplyResult, error) {
			counters.discardPending.Add(1)
			return ConfigApplyResult{OK: true}, nil
		},
	}
	return newTestServer(t, config.AdminConfig{}, deps)
}

func assertConfigAuthorityDenial(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	var body configAuthorityErrorEnvelope
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode error envelope: %v; body=%s", err, rr.Body.String())
	}
	if body.Error.Code != configAuthorityErrorCode {
		t.Fatalf("error.code = %q, want %q", body.Error.Code, configAuthorityErrorCode)
	}
	if body.Error.Details["config_authority"] != "file_owned" {
		t.Errorf("error.details.config_authority = %q, want file_owned", body.Error.Details["config_authority"])
	}
}

func TestFileOwnedDeniesRawApplyBeforeSideEffect(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader([]byte(`[[servers]]
listen = ":9090"
`))))
	assertConfigAuthorityDenial(t, rr)
	if n := counters.applyConfigRaw.Load(); n != 0 {
		t.Errorf("ApplyConfigRaw called %d times, want 0", n)
	}
	if n := counters.writeConfigRaw.Load(); n != 0 {
		t.Errorf("WriteConfigRaw called %d times, want 0", n)
	}
}

func TestFileOwnedDeniesSettingsApply(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/settings", bytes.NewReader([]byte(`{}`))))
	assertConfigAuthorityDenial(t, rr)
	if n := counters.applyConfig.Load(); n != 0 {
		t.Errorf("ApplyConfig called %d times, want 0", n)
	}
}

func TestFileOwnedDeniesPatchApply(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	body, _ := json.Marshal(patchApplyRequest{Ops: []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9000"}}})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/apply", bytes.NewReader(body)))
	assertConfigAuthorityDenial(t, rr)
	if n := counters.applyConfig.Load(); n != 0 {
		t.Errorf("ApplyConfig called %d times, want 0", n)
	}
}

func TestFileOwnedDeniesRollbackBeforeSideEffect(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	body, _ := json.Marshal(map[string]string{"id": "20260101T000000.000Z"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/rollback", bytes.NewReader(body)))
	assertConfigAuthorityDenial(t, rr)
	if n := counters.applyConfigRaw.Load(); n != 0 {
		t.Errorf("ApplyConfigRaw called %d times, want 0", n)
	}
}

func TestFileOwnedDeniesDiscardPendingRestartBeforeHistoryRead(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/pending-restart/discard", nil))
	assertConfigAuthorityDenial(t, rr)
	if n := counters.discardPending.Load(); n != 0 {
		t.Errorf("DiscardPendingRestart called %d times, want 0", n)
	}
	// The handler reads the staged bytes into history BEFORE discarding; that
	// read must not happen either once the denial is the first statement.
	if n := counters.readConfigRaw.Load(); n != 0 {
		t.Errorf("ReadConfigRaw called %d times, want 0 (history write is a side effect)", n)
	}
}

func TestFileOwnedAllowsPreviewAndStatus(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	// Preview/validate/status/history-list must remain available.
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/status", nil),
		httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil),
		httptest.NewRequest(http.MethodGet, "/api/config/pending-restart", nil),
	} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("%s %s = %d, want 200 (read-only surfaces stay available in file_owned mode); body=%s", req.Method, req.URL.Path, rr.Code, rr.Body.String())
		}
	}

	body, _ := json.Marshal(patchApplyRequest{Ops: []patchRequest{{Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/", Target: "http://127.0.0.1:9000"}}})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Errorf("patch preview = %d, want 200 (preview stays allowed); body=%s", rr.Code, rr.Body.String())
	}
}

func TestRuntimeOverviewSurfacesAuthority(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/runtime/overview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("overview status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var out RuntimeOverview
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode overview: %v", err)
	}
	if out.Authority == nil || out.Authority.Mode != "file_owned" {
		t.Fatalf("overview.authority = %+v, want mode file_owned", out.Authority)
	}
}

func TestManagedModeAllowsMutation(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	s.deps.Authority = func() ConfigAuthorityStatus {
		return ConfigAuthorityStatus{Mode: "managed", Source: "explicit"}
	}
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/raw", bytes.NewReader([]byte(`[[servers]]
listen = ":9090"
`))))
	if rr.Code != http.StatusOK {
		t.Fatalf("managed-mode raw apply = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if n := counters.applyConfigRaw.Load(); n != 1 {
		t.Errorf("ApplyConfigRaw called %d times, want 1", n)
	}
}

// TestRefreshAuthorityDriftNotWiredIs501 pins that the explicit-refresh
// endpoint (ADR 0019 §12's fourth event-driven drift trigger) degrades to 501
// rather than a panic or a silently-stale 200 when the hook is not wired.
func TestRefreshAuthorityDriftNotWiredIs501(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/authority/refresh", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rr.Code, rr.Body.String())
	}
}

// TestRefreshAuthorityDriftInvokesHookAndReturnsStatus pins that the endpoint
// calls the refresh hook exactly once per request and returns its result,
// distinct from a passive read of Deps.Authority.
func TestRefreshAuthorityDriftInvokesHookAndReturnsStatus(t *testing.T) {
	counters := &authorityGateCounters{}
	s := newFileOwnedTestServer(t, counters)
	var calls atomic.Int64
	s.deps.RefreshAuthorityDrift = func() ConfigAuthorityStatus {
		calls.Add(1)
		return ConfigAuthorityStatus{Mode: "managed", Source: "explicit", Drift: true}
	}
	h := s.routes()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/authority/refresh", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out ConfigAuthorityStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.Drift || out.Mode != "managed" {
		t.Errorf("response = %+v, want the hook's fresh status", out)
	}
	if calls.Load() != 1 {
		t.Errorf("hook called %d times, want 1", calls.Load())
	}
	// GET must not be accepted: this is an action, not a cached read.
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/config/authority/refresh", nil))
	if rr2.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rr2.Code)
	}
}
