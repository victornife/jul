// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"jul/internal/config"
)

func TestProxyTransformedVaryNamesCoverObservedRewrites(t *testing.T) {
	var forwarded http.Header
	var originHost string
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded = r.Header.Clone()
		originHost = r.Host
		w.WriteHeader(http.StatusNoContent)
	}))
	defer origin.Close()
	loc := config.LocationConfig{ProxyPass: origin.URL, Headers: map[string]string{
		"X-Configured": "replacement", "X-Empty": "", "Host": "backend.example",
	}}
	h := newProxy(t, loc, nil)
	req := httptest.NewRequest(http.MethodGet, "http://edge.example/item", nil)
	for _, name := range []string{
		"Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto",
		"Client-Cert", "Client-Cert-Chain", "X-Forwarded-Client-Cert",
		"X-Configured", "X-Empty", "Proxy-Authorization",
	} {
		req.Header.Set(name, "client-fixture")
	}

	req.Header.Set("Accept-Language", "en")
	h.ServeHTTP(httptest.NewRecorder(), req)
	names, complete := ProxyTransformedRequestHeaders(loc, 256)
	if !complete {
		t.Fatal("proxy Vary policy was unexpectedly incomplete")
	}
	contains := func(name string) bool {
		return slices.ContainsFunc(names, func(n string) bool { return strings.EqualFold(n, name) })
	}
	if originHost == req.Host || !contains("Host") {
		t.Fatal("Host rewrite was not represented")
	}
	for name, values := range req.Header {
		if !slices.Equal(values, forwarded.Values(name)) && !contains(name) {
			t.Errorf("observed rewrite of %s is absent from the policy", name)
		}
	}
	for name := range forwarded {
		if len(req.Header.Values(name)) == 0 && !contains(name) {
			t.Errorf("generated %s is absent from the policy", name)
		}
	}
	if !slices.Equal(req.Header.Values("Accept-Language"), forwarded.Values("Accept-Language")) || contains("Accept-Language") {
		t.Fatal("unaffected Accept-Language was not preserved")
	}
}
