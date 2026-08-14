// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"net"
	"syscall"
)

// ClassifyDialError buckets a backend dial/connect failure into a bounded,
// low-cardinality reason for jul_stream_backend_dial_failures_total and
// jul_http_backend_dial_failures_total. The set is closed by design (ADR 0017
// constraint 6: no metric may be labelled by an unbounded value such as a raw
// error string): anything not recognized falls into "other" rather than
// growing the label set.
func ClassifyDialError(err error) string {
	if errors.Is(err, ErrNoAvailableBackend) {
		return "no_backend"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	return "other"
}
