// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"jul/internal/config"
)

func managedTestServer(t *testing.T) *Server {
	t.Helper()
	deps := Deps{
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "managed", Source: "explicit"}
		},
	}
	return newTestServer(t, config.AdminConfig{}, deps)
}

func TestHandleAdoptExternalPreviewNotWiredIs501(t *testing.T) {
	s := managedTestServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/adopt-external/preview", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalPreviewMethodNotAllowed(t *testing.T) {
	s := managedTestServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/config/adopt-external/preview", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalPreviewSuccess(t *testing.T) {
	s := managedTestServer(t)
	s.deps.AdoptExternalPreview = func() (AdoptPreviewResult, error) {
		return AdoptPreviewResult{OK: true, Origin: "drift", ObservedDigest: "deadbeef"}, nil
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/adopt-external/preview", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out AdoptPreviewResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Origin != "drift" {
		t.Errorf("response = %+v, want the preview result", out)
	}
}

func TestHandleAdoptExternalPreviewError(t *testing.T) {
	s := managedTestServer(t)
	s.deps.AdoptExternalPreview = func() (AdoptPreviewResult, error) {
		return AdoptPreviewResult{}, errors.New("boom")
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/adopt-external/preview", nil))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "boom") {
		t.Errorf("body = %s, want it to carry the error", rr.Body.String())
	}
}

func TestHandleAdoptExternalDeniedWhenFileOwned(t *testing.T) {
	var called atomic.Bool
	deps := Deps{
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "file_owned", Source: "default"}
		},
		AdoptExternal: func(ApplyRequestContext, AdoptExternalRequest) (ConfigApplyResult, error) {
			called.Store(true)
			return ConfigApplyResult{OK: true}, nil
		},
	}
	s := newTestServer(t, config.AdminConfig{}, deps)
	body, _ := json.Marshal(AdoptExternalRequest{Mode: "hot", Confirm: true})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (file_owned denial); body=%s", rr.Code, rr.Body.String())
	}
	if called.Load() {
		t.Error("AdoptExternal must not be called when file_owned denies the request first")
	}
}

func TestHandleAdoptExternalNotWiredIs501(t *testing.T) {
	s := managedTestServer(t)
	body, _ := json.Marshal(AdoptExternalRequest{Mode: "hot", Confirm: true})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", bytes.NewReader(body)))
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalMethodNotAllowed(t *testing.T) {
	s := managedTestServer(t)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/config/adopt-external", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalBadJSON(t *testing.T) {
	s := managedTestServer(t)
	s.deps.AdoptExternal = func(ApplyRequestContext, AdoptExternalRequest) (ConfigApplyResult, error) {
		t.Fatal("AdoptExternal must not be called for an undecodable body")
		return ConfigApplyResult{}, nil
	}
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", strings.NewReader("{not json")))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalCoordinatorError(t *testing.T) {
	s := managedTestServer(t)
	s.deps.AdoptExternal = func(ApplyRequestContext, AdoptExternalRequest) (ConfigApplyResult, error) {
		return ConfigApplyResult{OK: false}, ErrConfigStorageUnavailable
	}
	body, _ := json.Marshal(AdoptExternalRequest{Mode: "hot", Confirm: true})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", bytes.NewReader(body)))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalRejected(t *testing.T) {
	s := managedTestServer(t)
	s.deps.AdoptExternal = func(ApplyRequestContext, AdoptExternalRequest) (ConfigApplyResult, error) {
		return ConfigApplyResult{OK: false, Message: "adoption requires explicit confirmation"}, nil
	}
	body, _ := json.Marshal(AdoptExternalRequest{Mode: "hot"})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", bytes.NewReader(body)))
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
}

func TestHandleAdoptExternalSuccess(t *testing.T) {
	var gotReq AdoptExternalRequest
	s := managedTestServer(t)
	s.deps.AdoptExternal = func(_ ApplyRequestContext, req AdoptExternalRequest) (ConfigApplyResult, error) {
		gotReq = req
		return ConfigApplyResult{OK: true, Origin: "drift", Message: "adopted"}, nil
	}
	body, _ := json.Marshal(AdoptExternalRequest{Mode: "hot", Confirm: true, ObservedDigest: "deadbeef"})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/adopt-external", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if gotReq.Mode != "hot" || !gotReq.Confirm || gotReq.ObservedDigest != "deadbeef" {
		t.Errorf("request forwarded = %+v, want the decoded body", gotReq)
	}
	var out ConfigApplyResult
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.OK || out.Origin != "drift" {
		t.Errorf("response = %+v, want the coordinator result", out)
	}
}
