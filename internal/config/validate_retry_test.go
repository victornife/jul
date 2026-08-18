// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package config

import (
	"strings"
	"testing"
	"time"
)

// TestProxyRetriesAliasIsAcceptedAndExclusive pins the deprecation contract:
// the old spelling keeps working, so no running configuration becomes a startup
// failure, and setting both spellings is an error rather than a silent
// precedence rule nobody can read off the file.
func TestProxyRetriesAliasIsAcceptedAndExclusive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		loc     *LocationResilienceConfig
		retries int
		wantErr string
	}{
		{name: "deprecated spelling alone is valid", retries: 3},
		{name: "canonical spelling alone is valid", loc: &LocationResilienceConfig{RetryAttempts: 3}},
		{name: "neither is valid", loc: &LocationResilienceConfig{}},
		{
			name:    "both is an error",
			loc:     &LocationResilienceConfig{RetryAttempts: 3},
			retries: 2,
			wantErr: "must not both be set",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateLocationResilience(tc.loc, tc.retries, "where")
			switch {
			case tc.wantErr == "" && len(errs) > 0:
				t.Fatalf("unexpected errors: %v", errs)
			case tc.wantErr == "":
				return
			case len(errs) == 0:
				t.Fatalf("expected an error containing %q", tc.wantErr)
			case !strings.Contains(errs[0].Error(), tc.wantErr):
				t.Fatalf("error = %v, want it to contain %q", errs[0], tc.wantErr)
			}
		})
	}
}

// TestRetryRangesAreRejectedAtLoad pins that a typo fails at load rather than
// at 3am, and that the cross-field rules hold.
func TestRetryRangesAreRejectedAtLoad(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cfg     ResilienceConfig
		wantErr string
	}{
		{"attempts above the ceiling", ResilienceConfig{RetryAttempts: 101}, "retry_attempts"},
		{"negative attempts", ResilienceConfig{RetryAttempts: -1}, "retry_attempts"},
		{"deadline above the ceiling", ResilienceConfig{RetryDeadline: Duration(6 * time.Minute)}, "retry_deadline"},
		{"budget above the ceiling", ResilienceConfig{RetryBudgetPercent: 1001}, "retry_budget_percent"},
		{
			"backoff_max without backoff_initial",
			ResilienceConfig{RetryBackoffMax: Duration(time.Second)},
			"requires retry_backoff_initial",
		},
		{
			"initial above max",
			ResilienceConfig{RetryBackoffInitial: Duration(2 * time.Second), RetryBackoffMax: Duration(time.Second)},
			"must not exceed retry_backoff_max",
		},
		{
			"initial above the whole deadline",
			ResilienceConfig{RetryBackoffInitial: Duration(2 * time.Second), RetryDeadline: Duration(time.Second)},
			"must not exceed retry_deadline",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := validateResilience(&tc.cfg, "upstreams[0].resilience", time.Minute)
			if len(errs) == 0 {
				t.Fatalf("expected an error containing %q", tc.wantErr)
			}
			if !strings.Contains(errs[0].Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to contain %q", errs[0], tc.wantErr)
			}
		})
	}
}

// TestRetryDefaultsAreAccepted pins that every zero value resolves, which is
// what makes "the defaults reproduce today's behaviour" a property of the type
// rather than a claim in a document.
func TestRetryDefaultsAreAccepted(t *testing.T) {
	if errs := validateResilience(&ResilienceConfig{}, "where", time.Minute); len(errs) > 0 {
		t.Fatalf("an empty resilience block was rejected: %v", errs)
	}
	if errs := validateLocationResilience(&LocationResilienceConfig{}, 0, "where"); len(errs) > 0 {
		t.Fatalf("an empty location resilience block was rejected: %v", errs)
	}
}
