// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"fmt"
	"testing"

	"jul/internal/config"
)

func TestProxyTransformedVaryNamesBoundConstruction(t *testing.T) {
	loc := config.LocationConfig{Headers: make(map[string]string)}
	for i := 0; i < 256; i++ {
		loc.Headers[fmt.Sprintf("X-Fixture-%d", i)] = "value"
	}
	names, complete := ProxyTransformedRequestHeaders(loc, 256)
	if complete || len(names) >= 256 {
		t.Fatal("oversized configuration did not produce a bounded incomplete snapshot")
	}
}
