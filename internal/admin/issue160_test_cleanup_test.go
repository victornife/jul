// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "testing"

func closeAuditLogOnCleanup(t *testing.T, a *auditLog) {
	t.Helper()
	if a == nil {
		return
	}
	t.Cleanup(func() { _ = a.Close() })
}
