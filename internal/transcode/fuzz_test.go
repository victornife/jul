// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import "testing"

// FuzzParseTemplate exercises the google.api.http path-template parser and, for
// every template that compiles, the matcher — asserting neither panics on
// arbitrary input. This guards the descriptor-loading parsing surface required
// by the GA bar (ADR 0003, criterion 8: fuzzing where parsing is involved).
func FuzzParseTemplate(f *testing.F) {
	seeds := []string{
		"/v1/echo",
		"/v1/echo/{id}",
		"/v1/shelves/{shelf}/books/{book}",
		"/v1/{name=shelves/*/books/*}",
		"/v1/{name=**}",
		"/v1/echo:custom",
		"/v1/**",
		"/",
		"",
		"no-leading-slash",
		"/a/{b=c/*/d}/e:verb",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		tmpl, err := parseTemplate(raw)
		if err != nil {
			return // rejected templates are fine; we only require no panic
		}
		// A compiled template must match against arbitrary paths without
		// panicking (the result is irrelevant here).
		_, _ = tmpl.match(raw)
		_, _ = tmpl.match("/v1/shelves/1/books/2")
		_, _ = tmpl.match("/")
	})
}
