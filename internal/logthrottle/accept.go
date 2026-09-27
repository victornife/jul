// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package logthrottle

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
	"time"
)

// acceptErrorMarker is how net/http's server logs a failed Accept before it
// backs off (5ms doubling to 1s) and retries.
var acceptErrorMarker = []byte("http: Accept error:")

// AcceptErrorFilter passes net/http server error-log lines through to out
// unchanged, except Accept errors, which are admitted at most once per
// interval with the number seen since the last one. Under descriptor
// exhaustion net/http logs an Accept error every few milliseconds for as long
// as the pressure lasts (#422: ~160 lines/s), which fills disks rather than
// informing anyone.
type AcceptErrorFilter struct {
	out      io.Writer
	interval time.Duration
	limit    Limiter
	seen     atomic.Int64
}

// acceptErrorLogInterval spaces Accept-error reports on server error logs.
const acceptErrorLogInterval = 10 * time.Second

// ServerErrorLog is the http.Server ErrorLog Jul installs: the format and
// destination net/http uses when ErrorLog is nil (standard flags, stderr),
// with Accept errors bounded by AcceptErrorFilter.
func ServerErrorLog() *log.Logger {
	return log.New(NewAcceptErrorFilter(os.Stderr, acceptErrorLogInterval), "", log.LstdFlags)
}

// NewAcceptErrorFilter returns a filter writing to out.
func NewAcceptErrorFilter(out io.Writer, interval time.Duration) *AcceptErrorFilter {
	return &AcceptErrorFilter{out: out, interval: interval}
}

func (f *AcceptErrorFilter) Write(p []byte) (int, error) {
	if !bytes.Contains(p, acceptErrorMarker) {
		return f.out.Write(p)
	}
	n := f.seen.Add(1)
	if !f.limit.Allow(f.interval) {
		return len(p), nil
	}
	f.seen.Add(-n)
	line := bytes.TrimRight(p, "\n")
	if _, err := fmt.Fprintf(f.out, "%s (%d accept errors since the last report)\n", line, n); err != nil {
		return 0, err
	}
	return len(p), nil
}
