// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestTranscodeMetadataCannotOverrideAuthorization(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer intended")
	r.Header.Set("Grpc-Metadata-Authorization", "Bearer alternate")
	r.Header.Set("Grpc-Metadata-X-Trace", "trace")
	r.Header.Set("Grpc-Metadata-Grpc-Timeout", "1n")
	md, ok := metadata.FromOutgoingContext(outgoingContext(r))
	if !ok {
		t.Fatal("missing outgoing metadata")
	}
	if got := md.Get("authorization"); len(got) != 1 || got[0] != "Bearer intended" {
		t.Fatalf("backend received ambiguous authorization: %v", got)
	}
	if got := md.Get("x-trace"); len(got) != 1 || got[0] != "trace" {
		t.Fatalf("ordinary metadata was lost: %v", got)
	}
	if got := md.Get("grpc-timeout"); len(got) != 0 {
		t.Fatalf("client injected transport deadline: %v", got)
	}
}

func TestTranscodeRejectsRepeatedAuthorizationBeforeBackend(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Add("Authorization", "Bearer one")
	r.Header.Add("Authorization", "Bearer two")
	rec := httptest.NewRecorder()
	(&Transcoder{}).ServeHTTP(rec, r)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous authorization returned %d", rec.Code)
	}
}

func TestTranscodeQueryBoundsBeforeDecode(t *testing.T) {
	for _, query := range []string{strings.Repeat("x", (64<<10)+1), strings.Repeat("a&", 1024)} {
		if err := validateTranscodeQuery(query); err == nil {
			t.Errorf("oversized query accepted: %d bytes", len(query))
		}
	}
	if err := validateTranscodeQuery("a=one&b=two"); err != nil {
		t.Fatal(err)
	}
}
