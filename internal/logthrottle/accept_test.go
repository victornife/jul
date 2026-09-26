// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package logthrottle

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestAcceptErrorFilterBoundsAcceptErrors(t *testing.T) {
	var out bytes.Buffer
	f := NewAcceptErrorFilter(&out, time.Hour)
	l := log.New(f, "", 0)
	for i := 0; i < 500; i++ {
		l.Printf("http: Accept error: accept tcp 127.0.0.1:80: accept4: too many open files; retrying in 5ms")
	}
	l.Printf("http: TLS handshake error from 10.0.0.1:1: EOF")
	got := out.String()
	if n := strings.Count(got, "Accept error"); n != 1 {
		t.Fatalf("%d accept-error lines for 500, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "(1 accept errors since the last report)") {
		t.Fatalf("first report lacks its count: %q", got)
	}
	if !strings.Contains(got, "TLS handshake error") {
		t.Fatalf("other server errors must pass through unchanged: %q", got)
	}

	// After the interval the next report carries everything suppressed.
	f.limit = Limiter{}
	out.Reset()
	l.Printf("http: Accept error: x")
	if !strings.Contains(out.String(), "(500 accept errors since the last report)") {
		t.Fatalf("second report = %q, want 500 counted", out.String())
	}
}
