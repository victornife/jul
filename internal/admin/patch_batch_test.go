// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func TestExecutePatchBatchAppliesOrderedOperations(t *testing.T) {
	before := patchProxyConfig()
	ops := []patchRequest{
		{Op: "server_add", Listen: ":9090", ServerNames: []string{"example.test"}},
		{
			Op:          "location_add",
			Listen:      ":9090",
			ServerNames: []string{"example.test"},
			Match:       &locationMatch{Type: "prefix", Path: "/"},
			Action:      &locationActionPayload{Kind: "proxy", Target: "http://127.0.0.1:9100"},
		},
	}

	got, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: before,
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", ops)
	if err != nil {
		t.Fatalf("executePatchBatch: %v", err)
	}
	if !got.Valid {
		t.Fatalf("candidate valid = false; validation errors: %+v", got.ValidationErrors)
	}
	if len(got.OperationSummaries) != 2 {
		t.Fatalf("operation summaries = %d, want 2", len(got.OperationSummaries))
	}
	for i, wantOp := range []string{"server_add", "location_add"} {
		if got.OperationSummaries[i].OpIndex != i || got.OperationSummaries[i].Op != wantOp {
			t.Errorf("summary[%d] = %+v, want index=%d op=%q", i, got.OperationSummaries[i], i, wantOp)
		}
	}
	if !strings.Contains(got.summaryText(), "server") || !strings.Contains(got.summaryText(), "route") {
		t.Errorf("summary = %q, want ordered server and route summaries", got.summaryText())
	}
	if len(got.CandidateConfig.Servers) != 2 {
		t.Fatalf("candidate servers = %d, want 2", len(got.CandidateConfig.Servers))
	}
	if len(before.Servers) != 1 {
		t.Fatalf("executor mutated baseline: servers = %d, want 1", len(before.Servers))
	}
}

func TestExecutePatchBatchReportsExactFailedOperationWithoutMutation(t *testing.T) {
	before := patchProxyConfig()
	canonicalBefore, err := config.Marshal(before)
	if err != nil {
		t.Fatalf("marshal before: %v", err)
	}
	_, err = executePatchBatch(context.Background(), patchBatchBaseline{Config: before}, "", []patchRequest{
		{Op: "server_add", Listen: ":9090"},
		{Op: "server_remove", Listen: ":does-not-exist"},
	})
	var opErr *patchOperationError
	if !errors.As(err, &opErr) {
		t.Fatalf("error = %T %v, want *patchOperationError", err, err)
	}
	if opErr.OpIndex != 1 || opErr.Op != "server_remove" {
		t.Fatalf("operation error = %+v, want index 1 server_remove", opErr)
	}
	after, marshalErr := config.Marshal(before)
	if marshalErr != nil {
		t.Fatalf("marshal after: %v", marshalErr)
	}
	if !bytes.Equal(after, canonicalBefore) {
		t.Fatal("failed batch mutated the caller-owned baseline")
	}
}

func TestExecutePatchBatchOptimisticConcurrency(t *testing.T) {
	before := patchProxyConfig()
	raw, err := config.Marshal(before)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	version := configVersion(raw)
	ops := []patchRequest{{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api",
		Target: "http://127.0.0.1:9100",
	}}

	for _, requested := range []string{"", version} {
		got, execErr := executePatchBatch(context.Background(), patchBatchBaseline{
			Config:  before,
			Version: version,
		}, requested, ops)
		if execErr != nil {
			t.Fatalf("requested version %q: %v", requested, execErr)
		}
		if got.BaseVersion != version {
			t.Errorf("base version = %q, want %q", got.BaseVersion, version)
		}
	}

	_, err = executePatchBatch(context.Background(), patchBatchBaseline{
		Config:  before,
		Version: version,
	}, "stale", ops)
	var conflict *patchVersionConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("stale error = %T %v, want conflict", err, err)
	}
	if conflict.CurrentVersion != version {
		t.Errorf("current version = %q, want %q", conflict.CurrentVersion, version)
	}
}

