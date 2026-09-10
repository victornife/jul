// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package admin

import "testing"

// closeAuditLogOnCleanup makes ownership explicit in tests. This matters on
// Windows, where an open audit destination prevents TempDir cleanup, and also
// documents the production invariant that the process owner is responsible for
// retiring the durable sink it created.
func closeAuditLogOnCleanup(t *testing.T, a *auditLog) {
	t.Helper()
	if a == nil {
		return
	}
	t.Cleanup(func() { _ = a.Close() })
}
