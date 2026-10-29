// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"slices"
	"testing"

	"jul/internal/config"
)

func TestProxyAndCGITransformedRequestHeaders(t *testing.T) {
	loc := config.LocationConfig{
		Headers: map[string]string{
			"x-custom-upstream": "1",
			"x-tenant-id":       "acme",
		},
		FastCGIParams: map[string]string{
			"HTTP_X_CUSTOM_CGI": "1",
			"CONTENT_TYPE":      "application/json",
			"SCRIPT_FILENAME":   "/var/www/index.php",
		},
	}

	names, complete := ProxyTransformedRequestHeaders(loc, 64)
	if !complete {
		t.Fatal("expected complete=true for ProxyTransformedRequestHeaders with limit 64")
	}
	for _, want := range []string{"Host", "X-Forwarded-For", "Client-Cert", "X-Custom-Upstream", "X-Tenant-Id"} {
		if !slices.Contains(names, want) {
			t.Fatalf("expected %q in ProxyTransformedRequestHeaders, got %v", want, names)
		}
	}

	if _, ok := ProxyTransformedRequestHeaders(loc, 0); ok {
		t.Fatal("expected complete=false when limit=0")
	}
	if _, ok := ProxyTransformedRequestHeaders(loc, 21); ok {
		t.Fatal("expected complete=false when loc.Headers exceeds remaining limit")
	}

	cgiNames, cgiPrefixes, cgiComplete := CGITransformedRequestHeaders(loc, 64)
	if !cgiComplete {
		t.Fatal("expected complete=true for CGITransformedRequestHeaders with limit 64")
	}
	for _, want := range []string{"FORWARDED", "X-REAL-IP", "HOST", "PROXY", "CONTENT-LENGTH", "CONTENT-TYPE", "X-CUSTOM-CGI"} {
		if !slices.Contains(cgiNames, want) {
			t.Fatalf("expected %q in CGITransformedRequestHeaders names, got %v", want, cgiNames)
		}
	}
	for _, wantPrefix := range []string{"X-FORWARDED-", "X-SSL-CLIENT-", "SSL-CLIENT-"} {
		if !slices.Contains(cgiPrefixes, wantPrefix) {
			t.Fatalf("expected %q in CGITransformedRequestHeaders prefixes, got %v", wantPrefix, cgiPrefixes)
		}
	}

	if _, _, ok := CGITransformedRequestHeaders(loc, 0); ok {
		t.Fatal("expected complete=false for CGITransformedRequestHeaders with limit=0")
	}
	for _, lim := range []int{2, 9, 11, 14} {
		if _, _, ok := CGITransformedRequestHeaders(loc, lim); ok {
			t.Fatalf("expected complete=false for CGITransformedRequestHeaders with limit=%d", lim)
		}
	}
}
