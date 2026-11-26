// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import (
	"os"
	"strings"
)

// Go's HTTP/2 server tests the raw startup GODEBUG string for this substring.
var http2ExtendedConnectEnabled = strings.Contains(os.Getenv("GODEBUG"), "http2xconnect=1")

// HTTP2ExtendedConnectEnabled reports the startup setting used by Go when it
// decides whether to advertise HTTP/2 Extended CONNECT.
func HTTP2ExtendedConnectEnabled() bool {
	return http2ExtendedConnectEnabled
}
