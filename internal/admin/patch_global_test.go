// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"jul/internal/config"
	"jul/internal/lifecycle"
)

func issue80BaseConfig() *config.Config {
	return &config.Config{
		Global: config.GlobalConfig{
			WorkerThreads:         "4",
			LogLevel:              "info",
			LogFormat:             "text",
			ShutdownTimeout:       config.Duration(30 * time.Second),
			ReloadTimeout:         config.Duration(10 * time.Second),
			RedactMinSecretLength: 4,
		},
		Compression: config.CompressionConfig{
			Enabled:       config.Bool(true),
			Encoders:      []string{"gzip"},
			Level:         6,
			MinSize:       config.Size(2 << 10),
			Types:         []string{"text/html", "application/json"},
			Precompressed: true,
		},
		RateLimit: config.RateLimitConfig{
			Enabled:  true,
			Key:      "ip",
			Rate:     100,
			Burst:    200,
			MaxConns: 10,
		},
		Servers: []config.ServerConfig{{
			Listen: ":8080",
			Locations: []config.LocationConfig{{
				Match:     config.MatchConfig{Type: "prefix", Path: "/api"},
				ProxyPass: "http://127.0.0.1:9000",
			}},
		}},
	}
}

func mustConfigBytes(t *testing.T, c *config.Config) []byte {
	t.Helper()
	out, err := config.Marshal(c)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	return out
}

func hasPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func hasLifecyclePath(result lifecycle.Result, want string) bool {
	for _, change := range result.Changes {
		if change.Path == want {
			return true
		}
	}
	return false
}

func TestApplyGlobalSetSparsePresenceAndAtomicity(t *testing.T) {
	c := issue80BaseConfig()
	summary, err := applyPatch(c, patchRequest{
		Op: "global_set",
		Global: &globalPatch{
			WorkerThreads:         ptr(" 8 "),
			LogLevel:              ptr("debug"),
			ReloadTimeout:         ptr("20s"),
			RedactMinSecretLength: ptr(0),
		},
	})
	if err != nil {
		t.Fatalf("apply global_set: %v", err)
	}
	if want := "global fields changed: worker_threads, log_level, reload_timeout, redact_min_secret_length"; summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
	if c.Global.WorkerThreads != "8" || c.Global.LogLevel != "debug" || c.Global.ReloadTimeout.Std() != 20*time.Second {
		t.Fatalf("global fields not applied: %+v", c.Global)
	}
	if c.Global.LogFormat != "text" || c.Global.ShutdownTimeout.Std() != 30*time.Second {
		t.Fatalf("omitted fields changed: %+v", c.Global)
	}
	if c.Global.RedactMinSecretLength != 0 {
		t.Fatalf("explicit zero redaction minimum = %d, want 0", c.Global.RedactMinSecretLength)
	}

	before := c.Global
	_, err = applyPatch(c, patchRequest{
		Op: "global_set",
		Global: &globalPatch{
			WorkerThreads:   ptr("16"),
			ReloadTimeout:   ptr("0s"),
			ShutdownTimeout: ptr("45s"),
		},
	})
	if err == nil {
		t.Fatal("expected zero reload_timeout rejection")
	}
	if c.Global != before {
		t.Fatalf("rejected global_set partially mutated config: got %+v want %+v", c.Global, before)
	}

	for _, req := range []patchRequest{
		{Op: "global_set"},
		{Op: "global_set", Global: &globalPatch{}},
	} {
		if _, err := applyPatch(issue80BaseConfig(), req); err == nil {
			t.Fatalf("expected payload/presence error for %+v", req)
		}
	}
}

func TestApplyGlobalSetRejectsInvalidFieldsWithoutMutation(t *testing.T) {
	tests := []globalPatch{
		{WorkerThreads: ptr("0")},
		{WorkerThreads: ptr("1.5")},
		{LogLevel: ptr("trace")},
		{LogFormat: ptr("yaml")},
		{ShutdownTimeout: ptr("-1s")},
		{ReloadTimeout: ptr("not-a-duration")},
		{RedactMinSecretLength: ptr(-1)},
	}
	for _, patch := range tests {
		c := issue80BaseConfig()
		before := c.Global
		if _, err := applyPatch(c, patchRequest{Op: "global_set", Global: &patch}); err == nil {
			t.Errorf("patch %+v: expected error", patch)
		}
		if c.Global != before {
			t.Errorf("patch %+v mutated global config on rejection", patch)
		}
	}
}

