// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package main

import (
	"testing"

	sdk "juliaplugins/sdk/v2"
)

func TestVaryCookieFixture(t *testing.T) {
	if sdk.HandleResponse == nil {
		t.Fatal("response fixture was not installed")
	}
	if got := sdk.HandleResponse(&sdk.Response{}); got != sdk.Deliver {
		t.Fatalf("response verdict = %d, want Deliver", got)
	}
	if got := sdk.HandleRequest(&sdk.Request{}); got != sdk.Continue {
		t.Fatalf("request action = %d, want Continue", got)
	}
}
