// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

func TestV1MutationAuthorityDenialsCoverEveryWriteAdapter(t *testing.T) {
	fileOwned := newTestServer(t, config.AdminConfig{}, Deps{Authority: func() ConfigAuthorityStatus {
		return ConfigAuthorityStatus{Mode: "file_owned", Source: "explicit", ConfigState: "file_owned"}
	}})
	for _, tc := range []struct {
		name        string
		handler     func(http.ResponseWriter, *http.Request)
		method      string
		path        string
		contentType string
		body        string
	}{
		{"apply", fileOwned.handleV1ConfigApply, http.MethodPost, "/api/v1/config/apply?base_version=v1", "application/toml", "[global]\n"},
		{"patch apply", fileOwned.handleV1ConfigPatchApply, http.MethodPost, "/api/v1/config/patch/apply", "application/json", `{"base_version":"v1","ops":[{}]}`},
		{"rollback", fileOwned.handleV1Rollback, http.MethodPost, "/api/v1/config/rollback", "application/json", `{"id":"h1","base_version":"v1"}`},
		{"adopt", fileOwned.handleV1Adopt, http.MethodPost, "/api/v1/config/adopt-external", "application/json", `{"base_version":"v1"}`},
		{"discard", fileOwned.handleV1DiscardPendingRestart, http.MethodPost, "/api/v1/config/pending-restart/discard?base_version=v1", "", ""},
		{"client address", fileOwned.handleV1ClientAddressWrite, http.MethodPatch, "/api/v1/listeners/%3A8080/client_address", "application/json", `{"base_version":"v1"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := callV1OperationHandler(t, tc.handler, tc.method, tc.path, tc.contentType, tc.body)
			requireV1ErrorCode(t, rr, adminapi.CodeConfigAuthorityReadOnly)
		})
	}
}

func TestV1RouteTestSuccessDefaultsEmptyPath(t *testing.T) {
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatal(err)
	}
	s := managedV1OperationServer(t, Deps{ReadConfigRaw: func() ([]byte, error) {
		return append([]byte(nil), seed...), nil
	}})
	rr := callV1OperationHandler(t, s.handleV1RouteTest, http.MethodPost,
		"/api/v1/routes/test", "application/json", `{"method":"GET","path":"   "}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestV1PlanStaleBaseAndCancelledAssessment(t *testing.T) {
	seed, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatal(err)
	}
	s := managedV1OperationServer(t, Deps{ReadConfigRaw: func() ([]byte, error) {
		return append([]byte(nil), seed...), nil
	}})

	rr := callV1OperationHandler(t, s.handleV1ConfigPlan, http.MethodPost,
		"/api/v1/config/plan?base_version=definitely-stale", "application/toml", string(seed))
	requireV1ErrorCode(t, rr, adminapi.CodeStaleBaseVersion)

	req, err := http.NewRequest(http.MethodPost, "/api/v1/config/plan", stringsReader(string(seed)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/toml")
	ctx, cancel := context.WithCancel(req.Context())
	cancel()
	req = req.WithContext(ctx)
	rr = httptestRecorder()
	s.handleV1ConfigPlan(rr, req)
	if rr.Code != http.StatusGatewayTimeout && rr.Code != http.StatusBadRequest {
		t.Fatalf("cancelled plan status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestV1AdoptPreviewSuccessAndDriftFailure(t *testing.T) {
	okServer := managedV1OperationServer(t, Deps{AdoptExternalPreview: func() (AdoptExternalAssessment, error) {
		return AdoptExternalAssessment{OK: true}, nil
	}})
	rr := callV1OperationHandler(t, okServer.handleV1AdoptPreview, http.MethodPost,
		"/api/v1/config/adopt-external/preview", "application/json", `{}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("success status=%d body=%s", rr.Code, rr.Body.String())
	}

	badServer := managedV1OperationServer(t, Deps{AdoptExternalPreview: func() (AdoptExternalAssessment, error) {
		return AdoptExternalAssessment{}, errors.New("drift changed")
	}})
	rr = callV1OperationHandler(t, badServer.handleV1AdoptPreview, http.MethodPost,
		"/api/v1/config/adopt-external/preview", "application/json", `{}`)
	requireV1ErrorCode(t, rr, adminapi.CodeDriftDetected)
}

// Tiny wrappers keep this coverage file free of duplicated recorder/reader setup
// while exercising the real handlers.
func stringsReader(s string) *strings.Reader { return strings.NewReader(s) }
func httptestRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }
