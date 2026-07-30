// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "testing"

// TestFinalizedAuditOperation verifies AC-04: each managed ApplyOperation maps
// to its own operation-specific terminal audit operation name so a reviewer can
// distinguish a finalized patch from a finalized rollback without parsing the
// free-text detail. An empty or unknown operation falls back to the generic
// config.apply.finalized name.
func TestFinalizedAuditOperation(t *testing.T) {
	cases := []struct {
		op   ApplyOperation
		want string
	}{
		{ApplyOperationConfigApply, "config.apply.finalized"},
		{ApplyOperationPatchApply, "config.patch.finalized"},
		{ApplyOperationLegacyRaw, "config.raw.finalized"},
		{ApplyOperationSettings, "config.settings.finalized"},
		{ApplyOperationRollback, "config.rollback.finalized"},
		{ApplyOperation(""), "config.apply.finalized"},
		{ApplyOperation("something.unknown"), "config.apply.finalized"},
	}
	for _, tc := range cases {
		if got := finalizedAuditOperation(tc.op); got != tc.want {
			t.Errorf("finalizedAuditOperation(%q) = %q, want %q", tc.op, got, tc.want)
		}
	}
}
