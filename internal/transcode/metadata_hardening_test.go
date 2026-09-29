// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build grpc

package transcode

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"google.golang.org/grpc/metadata"
)

func TestTranscodeMetadataCannotOverrideAuthorization(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer intended")
	r.Header.Set("Grpc-Metadata-Authorization", "Bearer alternate")
	r.Header.Set("Grpc-Metadata-X-Trace", "trace")
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
}
