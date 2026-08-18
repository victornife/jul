// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package upstream

import (
	"context"
	"errors"
	"net"
	"strings"
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
	// Windows does not always surface WSAECONNREFUSED as a syscall.Errno that
	// errors.Is can match (the wrapping varies by Go/OS version); its message
	// still says "actively refused", so fall back to that rather than losing
	// the bucket entirely on that platform.
	if strings.Contains(err.Error(), "refused") {
		return "refused"
	}
	return "other"
}

// ClassifyAdmissionError buckets an admission rejection into the same bounded
// reason set. Overload and forced retirement are distinct from a dial failure:
// nothing was dialled, so counting them as "other" would hide a capacity
// problem inside a connectivity bucket.
func ClassifyAdmissionError(err error) string {
	switch {
	case errors.Is(err, ErrOverloaded):
		return "overloaded"
	case errors.Is(err, ErrRetired), errors.Is(err, context.Canceled):
		return "shutdown"
	default:
		return ClassifyDialError(err)
	}
}