func TestExecutePatchBatchCanonicalLifecycleCompleteCandidate(t *testing.T) {
	enabled := true
	got, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: patchProxyConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{
		{
			Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Target: "http://127.0.0.1:9100",
		},
		{Op: "server_toggle_h2c", Listen: ":8080", Enabled: &enabled},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got.Lifecycle.CanApplyHot {
		t.Fatal("mixed hot + retained-listener candidate reported can_apply_hot=true")
	}
	if len(got.Lifecycle.RestartRequired) == 0 {
		t.Fatal("retained listener h2c change was not restart-required")
	}
	var sawHot, sawRestart bool
	for _, change := range got.Lifecycle.Changes {
		switch change.Effective {
		case lifecycle.HotReloadClass:
			sawHot = true
		case lifecycle.RestartRequiredClass:
			sawRestart = true
		}
	}
	if !sawHot || !sawRestart {
		t.Fatalf("lifecycle changes = %+v, want both hot and restart-required", got.Lifecycle.Changes)
	}
}

func TestExecutePatchBatchNewListenerOnlyClassification(t *testing.T) {
	enabled := true
	got, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: patchProxyConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{
		{Op: "server_add", Listen: ":9090"},
		{
			Op: "location_add", Listen: ":9090",
			Match:  &locationMatch{Type: "prefix", Path: "/"},
			Action: &locationActionPayload{Kind: "proxy", Target: "http://127.0.0.1:9100"},
		},
		{Op: "server_toggle_h2c", Listen: ":9090", Enabled: &enabled},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !got.Lifecycle.CanApplyHot {
		t.Fatalf("new-listener-only candidate reported can_apply_hot=false: %+v", got.Lifecycle)
	}
	if len(got.Lifecycle.NewListenerOnly) == 0 {
		t.Fatal("new listener h2c change was not classified new-listener-only")
	}
	if len(got.Lifecycle.RestartRequired) != 0 {
		t.Fatalf("restart-required paths = %v, want none", got.Lifecycle.RestartRequired)
	}
}

func TestPatchPreviewAndCandidateShareAssessment(t *testing.T) {
	cfg := patchProxyConfig()
	deps := Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }}
	s := newTestServer(t, config.AdminConfig{}, deps)
	raw, err := config.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal baseline: %v", err)
	}
	body, err := json.Marshal(patchApplyRequest{
		BaseVersion: configVersion(raw),
		Ops: []patchRequest{{
			Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api",
			Target: "http://127.0.0.1:9100",
		}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	request := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s status = %d; body: %s", path, rr.Code, rr.Body.String())
		}
		return rr
	}
	previewRR := request("/api/config/patch/preview")
	candidateRR := request("/api/config/patch/candidate")

	var preview patchPreviewResponse
	if err := json.Unmarshal(previewRR.Body.Bytes(), &preview); err != nil {
		t.Fatalf("decode preview: %v", err)
	}
	var candidate patchCandidateResponse
	if err := json.Unmarshal(candidateRR.Body.Bytes(), &candidate); err != nil {
		t.Fatalf("decode candidate: %v", err)
	}
	if preview.Summary != candidate.Summary || preview.BaseVersion != candidate.BaseVersion {
		t.Fatalf("assessment mismatch: preview=%+v candidate=%+v", preview, candidate.patchPreviewResponse)
	}
	if len(preview.OperationSummaries) != 1 || len(candidate.OperationSummaries) != 1 {
		t.Fatalf("operation summaries preview=%d candidate=%d", len(preview.OperationSummaries), len(candidate.OperationSummaries))
	}
	if preview.OperationSummaries[0] != candidate.OperationSummaries[0] {
		t.Fatalf("operation summaries differ: %+v vs %+v", preview.OperationSummaries, candidate.OperationSummaries)
	}
	if strings.Contains(previewRR.Body.String(), "candidate") {
		t.Fatal("secret-safe preview exposed candidate TOML")
	}
	parsedCandidate, err := config.Parse([]byte(candidate.Candidate))
	if err != nil {
		t.Fatalf("candidate is not canonical parseable TOML: %v", err)
	}
	if got := parsedCandidate.Servers[0].Locations[0].ProxyPass; got != "http://127.0.0.1:9100" {
		t.Fatalf("candidate proxy_pass = %q", got)
	}
}

func TestPatchPreviewOperationFailureWireContract(t *testing.T) {
	deps := Deps{LoadConfig: func() (*config.Config, error) { return patchProxyConfig(), nil }}
	s := newTestServer(t, config.AdminConfig{}, deps)
	body, _ := json.Marshal(patchApplyRequest{Ops: []patchRequest{
		{Op: "server_add", Listen: ":9090"},
		{Op: "server_remove", Listen: ":missing"},
	}})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	var got patchOperationFailureResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.OpIndex != 1 || got.Op != "server_remove" {
		t.Fatalf("failure = %+v, want zero-based index 1 and server_remove", got)
	}
	if len(got.Errors) == 0 {
		t.Fatal("operation failure omitted humanized errors")
	}
}

func TestPatchPreviewLifecycleIsValueFreeAndArraysAreStable(t *testing.T) {
	cfg := patchProxyConfig()
	deps := Deps{LoadConfig: func() (*config.Config, error) { return cfg, nil }}
	s := newTestServer(t, config.AdminConfig{}, deps)
	target := "http://sensitive-internal-target.example:9100"
	body, _ := json.Marshal(patchApplyRequest{Ops: []patchRequest{{
		Op: "route_set_target", Listen: ":8080", MatchType: "prefix", Path: "/api", Target: target,
	}}})
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/config/patch/preview", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body: %s", rr.Code, rr.Body.String())
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	lifecycleJSON := envelope["lifecycle"]
	if bytes.Contains(lifecycleJSON, []byte(target)) {
		t.Fatalf("lifecycle projection leaked configured target: %s", lifecycleJSON)
	}
	var lifecycleBody map[string]any
	if err := json.Unmarshal(lifecycleJSON, &lifecycleBody); err != nil {
		t.Fatalf("decode lifecycle: %v", err)
	}
	for _, field := range []string{
		"changes", "hot_paths", "restart_required_paths", "new_listener_only_paths",
		"ignored_deprecated_paths", "validation_rejected_paths", "pending_subsystems",
	} {
		if _, ok := lifecycleBody[field].([]any); !ok {
			t.Errorf("lifecycle.%s = %#v, want JSON array", field, lifecycleBody[field])
		}
	}
}
