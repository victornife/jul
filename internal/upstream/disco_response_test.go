// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDiscoveryResponseRejectsOversizedValidPrefix(t *testing.T) {
	var got map[string]any
	if err := decodeDiscoveryResponse(strings.NewReader(`{"items":[]}`+strings.Repeat(" ", maxDiscoveryResponseBytes)), &got); err == nil {
		t.Fatal("oversized discovery response accepted")
	}
	if err := decodeDiscoveryResponse(strings.NewReader(`{"items":[]}{"items":[]}`), &got); err == nil {
		t.Fatal("trailing discovery document accepted")
	}
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestDiscoveryResponsePropagatesReadError(t *testing.T) {
	want := errors.New("truncated discovery body")
	var got map[string]any
	err := decodeDiscoveryResponse(io.MultiReader(strings.NewReader("{"), errReader{want}), &got)
	if !errors.Is(err, want) {
		t.Fatalf("decodeDiscoveryResponse error = %v, want %v", err, want)
	}
}
