// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"jul/internal/config"
	"jul/internal/server"
)

func issue80StageServer(t *testing.T) (*Server, string, []byte, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.toml")
	seed := issue80BaseConfig()
	original := mustConfigBytes(t, seed)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	live, err := config.Parse(original)
	if err != nil {
		t.Fatalf("parse live seed: %v", err)
	}
	stagePending := false
	applySeq := 0

	deps := Deps{
		ReadConfigRaw: func() ([]byte, error) { return os.ReadFile(path) },
		LoadConfig: func() (*config.Config, error) {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			return config.Parse(raw)
		},
		LiveSnapshot: func() server.LiveSnapshot {
			return server.LiveSnapshot{
				EffectiveConfig: live,
				RawConfig:       live,
				Listeners: map[string]server.BoundListenerInfo{
					":8080": {Addr: ":8080"},
				},
			}
		},
		ApplyConfig: func(_ ApplyRequestContext, candidate *config.Config, mode string) (ConfigApplyResult, error) {
			if mode != "stage_restart" {
				return ConfigApplyResult{OK: false, Mode: mode, Message: "test harness accepts stage_restart only"}, nil
			}
			raw, err := config.Marshal(candidate)
			if err != nil {
				return ConfigApplyResult{}, err
			}
			parsed, err := config.Parse(raw)
			if err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, ValidationErrors: []string{err.Error()}}, nil
			}
			if err := config.Validate(parsed); err != nil {
				return ConfigApplyResult{OK: false, Mode: mode, ValidationErrors: []string{err.Error()}}, nil
			}
			wasUpdate := stagePending
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				return ConfigApplyResult{}, err
			}
			stagePending = true
			applySeq++
			version := configVersion(raw)
			servingVersion := configVersion(original)
			return ConfigApplyResult{
				ApplyID:               fmt.Sprintf("issue80-stage-%d", applySeq),
				OK:                    true,
				Mode:                  mode,
				Version:               version,
				PersistedVersion:      version,
				ServingVersion:        servingVersion,
				FinalDiskVersion:      version,
				FinalServingVersion:   servingVersion,
				Persisted:             true,
				StagedRestartIsUpdate: wasUpdate,
				PendingRestart: &PendingRestartStatus{
					State:            "managed_staged",
					Managed:          true,
					Staged:           true,
					StagedVersion:    version,
					ServingVersion:   servingVersion,
					Subsystems:       []string{"global"},
					DiscardAvailable: true,
				},
				Message: "Configuration staged.",
			}, nil
		},
		PendingRestart: func() *PendingRestartStatus {
			if !stagePending {
				return nil
			}
			return &PendingRestartStatus{
				State:            "managed_staged",
				Managed:          true,
				Staged:           true,
				ServingVersion:   configVersion(original),
				Subsystems:       []string{"global"},
				DiscardAvailable: true,
			}
		},
		DiscardPendingRestart: func() (ConfigApplyResult, error) {
			if !stagePending {
				return ConfigApplyResult{OK: true, Message: "No pending restart."}, nil
			}
			if err := os.WriteFile(path, original, 0o600); err != nil {
				return ConfigApplyResult{}, err
			}
			stagePending = false
			return ConfigApplyResult{
				ApplyID:             "issue80-discard",
				OK:                  true,
				Mode:                "hot",
				Version:             configVersion(original),
				PersistedVersion:    configVersion(original),
				ServingVersion:      configVersion(original),
				FinalDiskVersion:    configVersion(original),
				FinalServingVersion: configVersion(original),
				Message:             "Staged restart discarded.",
			}, nil
		},
	}
	server := newTestServer(t, config.AdminConfig{HistoryDir: t.TempDir(), HistoryKeep: 50}, deps)
	return server, path, original, live
}

func issue80PatchApply(t *testing.T, server *Server, ops []patchRequest) (int, ConfigApplyResult, string) {
	t.Helper()
	body, err := json.Marshal(patchApplyRequest{Ops: ops})
	if err != nil {
		t.Fatalf("marshal patch apply: %v", err)
	}
	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
		"/api/config/patch/apply?mode=stage_restart", bytes.NewReader(body)))
	var result ConfigApplyResult
	_ = json.Unmarshal(rr.Body.Bytes(), &result)
	return rr.Code, result, rr.Body.String()
}

func TestIssue80StructuredStageCreateUpdateCorrelationAndDiscard(t *testing.T) {
	server, path, original, live := issue80StageServer(t)

	status, first, body := issue80PatchApply(t, server, []patchRequest{{
		Op:     "global_set",
		Global: &globalPatch{LogFormat: ptr("json")},
	}})
	if status != http.StatusOK || !first.OK {
		t.Fatalf("first stage status=%d result=%+v body=%s", status, first, body)
	}
	if first.ApplyID == "" || first.StagedRestartIsUpdate {
		t.Fatalf("first stage correlation/update = %+v", first)
	}
	if first.PendingRestart == nil || !first.PendingRestart.Staged {
		t.Fatalf("first stage missing pending status: %+v", first)
	}
	stagedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read staged config: %v", err)
	}
	staged, err := config.Parse(stagedRaw)
	if err != nil {
		t.Fatalf("parse staged config: %v", err)
	}
	if staged.Global.LogFormat != "json" || live.Global.LogFormat != "text" {
		t.Fatalf("staging changed wrong state: disk=%q live=%q", staged.Global.LogFormat, live.Global.LogFormat)
	}

	status, second, body := issue80PatchApply(t, server, []patchRequest{{
		Op:     "global_set",
		Global: &globalPatch{LogLevel: ptr("debug")},
	}})
	if status != http.StatusOK || !second.OK {
		t.Fatalf("stage update status=%d result=%+v body=%s", status, second, body)
	}
	if second.ApplyID == "" || second.ApplyID == first.ApplyID || !second.StagedRestartIsUpdate {
		t.Fatalf("stage update correlation = first=%+v second=%+v", first, second)
	}
	updatedRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read updated stage: %v", err)
	}
	updated, err := config.Parse(updatedRaw)
	if err != nil {
		t.Fatalf("parse updated stage: %v", err)
	}
	if updated.Global.LogFormat != "json" || updated.Global.LogLevel != "debug" {
		t.Fatalf("stage update forked/lost candidate fields: %+v", updated.Global)
	}
	if live.Global.LogFormat != "text" || live.Global.LogLevel != "info" {
		t.Fatalf("stage update changed running config: %+v", live.Global)
	}

	// A rejected update must leave the staged bytes untouched.
	beforeRejected := append([]byte(nil), updatedRaw...)
	status, _, body = issue80PatchApply(t, server, []patchRequest{{
		Op:     "global_set",
		Global: &globalPatch{LogLevel: ptr("trace")},
	}})
	if status != http.StatusBadRequest {
		t.Fatalf("invalid stage update status=%d, want 400; body=%s", status, body)
	}
	afterRejected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after rejected update: %v", err)
	}
	if !bytes.Equal(beforeRejected, afterRejected) {
		t.Fatal("rejected stage update mutated staged state")
	}

	rr := httptest.NewRecorder()
	server.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost,
		"/api/config/pending-restart/discard", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("discard status=%d body=%s", rr.Code, rr.Body.String())
	}
	restored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read restored config: %v", err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("discard did not restore exact original bytes\nwant:\n%s\ngot:\n%s", original, restored)
	}
}
