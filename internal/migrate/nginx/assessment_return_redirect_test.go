// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build importer

package nginx

import "testing"

func TestClassifyReturnRedirectTargets(t *testing.T) {
	tests := []struct {
		name      string
		params    []string
		wantCode  string
		wantClass AssessmentClass
	}{
		{
			name:      "numeric local redirect",
			params:    []string{"302", "/health"},
			wantCode:  "NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT",
			wantClass: AssessmentApproximated,
		},
		{
			name:      "implicit local redirect",
			params:    []string{"/health"},
			wantCode:  "NGX_LOCATION_RETURN_ABSOLUTE_REDIRECT",
			wantClass: AssessmentApproximated,
		},
		{
			name:      "absolute http redirect",
			params:    []string{"301", "http://example.test/next"},
			wantCode:  "NGX_LOCATION_RETURN",
			wantClass: AssessmentSupported,
		},
		{
			name:      "absolute https redirect",
			params:    []string{"https://example.test/next"},
			wantCode:  "NGX_LOCATION_RETURN",
			wantClass: AssessmentSupported,
		},
		{
			name:      "scheme relative redirect",
			params:    []string{"302", "//example.test/next"},
			wantCode:  "NGX_LOCATION_RETURN",
			wantClass: AssessmentSupported,
		},
		{
			name:      "non redirect body",
			params:    []string{"200", "ok"},
			wantCode:  "NGX_LOCATION_RETURN_BODY",
			wantClass: AssessmentApproximated,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classifyReturn(test.params, false)
			if got.code != test.wantCode || got.class != test.wantClass {
				t.Fatalf("classifyReturn(%q) = %s/%s, want %s/%s", test.params, got.code, got.class, test.wantCode, test.wantClass)
			}
		})
	}
}
