// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package server

import "testing"

func TestHTTP2ExtendedConnectUsesProcessStartupGODEBUG(t *testing.T) {
	for _, tt := range []struct {
		value string
		want  bool
	}{
		{"", false},
		{"http2xconnect=1", true},
		{"http2xconnect=0", false},
		{"other=1,http2xconnect=1,other=2", true},
		{"http2xconnect=1,http2xconnect=0", false},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Setenv("GODEBUG", tt.value)
			if got := HTTP2ExtendedConnectEnabled(); got != tt.want {
				t.Fatalf("HTTP2ExtendedConnectEnabled()=%t, want %t", got, tt.want)
			}
		})
	}
}
