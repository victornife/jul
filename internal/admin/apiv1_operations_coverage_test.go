// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

func managedV1OperationServer(t *testing.T, deps Deps) *Server {
	t.Helper()
	if deps.Authority == nil {
		deps.Authority = func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "managed", Source: "explicit", ConfigState: "managed_clean"}
		}
	}
	return newTestServer(t, config.AdminConfig{}, deps)
}

func callV1OperationHandler(
	t *testing.T,
	handler func(http.ResponseWriter, *http.Request),
	method, path, contentType, body string,
) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	handler(rr, req)
	return rr
}

func requireV1ErrorCode(t *testing.T, rr *httptest.ResponseRecorder, code adminapi.Code) {
	t.Helper()
	if rr.Code < 400 {
		t.Fatalf("status=%d, want error; body=%s", rr.Code, rr.Body.String())
	}
	if got := decodeEnvelope(t, rr).Error.Code; got != code {
		t.Fatalf("code=%q want=%q status=%d body=%s", got, code, rr.Code, rr.Body.String())
	}
}

func TestV1OperationHandlersRejectInvalidAdmissionBeforeBusinessLogic(t *testing.T) {
	noStorage := managedV1OperationServer(t, Deps{})

	wrongMethod := []struct {
		name    string
		handler func(http.ResponseWriter, *http.Request)
		method  string
		path    string
	}{
		{"validate", noStorage.handleV1ConfigValidate, http.MethodGet, "/api/v1/config/validate"},
		{"plan", noStorage.handleV1ConfigPlan, http.MethodGet, "/api/v1/config/plan"},
		{"route-test", noStorage.handleV1RouteTest, http.MethodGet, "/api/v1/routes/test"},
		{"patch-preview", noStorage.handleV1ConfigPatch, http.MethodGet, "/api/v1/config/patch"},
		{"apply", noStorage.handleV1ConfigApply, http.MethodGet, "/api/v1/config/apply"},
		{"patch-apply", noStorage.handleV1ConfigPatchApply, http.MethodGet, "/api/v1/config/patch/apply"},
		{"rollback", noStorage.handleV1Rollback, http.MethodGet, "/api/v1/config/rollback"},
		{"adopt-preview", noStorage.handleV1AdoptPreview, http.MethodGet, "/api/v1/config/adopt-external/preview"},
		{"adopt", noStorage.handleV1Adopt, http.MethodGet, "/api/v1/config/adopt-external"},
		{"discard", noStorage.handleV1DiscardPendingRestart, http.MethodGet, "/api/v1/config/pending-restart/discard"},
		{"client-address", noStorage.handleV1ClientAddressWrite, http.MethodPost, "/api/v1/listeners/%3A8080/client_address"},
	}
	for _, tc := range wrongMethod {
		t.Run("wrong-method-"+tc.name, func(t *testing.T) {
			rr := callV1OperationHandler(t, tc.handler, tc.method, tc.path, "", "")
			requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
		})
	}

	t.Run("validate unsupported media type", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigValidate, http.MethodPost,
			"/api/v1/config/validate", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeUnsupportedMediaType)
	})
	t.Run("validate invalid TOML", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigValidate, http.MethodPost,
			"/api/v1/config/validate", "application/toml", `not toml = [`)
		requireV1ErrorCode(t, rr, adminapi.CodeValidationFailed)
	})
	t.Run("plan unsupported media type", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPlan, http.MethodPost,
			"/api/v1/config/plan", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeUnsupportedMediaType)
	})
	t.Run("plan storage unavailable", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPlan, http.MethodPost,
			"/api/v1/config/plan", "application/toml", "[global]\n")
		requireV1ErrorCode(t, rr, adminapi.CodeStorageUnavailable)
	})
	t.Run("route-test malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1RouteTest, http.MethodPost,
			"/api/v1/routes/test", "application/json", `{"method":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("route-test storage unavailable", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1RouteTest, http.MethodPost,
			"/api/v1/routes/test", "application/json", `{"method":"GET","path":"/"}`)
		requireV1ErrorCode(t, rr, adminapi.CodeStorageUnavailable)
	})
	t.Run("patch preview unavailable", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPatch, http.MethodPost,
			"/api/v1/config/patch", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeNotImplemented)
	})
	t.Run("apply missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigApply, http.MethodPost,
			"/api/v1/config/apply", "application/toml", "[global]\n")
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("apply unsupported media", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigApply, http.MethodPost,
			"/api/v1/config/apply?base_version=v1", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeUnsupportedMediaType)
	})
	t.Run("patch-apply malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPatchApply, http.MethodPost,
			"/api/v1/config/patch/apply", "application/json", `{"base_version":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("patch-apply missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPatchApply, http.MethodPost,
			"/api/v1/config/patch/apply", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("patch-apply empty ops", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ConfigPatchApply, http.MethodPost,
			"/api/v1/config/patch/apply", "application/json", `{"base_version":"v1","ops":[]}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("rollback malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1Rollback, http.MethodPost,
			"/api/v1/config/rollback", "application/json", `{"id":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("rollback missing id", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1Rollback, http.MethodPost,
			"/api/v1/config/rollback", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("rollback missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1Rollback, http.MethodPost,
			"/api/v1/config/rollback", "application/json", `{"id":"h1"}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("adopt preview malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1AdoptPreview, http.MethodPost,
			"/api/v1/config/adopt-external/preview", "application/json", `{"base_version":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("adopt malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1Adopt, http.MethodPost,
			"/api/v1/config/adopt-external", "application/json", `{"base_version":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("adopt missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1Adopt, http.MethodPost,
			"/api/v1/config/adopt-external", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("discard missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard", "", "")
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("client-address malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ClientAddressWrite, http.MethodPatch,
			"/api/v1/listeners/%3A8080/client_address", "application/json", `{"base_version":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("client-address missing base", func(t *testing.T) {
		rr := callV1OperationHandler(t, noStorage.handleV1ClientAddressWrite, http.MethodPatch,
			"/api/v1/listeners/%3A8080/client_address", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
}

func TestV1PatchPreviewAdmissionAndErrorProjectionBranches(t *testing.T) {
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	withStorage := managedV1OperationServer(t, Deps{
		ReadConfigRaw: func() ([]byte, error) { return append([]byte(nil), seed...), nil },
		LoadConfig:    func() (*config.Config, error) { return config.Parse(seed) },
	})

	t.Run("malformed JSON", func(t *testing.T) {
		rr := callV1OperationHandler(t, withStorage.handleV1ConfigPatch, http.MethodPost,
			"/api/v1/config/patch", "application/json", `{"ops":`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})
	t.Run("empty ops", func(t *testing.T) {
		rr := callV1OperationHandler(t, withStorage.handleV1ConfigPatch, http.MethodPost,
			"/api/v1/config/patch", "application/json", `{}`)
		requireV1ErrorCode(t, rr, adminapi.CodeInvalidRequest)
	})

	for _, tc := range []struct {
		name string
		err  error
		code adminapi.Code
	}{
		{"stale", &patchVersionConflictError{CurrentVersion: "new"}, adminapi.CodeStaleBaseVersion},
		{"operation", &patchOperationError{OpIndex: 2, Op: "replace", Err: errors.New("rejected")}, adminapi.CodeOperationFailed},
		{"candidate timeout", &patchCandidateError{Err: context.DeadlineExceeded}, adminapi.CodeOperationTimeout},
		{"candidate cancelled", &patchCandidateError{Err: context.Canceled}, adminapi.CodeOperationTimeout},
		{"candidate invalid", &patchCandidateError{Err: errors.New("candidate rejected")}, adminapi.CodeValidationFailed},
		{"unknown", errors.New("unknown patch failure"), adminapi.CodeInternalError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/config/patch", nil)
			withStorage.writeV1PatchExecutionError(rr, req, tc.err, "base")
			requireV1ErrorCode(t, rr, tc.code)
		})
	}

	if got := v1Findings(errors.New("")); len(got) == 0 {
		t.Fatal("v1Findings returned no fallback finding")
	}
	if got := v1Findings(errors.New("config: rejected")); len(got) == 0 {
		t.Fatal("v1Findings returned no humanized finding")
	}
}

func TestV1PlanValidationAndStorageFailureBranches(t *testing.T) {
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal seed: %v", err)
	}
	server := managedV1OperationServer(t, Deps{
		ReadConfigRaw: func() ([]byte, error) { return append([]byte(nil), seed...), nil },
		LoadConfig:    func() (*config.Config, error) { return config.Parse(seed) },
	})

	rr := callV1OperationHandler(t, server.handleV1ConfigPlan, http.MethodPost,
		"/api/v1/config/plan", "application/toml", `not toml = [`)
	requireV1ErrorCode(t, rr, adminapi.CodeValidationFailed)

	broken := managedV1OperationServer(t, Deps{
		ReadConfigRaw: func() ([]byte, error) { return nil, errors.New("disk unavailable") },
	})
	rr = callV1OperationHandler(t, broken.handleV1ConfigPlan, http.MethodPost,
		"/api/v1/config/plan", "application/toml", "[global]\n")
	requireV1ErrorCode(t, rr, adminapi.CodeStorageUnavailable)
}

func TestV1CaptureLowLevelBranches(t *testing.T) {
	cap := newV1Capture()
	cap.WriteHeader(http.StatusCreated)
	cap.WriteHeader(http.StatusNoContent)
	if cap.status != http.StatusCreated {
		t.Fatalf("second WriteHeader changed status to %d", cap.status)
	}

	cap = newV1Capture()
	if _, err := cap.Write([]byte("body")); err != nil || cap.status != http.StatusOK || cap.body.String() != "body" {
		t.Fatalf("write status=%d body=%q err=%v", cap.status, cap.body.String(), err)
	}

	s := managedV1OperationServer(t, Deps{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	s.runCanonicalV1(rr, req, "", func(http.ResponseWriter, *http.Request) {})
	if rr.Code != http.StatusOK {
		t.Fatalf("empty canonical handler status=%d", rr.Code)
	}

	if got, ok := stringSlice([]any{}); !ok || len(got) != 0 {
		t.Fatalf("empty string slice=%v ok=%v", got, ok)
	}

	h := newV1Capture()
	h.Header().Set("X-Test", "one")
	h.Header().Add("X-Test", "two")
	h.WriteHeader(http.StatusAccepted)
	_, _ = h.Write(bytes.Repeat([]byte("x"), 2))
	out := httptest.NewRecorder()
	writeCapturedV1(out, h)
	if out.Code != http.StatusAccepted || out.Header().Get("Cache-Control") != "no-store" || out.Body.String() != "xx" {
		t.Fatalf("captured status=%d headers=%v body=%q", out.Code, out.Header(), out.Body.String())
	}
}
