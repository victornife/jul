// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestCGITransformedVaryNamesFollowParameterMapping(t *testing.T) {
	loc := config.LocationConfig{FastCGIParams: map[string]string{
		"HTTP_X_CUSTOM_OVERRIDE": "value",
		"CONTENT_TYPE":           "text/plain",
		"OTHER_PARAMETER":        "value",
	}}
	names, prefixes, complete := CGITransformedRequestHeaders(loc, 256)
	if !complete {
		t.Fatal("CGI Vary policy was incomplete")
	}
	matches := func(field string) bool {
		for _, name := range names {
			if strings.EqualFold(name, field) {
				return true
			}
		}
		for _, prefix := range prefixes {
			if strings.HasPrefix(strings.ToLower(field), strings.ToLower(prefix)) {
				return true
			}
		}
		return false
	}
	for _, field := range []string{
		"Host", "Proxy", "Content-Length", "Content-Type", "Client-Cert",
		"Client-Cert-Chain", "Forwarded", "X-Real-Ip", "X-Forwarded-For",
		"X-Forwarded-Arbitrary", "X-Ssl-Client-Verify", "Ssl-Client-Cert",
		"X-Client-Cert", "X-Client-Verify", "X-Custom-Override",
	} {
		if !matches(field) {
			t.Errorf("transformed CGI field %q omitted", field)
		}
	}
	if matches("Accept-Language") {
		t.Fatal("unaffected request header was classified as transformed")
	}
	r := httptest.NewRequest(http.MethodGet, "http://example.test/", nil)
	r.Header.Set("X-Forwarded-Arbitrary", "fixture-spoof")
	r.Header.Set("Accept-Language", "en")
	params := buildCGIParams(loc, r)
	if _, ok := params["HTTP_X_FORWARDED_ARBITRARY"]; ok || params["HTTP_ACCEPT_LANGUAGE"] != "en" {
		t.Fatal("CGI parameter behavior does not match the Vary policy")
	}
}

func TestCGITransformedVaryNamesAreBounded(t *testing.T) {
	loc := config.LocationConfig{FastCGIParams: make(map[string]string)}
	for i := 0; i < 300; i++ {
		loc.FastCGIParams[fmt.Sprintf("HTTP_X_FIXTURE_%d", i)] = "value"
	}
	names, prefixes, complete := CGITransformedRequestHeaders(loc, 256)
	if complete || len(names)+len(prefixes) > 256 {
		t.Fatalf("CGI policy was not bounded: names=%d prefixes=%d complete=%t", len(names), len(prefixes), complete)
	}
}
