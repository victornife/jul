// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the package's tests under a goroutine-leak check so a listener,
// connection, or reload generation that fails to drain is caught deterministically
// instead of surfacing as an intermittent hang under parallel load (Finding CQ-1).
//
// goleak retries with backoff before failing, which gives net/http's connection
// goroutines time to exit after their listeners close; combined with the
// keep-alive-free test client in reload_test.go, no client connection is pooled
// past a request, so a surviving goroutine indicates a real leak.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
