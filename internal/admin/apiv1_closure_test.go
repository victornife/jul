// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"jul/internal/adminapi"
	"jul/internal/config"
	julserver "jul/internal/server"
)

func v1Request(t *testing.T, s *Server, method, path, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	s.routes().ServeHTTP(rr, req)
	return rr
}

func TestV1RequestAdmissionExhaustive(t *testing.T) {
	t.Run("accepted media type parameters", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("[global]\n"))
		req.Header.Set("Content-Type", "application/toml; charset=utf-8")
		body, apiErr := readV1TOML(rr, req)
		if apiErr != nil || string(body) != "[global]\n" {
			t.Fatalf("body=%q err=%v", body, apiErr)
		}
	})

	for _, contentType := range []string{"", "application/xml", "not a media type"} {
		t.Run("unsupported "+contentType, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
			_, apiErr := readV1JSON(rr, req, &map[string]any{})
			if apiErr == nil || apiErr.Code != adminapi.CodeUnsupportedMediaType {
				t.Fatalf("err = %#v", apiErr)
			}
		})
	}

	t.Run("body too large", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("x", int(v1MaxBodyBytes)+1)))
		req.Header.Set("Content-Type", "text/plain")
		_, apiErr := readV1TOML(rr, req)
		if apiErr == nil || apiErr.Code != adminapi.CodePayloadTooLarge || apiErr.Details.LimitBytes == nil {
			t.Fatalf("err = %#v", apiErr)
		}
	})

	t.Run("reader failure", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(errorReader{})
		_, apiErr := readV1JSON(rr, req, &map[string]any{})
		if apiErr == nil || apiErr.Code != adminapi.CodeInvalidRequest {
			t.Fatalf("err = %#v", apiErr)
		}
	})

	t.Run("strict JSON", func(t *testing.T) {
		type request struct {
			Name string `json:"name"`
		}
		for _, tc := range []struct {
			name string
			body string
		}{
			{"malformed", `{"name":`},
			{"unknown", `{"name":"ok","extra":1}`},
			{"trailing object", `{"name":"ok"} {"name":"second"}`},
			{"trailing garbage", `{"name":"ok"} nope`},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rr := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/json")
				_, apiErr := readV1JSON(rr, req, &request{})
				if apiErr == nil || apiErr.Code != adminapi.CodeInvalidRequest {
					t.Fatalf("err = %#v", apiErr)
				}
			})
		}

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"name":"ok"}`))
		req.Header.Set("Content-Type", "application/json")
		var got request
		body, apiErr := readV1JSON(rr, req, &got)
		if apiErr != nil || got.Name != "ok" || len(body) == 0 {
			t.Fatalf("got=%+v body=%q err=%v", got, body, apiErr)
		}
		restoreRequestBody(req, body)
		roundTrip, err := io.ReadAll(req.Body)
		if err != nil || string(roundTrip) != string(body) || req.ContentLength != int64(len(body)) {
			t.Fatalf("restored body=%q len=%d err=%v", roundTrip, req.ContentLength, err)
		}
	})

	t.Run("mandatory base version", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		if _, apiErr := requiredBaseVersion(req); apiErr == nil || apiErr.Code != adminapi.CodeInvalidRequest {
			t.Fatalf("missing base error = %#v", apiErr)
		}
		req = httptest.NewRequest(http.MethodPost, "/api/v1/config/apply?base_version=%20abc%20", nil)
		base, apiErr := requiredBaseVersion(req)
		if apiErr != nil || base != "abc" {
			t.Fatalf("base=%q err=%v", base, apiErr)
		}
	})
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestV1ApplyResponseProjectionExhaustive(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{BootID: func() string { return "boot-test" }})
	stagedAt := time.Date(2026, 9, 9, 18, 0, 0, 0, time.UTC).Format(time.RFC3339)
	result := ConfigApplyResult{
		ApplyID:             "rl_aaaaaaaaaaaa_1",
		OK:                  true,
		Mode:                "stage_restart",
		Version:             "version-fallback",
		PersistedVersion:    "persisted",
		DesiredVersion:      "desired",
		ServingVersion:      "serving",
		FinalDiskVersion:    "final-disk",
		FinalServingVersion: "final-serving",
		ConfigState:         "managed_clean",
		Origin:              "drift",
		RestartRequired:     true,
		CanStage:            true,
		Restored:            true,
		RestoreError:        "restore degraded",
		TimedOutPhase:       "preflight",
		Degraded: []DegradedEntry{
			{Kind: "history_error", Message: "history degraded"},
			{Kind: "baseline_error", Message: "baseline degraded"},
		},
		PendingRestart: &PendingRestartStatus{
			State: "managed_staged", StagedAt: stagedAt, StagedVersion: "staged",
			PersistedVersion: "persisted", ServingVersion: "serving",
			Subsystems: []string{"listener", "cache"}, DiscardAvailable: true,
		},
	}
	got := s.v1ConfigApplyResponse(result, http.StatusAccepted)
	if got.Terminal || got.State != "pending" || got.Outcome != "staged" || got.BootID != "boot-test" {
		t.Fatalf("pending projection = %+v", got)
	}
	if got.PersistedVersion != "final-disk" || got.ServingVersion != "final-serving" || got.PendingRestart == nil {
		t.Fatalf("versions/pending = %+v", got)
	}
	if len(got.Degraded) != 2 || len(got.PendingRestart.Subsystems) != 2 {
		t.Fatalf("degraded/pending = %+v", got)
	}

	reload := &julserver.ReloadResult{Outcome: julserver.ReloadAppliedLive}
	got = s.v1ConfigApplyResponse(ConfigApplyResult{OK: true, Mode: "hot", Version: "v", Reload: reload}, http.StatusOK)
	if !got.Terminal || got.State != "terminal" || got.Outcome != string(julserver.ReloadAppliedLive) || got.PersistedVersion != "v" {
		t.Fatalf("terminal reload projection = %+v", got)
	}
	if v1PendingRestartFromApply(nil) != nil {
		t.Fatal("nil pending restart did not stay nil")
	}
}

func TestV1MutationCaptureAndCanonicalErrorMapping(t *testing.T) {
	s := newTestServer(t, config.AdminConfig{}, Deps{})

	t.Run("valid mutation projection", func(t *testing.T) {
		cap := newV1Capture()
		writeJSON(cap, http.StatusAccepted, ConfigApplyResult{ApplyID: "rl_aaaaaaaaaaaa_1", OK: true, Mode: "hot", Version: "v"})
		req := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		s.projectV1MutationCapture(cap, req)
		var got adminapi.ConfigApplyResponse
		if err := json.Unmarshal(cap.body.Bytes(), &got); err != nil || got.ApplyID == "" || got.Terminal {
			t.Fatalf("capture=%s got=%+v err=%v", cap.body.String(), got, err)
		}
	})

	t.Run("invalid mutation payload becomes stable internal error", func(t *testing.T) {
		cap := newV1Capture()
		cap.WriteHeader(http.StatusOK)
		_, _ = cap.Write([]byte("not-json"))
		s.projectV1MutationCapture(cap, httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil))
		if cap.status != http.StatusInternalServerError {
			t.Fatalf("status=%d body=%s", cap.status, cap.body.String())
		}
		cap.reset()
		if cap.status != 0 || cap.body.Len() != 0 || len(cap.header) != 0 {
			t.Fatalf("reset left state: %#v", cap)
		}
	})

	cases := []struct {
		name       string
		status     int
		body       any
		wantStatus int
		wantCode   adminapi.Code
	}{
		{"bad request message", http.StatusBadRequest, map[string]any{"message": "bad"}, http.StatusBadRequest, adminapi.CodeInvalidRequest},
		{"bad request error", http.StatusBadRequest, map[string]any{"error": "bad"}, http.StatusBadRequest, adminapi.CodeInvalidRequest},
		{"forbidden", http.StatusForbidden, map[string]any{"error": "secret detail"}, http.StatusForbidden, adminapi.CodeForbidden},
		{"not found", http.StatusNotFound, map[string]any{"error": "missing"}, http.StatusNotFound, adminapi.CodeNotFound},
		{"stale", http.StatusConflict, map[string]any{"current_version": "new", "message": "stale"}, http.StatusConflict, adminapi.CodeStaleBaseVersion},
		{"admin confirmation", http.StatusConflict, map[string]any{"admin_change": true, "changes": []string{"listen", "token"}, "message": "confirm"}, http.StatusConflict, adminapi.CodeAdminReachabilityConf},
		{"restart", http.StatusConflict, map[string]any{"restart_required": true, "subsystems": []string{"listener"}, "message": "restart"}, http.StatusConflict, adminapi.CodeRestartRequired},
		{"drift", http.StatusConflict, map[string]any{"message": "drift"}, http.StatusConflict, adminapi.CodeDriftDetected},
		{"not implemented", http.StatusNotImplemented, map[string]any{}, http.StatusNotImplemented, adminapi.CodeNotImplemented},
		{"unavailable", http.StatusServiceUnavailable, map[string]any{"error": "storage"}, http.StatusServiceUnavailable, adminapi.CodeStorageUnavailable},
		{"request timeout", http.StatusRequestTimeout, map[string]any{"error": "timeout"}, http.StatusGatewayTimeout, adminapi.CodeOperationTimeout},
		{"gateway timeout", http.StatusGatewayTimeout, map[string]any{"error": "timeout"}, http.StatusGatewayTimeout, adminapi.CodeOperationTimeout},
		{"default", http.StatusTeapot, map[string]any{"error": "teapot"}, http.StatusInternalServerError, adminapi.CodeInternalError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
			s.runCanonicalV1(rr, req, "base", func(w http.ResponseWriter, _ *http.Request) {
				writeJSON(w, tc.status, tc.body)
			})
			if rr.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			env := decodeEnvelope(t, rr)
			if env.Error.Code != tc.wantCode {
				t.Fatalf("code=%q want=%q body=%s", env.Error.Code, tc.wantCode, rr.Body.String())
			}
		})
	}

	t.Run("already canonical API error passes through", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
		s.runCanonicalV1(rr, req, "", func(w http.ResponseWriter, r *http.Request) {
			writeAPIError(w, r, adminapi.Errorf(adminapi.CodeValidationFailed, "invalid"))
		})
		if rr.Code != http.StatusBadRequest || decodeEnvelope(t, rr).Error.Code != adminapi.CodeValidationFailed {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("success copies headers and body", func(t *testing.T) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
		s.runCanonicalV1(rr, req, "", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		if rr.Code != http.StatusAccepted || rr.Header().Get("Retry-After") != "3" || !strings.Contains(rr.Body.String(), `"ok":true`) {
			t.Fatalf("status=%d headers=%v body=%s", rr.Code, rr.Header(), rr.Body.String())
		}
	})

	if got, ok := stringSlice([]any{"a", "b"}); !ok || len(got) != 2 {
		t.Fatalf("stringSlice success=%v %v", got, ok)
	}
	if _, ok := stringSlice("not-a-slice"); ok {
		t.Fatal("stringSlice accepted non-slice")
	}
	if _, ok := stringSlice([]any{"a", 3}); ok {
		t.Fatal("stringSlice accepted non-string member")
	}
}

func TestV1WriteHandlersProtocolAndSideEffectFreeOperations(t *testing.T) {
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	raw := append([]byte(nil), seed...)
	reads := 0
	writes := 0
	s := newTestServer(t, config.AdminConfig{}, Deps{
		ReadConfigRaw: func() ([]byte, error) {
			reads++
			return append([]byte(nil), raw...), nil
		},
		WriteConfigRaw: func(data []byte) error {
			writes++
			raw = append(raw[:0], data...)
			return nil
		},
		LoadConfig: func() (*config.Config, error) { return config.Parse(raw) },
		AdoptExternalPreview: func() (AdoptPreviewResult, error) {
			return AdoptPreviewResult{OK: true, Origin: "drift", ObservedDigest: "abc", BaseVersion: "base"}, nil
		},
	})
	state, err := s.currentWriteState(false)
	if err != nil {
		t.Fatalf("state: %v", err)
	}

	beforeWrites := writes
	for _, tc := range []struct {
		name        string
		path        string
		contentType string
		body        string
	}{
		{"validate", "/api/v1/config/validate", "application/toml", string(seed)},
		{"plan", "/api/v1/config/plan?base_version=" + state.Version, "application/toml", string(seed)},
		{"route test", "/api/v1/routes/test", "application/json", `{"method":"GET","path":"/","host":"example.com"}`},
		{"adopt preview", "/api/v1/config/adopt-external/preview", "application/json", `{"observed_digest":"abc","base_version":"base"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := v1Request(t, s, http.MethodPost, tc.path, tc.contentType, []byte(tc.body))
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
	if writes != beforeWrites {
		t.Fatalf("side-effect-free operations wrote %d times", writes-beforeWrites)
	}
	if reads == 0 {
		t.Fatal("plan/route test did not use the authoritative config")
	}

	t.Run("validation failure", func(t *testing.T) {
		rr := v1Request(t, s, http.MethodPost, "/api/v1/config/validate", "application/toml", []byte("not toml = ["))
		if rr.Code != http.StatusBadRequest || decodeEnvelope(t, rr).Error.Code != adminapi.CodeValidationFailed {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("plan stale", func(t *testing.T) {
		rr := v1Request(t, s, http.MethodPost, "/api/v1/config/plan?base_version=stale", "application/toml", seed)
		if rr.Code != http.StatusConflict || decodeEnvelope(t, rr).Error.Code != adminapi.CodeStaleBaseVersion {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("route test defaults path", func(t *testing.T) {
		rr := v1Request(t, s, http.MethodPost, "/api/v1/routes/test", "application/json", []byte(`{"method":"GET","host":"example.com"}`))
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("adopt preview unavailable", func(t *testing.T) {
		u := newTestServer(t, config.AdminConfig{}, Deps{})
		rr := v1Request(t, u, http.MethodPost, "/api/v1/config/adopt-external/preview", "application/json", []byte(`{}`))
		if rr.Code != http.StatusNotImplemented || decodeEnvelope(t, rr).Error.Code != adminapi.CodeNotImplemented {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
	t.Run("adopt preview drift failure", func(t *testing.T) {
		u := newTestServer(t, config.AdminConfig{}, Deps{AdoptExternalPreview: func() (AdoptPreviewResult, error) {
			return AdoptPreviewResult{}, errors.New("drift")
		}})
		rr := v1Request(t, u, http.MethodPost, "/api/v1/config/adopt-external/preview", "application/json", []byte(`{}`))
		if rr.Code != http.StatusConflict || decodeEnvelope(t, rr).Error.Code != adminapi.CodeDriftDetected {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	for _, path := range []string{
		"/api/v1/config/validate", "/api/v1/config/plan", "/api/v1/routes/test", "/api/v1/config/patch",
		"/api/v1/config/apply", "/api/v1/config/patch/apply", "/api/v1/config/rollback",
		"/api/v1/config/adopt-external/preview", "/api/v1/config/adopt-external",
		"/api/v1/config/pending-restart/discard",
	} {
		t.Run("method "+path, func(t *testing.T) {
			rr := v1Request(t, s, http.MethodGet, path, "", nil)
			if rr.Code != http.StatusBadRequest || decodeEnvelope(t, rr).Error.Code != adminapi.CodeInvalidRequest {
				t.Fatalf("%s status=%d body=%s", path, rr.Code, rr.Body.String())
			}
		})
	}
}
