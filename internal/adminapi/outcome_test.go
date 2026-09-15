// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import (
	"reflect"
	"testing"
)

func TestReloadOutcomesContract(t *testing.T) {
	want := []ReloadOutcome{
		"applied_live",
		"applied_degraded",
		"no_change",
		"not_applied",
		"saved_not_live",
		"staged",
		"owned_not_serving",
	}
	if got := ReloadOutcomes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ReloadOutcomes() = %v, want %v", got, want)
	}
}
