// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import (
	"net/http"
	"testing"

	"jul/internal/server"
)

// TestConfigApplyResultStatusTimedOutPhase verifies AC-08: a result carrying a
// TimedOutPhase maps to 504 Gateway Timeout, and that this outcome takes
// precedence over the other status branches (validation, saved_not_live, ok).
func TestConfigApplyResultStatusTimedOutPhase(t *testing.T) {
	tests := []struct {
		name   string
		result ConfigApplyResult
		want   int
	}{
		{
			name:   "timed out handlers phase",
			result: ConfigApplyResult{TimedOutPhase: "preflight_handlers"},
			want:   http.StatusGatewayTimeout,
		},
		{
			name: "timeout wins over validation errors",
			result: ConfigApplyResult{
				TimedOutPhase:    "preflight_handlers",
				ValidationErrors: []string{"irrelevant"},
			},
			want: http.StatusGatewayTimeout,
		},
		{
			name: "timeout wins over saved_not_live",
			result: ConfigApplyResult{
				TimedOutPhase: "preflight_listeners",
				Reload:        &server.ReloadResult{Outcome: server.ReloadSavedNotLive},
			},
			want: http.StatusGatewayTimeout,
		},
		{
			name:   "no timeout, ok apply is 200",
			result: ConfigApplyResult{OK: true},
			want:   http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := configApplyResultStatus(tc.result); got != tc.want {
				t.Errorf("configApplyResultStatus = %d, want %d", got, tc.want)
			}
		})
	}
}
