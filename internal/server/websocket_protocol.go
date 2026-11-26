// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"os"
	"strings"
)

// HTTP2ExtendedConnectEnabled reports the process-startup GODEBUG setting that
// controls whether Go advertises HTTP/2 Extended CONNECT.
func HTTP2ExtendedConnectEnabled() bool {
	enabled := false
	for _, setting := range strings.Split(os.Getenv("GODEBUG"), ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(setting), "=")
		if ok && key == "http2xconnect" {
			enabled = value == "1"
		}
	}
	return enabled
}