func TestGlobalSetBatchLifecycleAndRoundTrip(t *testing.T) {
	workerThreads := lifecycle.InitialGOMAXPROCS() + 1
	if workerThreads == 4 {
		workerThreads++
	}
	workerThreadsText := strconv.Itoa(workerThreads)

	hot, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "global_set",
		Global: &globalPatch{
			WorkerThreads:   &workerThreadsText,
			LogLevel:        ptr("debug"),
			ShutdownTimeout: ptr("45s"),
			ReloadTimeout:   ptr("25s"),
		},
	}})
	if err != nil {
		t.Fatalf("hot execute: %v", err)
	}
	if !hot.Valid || !hot.Lifecycle.CanApplyHot || !hot.Lifecycle.CanStageRestart {
		t.Fatalf("hot lifecycle = %+v, valid=%v errors=%+v", hot.Lifecycle, hot.Valid, hot.ValidationErrors)
	}
	for _, path := range []string{"global.worker_threads", "global.log_level", "global.shutdown_timeout", "global.reload_timeout"} {
		found := false
		for _, ch := range hot.Lifecycle.Changes {
			if ch.Path == path && ch.Effective == lifecycle.HotReloadClass {
				found = true
			}
		}
		if !found {
			t.Errorf("missing canonical hot lifecycle change for %s: %+v", path, hot.Lifecycle)
		}
	}
	if strings.Contains(hot.summaryText(), "debug") || strings.Contains(hot.summaryText(), workerThreadsText) || strings.Contains(hot.summaryText(), "25s") {
		t.Fatalf("summary leaked values: %q", hot.summaryText())
	}
	if hot.CandidateConfig.Global.ReloadTimeout.Std() != 25*time.Second {
		t.Fatalf("candidate reload_timeout = %v, want 25s", hot.CandidateConfig.Global.ReloadTimeout.Std())
	}
	if _, err := config.Parse(hot.CandidateRaw); err != nil {
		t.Fatalf("candidate does not round-trip: %v", err)
	}

	mixed, err := executePatchBatch(context.Background(), patchBatchBaseline{
		Config: issue80BaseConfig(),
		Live:   lifecycle.Live{BoundHTTPAddrs: []string{":8080"}},
	}, "", []patchRequest{{
		Op: "global_set",
		Global: &globalPatch{
			LogLevel:  ptr("error"),
			LogFormat: ptr("json"),
		},
	}, {
		// log_format is hot-reloadable now (#91), so global_set alone no
		// longer produces a mixed hot+restart batch; h2c is still
		// restart-required (bind-time), so pairing it here still proves a
		// mixed batch stages instead of partially applying.
		Op:      "server_toggle_h2c",
		Listen:  ":8080",
		Enabled: boolPtr(true),
	}})
	if err != nil {
		t.Fatalf("mixed execute: %v", err)
	}
	if mixed.Lifecycle.CanApplyHot || !mixed.Lifecycle.CanStageRestart {
		t.Fatalf("mixed lifecycle = %+v, want complete candidate staged", mixed.Lifecycle)
	}
	if !hasPath(mixed.Lifecycle.RestartRequired, "servers.*.h2c") {
		t.Fatalf("restart paths = %v, want servers.*.h2c", mixed.Lifecycle.RestartRequired)
	}
	if mixed.CandidateConfig.Global.LogLevel != "error" || mixed.CandidateConfig.Global.LogFormat != "json" {
		t.Fatalf("mixed candidate was partially constructed: %+v", mixed.CandidateConfig.Global)
	}
}

func TestIssue80OperationsBatchAllOrNothing(t *testing.T) {
	encoders := []string{"gzip"}
	types := []string{"text/*"}
	ops := []patchRequest{
		{Op: "global_set", Global: &globalPatch{LogLevel: ptr("debug")}},
		{Op: "compression_set", Compression: &compressionPatch{Encoders: &encoders, Types: &types}},
		{Op: "rate_limit_global_set", RateLimit: &rateLimitPatch{Rate: ptr(250), Burst: ptr(250)}},
	}
	for _, first := range ops {
		before := issue80BaseConfig()
		beforeRaw := mustConfigBytes(t, before)
		_, err := executePatchBatch(context.Background(), patchBatchBaseline{Config: before}, "", []patchRequest{
			first,
			{Op: "global_set", Global: &globalPatch{LogLevel: ptr("not-valid")}},
		})
		var opErr *patchOperationError
		if !errors.As(err, &opErr) || opErr.OpIndex != 1 {
			t.Fatalf("first op %s: error = %T %v, want op index 1", first.Op, err, err)
		}
		if after := mustConfigBytes(t, before); !bytes.Equal(after, beforeRaw) {
			t.Fatalf("failed batch beginning with %s mutated baseline", first.Op)
		}
	}
}

func TestIssue80WireHasSingleRateLimitJSONField(t *testing.T) {
	typ := reflect.TypeOf(patchRequest{})
	count := 0
	for i := 0; i < typ.NumField(); i++ {
		if strings.Split(typ.Field(i).Tag.Get("json"), ",")[0] == "rate_limit" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("patchRequest has %d rate_limit JSON fields, want exactly one", count)
	}
}
