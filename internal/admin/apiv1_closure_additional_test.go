// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"jul/internal/adminapi"
	"jul/internal/config"
)

func closureConfigBytes(t *testing.T) []byte {
	t.Helper()
	raw, err := config.Marshal(config.ProxyTarget("127.0.0.1:9000", ":8080"))
	if err != nil {
		t.Fatalf("marshal closure config: %v", err)
	}
	return raw
}

func TestV1DiscardPendingRestartContextClosureBranches(t *testing.T) {
	raw := closureConfigBytes(t)

	newDiscardServer := func(t *testing.T, read func() ([]byte, error), result ConfigApplyResult) (*Server, *bool) {
		t.Helper()
		called := false
		s := managedV1OperationServer(t, Deps{
			ReadConfigRaw: read,
			LoadConfig: func() (*config.Config, error) {
				return config.Parse(raw)
			},
			DiscardPendingRestartWithContext: func(ctx ApplyRequestContext) (ConfigApplyResult, error) {
				called = true
				if ctx.Baseline == nil {
					t.Error("contextual discard did not receive the authorized baseline")
				}
				return result, nil
			},
		})
		return s, &called
	}

	t.Run("success binds exact v1 baseline", func(t *testing.T) {
		s, called := newDiscardServer(t, func() ([]byte, error) {
			return append([]byte(nil), raw...), nil
		}, ConfigApplyResult{OK: true, Mode: "hot", Version: "restored"})
		state, err := s.currentWriteState(false)
		if err != nil {
			t.Fatalf("current state: %v", err)
		}
		rr := callV1OperationHandler(t, s.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard?base_version="+state.Version, "", "")
		if rr.Code != http.StatusOK || !*called {
			t.Fatalf("status=%d called=%v body=%s", rr.Code, *called, rr.Body.String())
		}
	})

	t.Run("stale base rejects before coordinator", func(t *testing.T) {
		s, called := newDiscardServer(t, func() ([]byte, error) {
			return append([]byte(nil), raw...), nil
		}, ConfigApplyResult{OK: true, Mode: "hot"})
		rr := callV1OperationHandler(t, s.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard?base_version=stale", "", "")
		if rr.Code != http.StatusConflict || *called {
			t.Fatalf("status=%d called=%v body=%s", rr.Code, *called, rr.Body.String())
		}
	})

	t.Run("storage failure rejects before coordinator", func(t *testing.T) {
		s, called := newDiscardServer(t, func() ([]byte, error) {
			return nil, errors.New("disk unavailable")
		}, ConfigApplyResult{OK: true, Mode: "hot"})
		s.deps.LoadConfig = nil
		rr := callV1OperationHandler(t, s.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard?base_version=anything", "", "")
		if rr.Code != http.StatusServiceUnavailable || *called {
			t.Fatalf("status=%d called=%v body=%s", rr.Code, *called, rr.Body.String())
		}
	})

	t.Run("coordinator conflict is preserved", func(t *testing.T) {
		s, called := newDiscardServer(t, func() ([]byte, error) {
			return append([]byte(nil), raw...), nil
		}, ConfigApplyResult{Conflict: true, Message: "changed", CurrentVersion: "new"})
		state, err := s.currentWriteState(false)
		if err != nil {
			t.Fatalf("current state: %v", err)
		}
		rr := callV1OperationHandler(t, s.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard?base_version="+state.Version, "", "")
		if rr.Code != http.StatusConflict || !*called {
			t.Fatalf("status=%d called=%v body=%s", rr.Code, *called, rr.Body.String())
		}
	})

	t.Run("empty staged read falls back to authorized snapshot", func(t *testing.T) {
		s, called := newDiscardServer(t, func() ([]byte, error) { return nil, nil }, ConfigApplyResult{OK: true, Mode: "hot"})
		state, err := s.currentWriteState(false)
		if err != nil {
			t.Fatalf("current state: %v", err)
		}
		rr := callV1OperationHandler(t, s.handleV1DiscardPendingRestart, http.MethodPost,
			"/api/v1/config/pending-restart/discard?base_version="+state.Version, "", "")
		if rr.Code != http.StatusOK || !*called {
			t.Fatalf("status=%d called=%v body=%s", rr.Code, *called, rr.Body.String())
		}
	})
}

func TestV1MutationHandlersReachCanonicalClosurePaths(t *testing.T) {
	s := managedV1OperationServer(t, Deps{})

	cases := []struct {
		name        string
		handler     func(http.ResponseWriter, *http.Request)
		method      string
		path        string
		contentType string
		body        string
	}{
		{"apply", s.handleV1ConfigApply, http.MethodPost, "/api/v1/config/apply?base_version=v1", "application/toml", "[global]\n"},
		{"patch-preview-execution", s.handleV1ConfigPatch, http.MethodPost, "/api/v1/config/patch", "application/json", `{"base_version":"v1","ops":[{"op":"definitely_invalid"}]}`},
		{"patch-apply", s.handleV1ConfigPatchApply, http.MethodPost, "/api/v1/config/patch/apply", "application/json", `{"base_version":"v1","ops":[{"op":"definitely_invalid"}]}`},
		{"rollback", s.handleV1Rollback, http.MethodPost, "/api/v1/config/rollback", "application/json", `{"id":"missing","base_version":"v1"}`},
		{"adopt", s.handleV1Adopt, http.MethodPost, "/api/v1/config/adopt-external", "application/json", `{"base_version":"v1"}`},
		{"discard", s.handleV1DiscardPendingRestart, http.MethodPost, "/api/v1/config/pending-restart/discard?base_version=v1", "", ""},
		{"client-address", s.handleV1ClientAddressWrite, http.MethodPatch, "/api/v1/listeners/%3A8080/client_address", "application/json", `{"base_version":"v1"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rr := callV1OperationHandler(t, tc.handler, tc.method, tc.path, tc.contentType, tc.body)
			if rr.Code < http.StatusBadRequest && tc.name != "patch-preview-execution" {
				t.Fatalf("unexpected success status=%d body=%s", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestV1ClosureLowLevelAndAuthorityBranches(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
	if got := withV1Idempotency(req, nil); got != req {
		t.Fatal("nil idempotency metadata should preserve the request")
	}
	if got := v1IdempotencyFromRequest(nil); got != nil {
		t.Fatalf("nil request produced idempotency binding: %+v", got)
	}

	out := httptest.NewRecorder()
	writeCapturedV1(out, newV1Capture())
	if out.Code != http.StatusOK {
		t.Fatalf("default captured status=%d", out.Code)
	}

	denied := managedV1OperationServer(t, Deps{
		Authority: func() ConfigAuthorityStatus {
			return ConfigAuthorityStatus{Mode: "file_owned", Source: "explicit"}
		},
	})
	rr := httptest.NewRecorder()
	h := denied.withExternalContract(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !denied.denyIfFileOwned(w, r, "config.apply") {
			t.Error("file-owned external request was not denied")
		}
	}))
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/v1/config/apply", nil))
	if rr.Code != http.StatusConflict || decodeEnvelope(t, rr).Error.Code != adminapi.CodeConfigAuthorityRO {
		t.Fatalf("authority status=%d body=%s", rr.Code, rr.Body.String())
	}

	var clientAddress *RouteSpec
	for i := range Catalog {
		if Catalog[i].Pattern == "/api/v1/listeners/{addr}/client_address" {
			clientAddress = &Catalog[i]
			break
		}
	}
	if clientAddress == nil || clientAddress.Handler == nil {
		t.Fatal("client-address route missing")
	}
	rr = httptest.NewRecorder()
	clientAddress.Handler(denied).ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/v1/listeners/%3A8080/client_address", nil))
	if rr.Code != http.StatusBadRequest || rr.Header().Get("Allow") != "GET, PATCH" {
		t.Fatalf("catalog fallback status=%d allow=%q body=%s", rr.Code, rr.Header().Get("Allow"), rr.Body.String())
	}
}

func TestManagedApplyFinalizationRejectsOperationMismatch(t *testing.T) {
	reg := NewManagedApplyRegistry(0, 0)
	id := "rl_aaaaaaaaaaaa_9"
	if err := reg.BeginPending(ManagedApplyRecord{
		ID:        id,
		Operation: ApplyOperationConfigApply,
		Result:    ConfigApplyResult{ApplyID: id, Mode: "hot", OK: true},
	}); err != nil {
		t.Fatalf("begin pending: %v", err)
	}
	err := reg.FailFinalization(ManagedApplyRecord{
		ID:        id,
		Operation: ApplyOperationRollback,
		Result:    ConfigApplyResult{ApplyID: id, Mode: "hot"},
	})
	if !errors.Is(err, ErrManagedApplyIDMismatch) {
		t.Fatalf("mismatch err=%v, want %v", err, ErrManagedApplyIDMismatch)
	}
}
