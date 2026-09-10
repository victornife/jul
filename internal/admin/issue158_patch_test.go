// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"strings"
	"testing"

	"jul/internal/config"
)

func intPtr(v int) *int { return &v }

func TestAdminLimitsPatchSparseAndCanonicalValues(t *testing.T) {
	cfg := config.Config{Admin: limitTestConfig(240, 60, 30, 4)}
	req := patchRequest{
		Op: "admin_limits_set",
		AdminLimits: &adminLimitsPatch{
			ReadPerMin:    intPtr(-1),
			MaxEventConns: intPtr(8),
		},
	}
	summary, err := applyAdminRuntimePatch(&cfg, req)
	if err != nil {
		t.Fatalf("apply sparse limits patch: %v", err)
	}
	if cfg.Admin.RateLimitReadPerMin != -1 || cfg.Admin.MaxEventConns != 8 {
		t.Fatalf("patched values not applied: %+v", cfg.Admin)
	}
	if cfg.Admin.RateLimitWritePerMin != 60 || cfg.Admin.RateLimitApplyPerMin != 30 {
		t.Fatalf("sparse patch changed unrelated rate classes: %+v", cfg.Admin)
	}
	if !strings.Contains(summary, "max_event_conns") || !strings.Contains(summary, "rate_limit_read_per_min") {
		t.Fatalf("summary does not name changed fields: %q", summary)
	}
}

func TestAdminLimitsPatchAllowsRateZeroDefault(t *testing.T) {
	cfg := config.Config{Admin: limitTestConfig(240, 60, 30, 4)}
	_, err := applyAdminRuntimePatch(&cfg, patchRequest{
		Op:          "admin_limits_set",
		AdminLimits: &adminLimitsPatch{WritePerMin: intPtr(0)},
	})
	if err != nil {
		t.Fatalf("zero request-rate value must remain available for canonical defaulting: %v", err)
	}
	if cfg.Admin.RateLimitWritePerMin != 0 {
		t.Fatalf("write rate=%d want raw canonical-default sentinel 0", cfg.Admin.RateLimitWritePerMin)
	}
}

func TestAdminLimitsPatchRejectsInvalidSSEAndEmptyPatch(t *testing.T) {
	cfg := config.Config{Admin: limitTestConfig(240, 60, 30, 4)}
	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{
		Op:          "admin_limits_set",
		AdminLimits: &adminLimitsPatch{MaxEventConns: intPtr(-1)},
	}); err == nil {
		t.Fatal("negative public max_event_conns must be rejected")
	}
	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{
		Op:          "admin_limits_set",
		AdminLimits: &adminLimitsPatch{},
	}); err == nil {
		t.Fatal("empty admin_limits_set patch must be rejected")
	}
	if _, err := applyAdminRuntimePatch(&cfg, patchRequest{Op: "admin_limits_set"}); err == nil {
		t.Fatal("missing admin_limits payload must be rejected")
	}
}
