// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package doctor

import (
	"context"
	"testing"

	"jul/internal/config"
	"jul/internal/diagnostics"
)

func TestConfigValidateCheckReportsAuthoritativeErrors(t *testing.T) {
	t.Parallel()
	s := &session{cfg: &config.Config{Global: config.GlobalConfig{
		LogLevel:        "not-a-log-level",
		ConfigAuthority: "not-an-authority",
	}}}
	result := s.configValidateCheck(context.Background())
	if result.Status != diagnostics.StatusError {
		t.Fatalf("validation status = %q, result=%#v", result.Status, result)
	}
	errorsFound, ok := result.Evidence["errors"].([]string)
	if !ok || len(errorsFound) == 0 {
		t.Fatalf("validation evidence = %#v", result.Evidence)
	}
}

func TestEmptyConfigurationChecksHaveTruthfulNoopResults(t *testing.T) {
	t.Parallel()
	s := &session{cfg: &config.Config{}, options: Options{CheckNetwork: true}}
	if result := s.configuredPathsCheck(context.Background()); result.Status != diagnostics.StatusPass {
		t.Fatalf("empty configured paths result = %#v", result)
	}
	if result := s.listenerBindCheck(context.Background()); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("empty listener result = %#v", result)
	}
}

func TestNetworkPreflightStillHonorsParsePrerequisite(t *testing.T) {
	t.Parallel()
	s := &session{options: Options{CheckNetwork: true}}
	if result := s.runtimePreflightCheck(context.Background()); result.Status != diagnostics.StatusSkipped {
		t.Fatalf("missing-config preflight result = %#v", result)
	}
}
