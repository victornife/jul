// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build windows

package observability

import (
	"errors"
	"io"
)

// newSyslogWriter reports that the syslog access-log sink is unavailable on
// Windows, where the standard library's log/syslog is not implemented. Use the
// "file" or "stdout" sink instead.
func newSyslogWriter() (io.WriteCloser, error) {
	return nil, errors.New("syslog sink is not supported on Windows (use file or stdout)")
}
