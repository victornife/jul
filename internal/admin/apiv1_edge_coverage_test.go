// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/rbac"
)

func TestPendingRestartInternalHandlerBranches(t *testing.T) {
	t.Run("dependency unavailable", func(t *testing.T) {
		s := managedV1OperationServer(t, Deps{})
		rr := callV1OperationHandler(t, s.handleDiscardPendingRestart, http.MethodPost,
			"/api/config/pending-restart/discard", "", "")
		if rr.Code != http.StatusNotImplemented {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
	})

	t.Run("discard verification failure", func(t *testing.T) {
		read := 0
		s := managedV1OperationServer(t, Deps{
			ReadConfigRaw: func() ([]byte, error) {
				read++
				return []byte("[global]\n"), nil
			},
			DiscardPendingRestart: func() (ConfigApplyResult, error) {
				return ConfigApplyResult{}, errors.New("digest mismatch")
			},
		})
		rr := callV1OperationHandler(t, s.handleDiscardPendingRestart, http.MethodPost,
			"/api/config/pending-restart/discard", "", "")
		if rr.Code != http.StatusConflict || read == 0 {
			t.Fatalf("status=%d read=%d body=%s", rr.Code, read, rr.Body.String())
		}
	})

	t.Run("discard success despite history read failure", func(t *testing.T) {
		called := 0
		s := managedV1OperationServer(t, Deps{
			ReadConfigRaw: func() ([]byte, error) { return nil, errors.New("history read failed") },
			DiscardPendingRestart: func() (ConfigApplyResult, error) {
				called++
				return ConfigApplyResult{OK: true, Mode: "stage_restart", AppOutcome: "discarded"}, nil
			},
		})
		rr := callV1OperationHandler(t, s.handleDiscardPendingRestart, http.MethodPost,
			"/api/config/pending-restart/discard", "", "")
		if rr.Code != http.StatusOK || called != 1 {
			t.Fatalf("status=%d called=%d body=%s", rr.Code, called, rr.Body.String())
		}
	})

	t.Run("pending read nil and present", func(t *testing.T) {
		none := managedV1OperationServer(t, Deps{})
		rr := callV1OperationHandler(t, none.handlePendingRestart, http.MethodGet,
			"/api/config/pending-restart", "", "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"pending":false`) {
			t.Fatalf("nil pending status=%d body=%s", rr.Code, rr.Body.String())
		}

		empty := managedV1OperationServer(t, Deps{PendingRestart: func() *PendingRestartStatus { return nil }})
		rr = callV1OperationHandler(t, empty.handlePendingRestart, http.MethodGet,
			"/api/config/pending-restart", "", "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"pending":false`) {
			t.Fatalf("empty pending status=%d body=%s", rr.Code, rr.Body.String())
		}

		present := managedV1OperationServer(t, Deps{PendingRestart: func() *PendingRestartStatus {
			return &PendingRestartStatus{State: "managed_staged", StagedVersion: "v2", ServingVersion: "v1"}
		}})
		rr = callV1OperationHandler(t, present.handlePendingRestart, http.MethodGet,
			"/api/config/pending-restart", "", "")
		if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), `"pending":true`) {
			t.Fatalf("present pending status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestV1IdempotencyMetadataAndFallbackBranches(t *testing.T) {
	s := &Server{deps: Deps{BootID: func() string { return "abcdef123456" }}}

	t.Run("no key", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", strings.NewReader("x"))
		meta, apiErr := s.v1IdempotencyMetadata(r, []byte("x"))
		if apiErr != nil || meta != nil {
			t.Fatalf("meta=%+v err=%v", meta, apiErr)
		}
	})
	t.Run("invalid key", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		r.Header.Set("Idempotency-Key", "short")
		_, apiErr := s.v1IdempotencyMetadata(r, nil)
		if apiErr == nil || apiErr.Code != adminapi.CodeInvalidRequest {
			t.Fatalf("err=%v", apiErr)
		}
	})
	t.Run("missing principal", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		r.Header.Set("Idempotency-Key", "retry-key")
		_, apiErr := s.v1IdempotencyMetadata(r, nil)
		if apiErr == nil || apiErr.Code != adminapi.CodeInternalError {
			t.Fatalf("err=%v", apiErr)
		}
	})
	t.Run("malformed query", func(t *testing.T) {
		r := &http.Request{
			Method: http.MethodPost,
			URL:    &url.URL{Path: "/api/v1/config/apply", RawQuery: "bad=%zz"},
			Header: make(http.Header),
		}
		r.Header.Set("Idempotency-Key", "retry-key")
		ident := rbac.Identity{Principal: "alice", TokenID: "tok", Permissions: []rbac.Permission{rbac.ConfigApply}}
		r = r.WithContext(rbac.WithIdentity(r.Context(), ident))
		_, apiErr := s.v1IdempotencyMetadata(r, nil)
		if apiErr == nil || apiErr.Code != adminapi.CodeInvalidRequest {
			t.Fatalf("err=%v", apiErr)
		}
	})
	t.Run("fingerprint empty path and operation fallback", func(t *testing.T) {
		r := &http.Request{Method: http.MethodPost, URL: &url.URL{}, Header: make(http.Header)}
		if _, apiErr := v1RequestFingerprint(r, nil); apiErr != nil {
			t.Fatal(apiErr)
		}
		r.URL.Path = "/fallback"
		if got := v1OperationTemplate(r); got != "/fallback" {
			t.Fatalf("operation=%q", got)
		}
	})

	t.Run("idempotent request requires ledger", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", strings.NewReader("[global]\n"))
		r.Header.Set("Idempotency-Key", "retry-key")
		r.Header.Set("Content-Type", "application/toml")
		ident := rbac.Identity{Principal: "alice", TokenID: "tok", Permissions: []rbac.Permission{rbac.ConfigApply}}
		r = r.WithContext(rbac.WithIdentity(r.Context(), ident))
		rr := httptest.NewRecorder()
		s.runIdempotentCanonicalV1(rr, r, "", []byte("[global]\n"), func(http.ResponseWriter, *http.Request) {
			t.Fatal("handler executed without ledger")
		})
		requireV1ErrorCode(t, rr, adminapi.CodeInternalError)
	})

	t.Run("no key executes canonical handler", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		rr := httptest.NewRecorder()
		called := 0
		s.runIdempotentCanonicalV1(rr, r, "", nil, func(w http.ResponseWriter, _ *http.Request) {
			called++
			writeJSON(w, http.StatusConflict, map[string]string{"error": "conflict"})
		})
		if called != 1 || rr.Code != http.StatusConflict {
			t.Fatalf("called=%d status=%d", called, rr.Code)
		}
	})

	cap := newV1Capture()
	cap.Header().Set("Cache-Control", "private")
	cap.Header().Add("X-Multi", "one")
	cap.Header().Add("X-Multi", "two")
	_, _ = cap.Write([]byte("ok"))
	out := httptest.NewRecorder()
	writeCapturedV1(out, cap)
	if out.Header().Get("Cache-Control") != "private" || len(out.Header().Values("X-Multi")) != 2 || out.Body.String() != "ok" {
		t.Fatalf("headers=%v body=%q", out.Header(), out.Body.String())
	}
}

func TestV1ExternalMutationProjectionBranches(t *testing.T) {
	s := &Server{deps: Deps{BootID: func() string { return "abcdef123456" }}}

	t.Run("accepted success projects pending DTO", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		rr := httptest.NewRecorder()
		s.runExternalMutationV1(rr, r, "", nil, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusAccepted, ConfigApplyResult{ApplyID: "rl_abcdef123456_1", Mode: "hot", OK: true})
		})
		if rr.Code != http.StatusAccepted {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		var got adminapi.ConfigApplyResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Terminal || got.State != "pending" || got.ApplyID == "" {
			t.Fatalf("response=%+v", got)
		}
	})

	t.Run("canonical error is not success-projected", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil)
		rr := httptest.NewRecorder()
		s.runExternalMutationV1(rr, r, "", nil, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusConflict, map[string]any{"current_version": "v2", "message": "stale"})
		})
		requireV1ErrorCode(t, rr, adminapi.CodeStaleBaseVersion)
	})
}
