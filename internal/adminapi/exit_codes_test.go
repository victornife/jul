// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package adminapi

import "testing"

func TestExitCodesReturnsCompleteDefensiveContract(t *testing.T) {
	got := ExitCodes()
	if len(got) != 10 {
		t.Fatalf("len=%d want=10", len(got))
	}
	for i, row := range got {
		if row.Code != i || row.Meaning == "" {
			t.Fatalf("row %d = %+v", i, row)
		}
	}
	got[0].Meaning = "mutated"
	again := ExitCodes()
	if again[0].Meaning == "mutated" {
		t.Fatal("ExitCodes returned the mutable backing table")
	}
}
