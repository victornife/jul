// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package logthrottle

import (
	"bytes"
	"errors"
	"log"
	"os"
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

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("disk full") }

func TestAcceptErrorFilterReportsWriteFailure(t *testing.T) {
	f := NewAcceptErrorFilter(failingWriter{}, time.Hour)
	if n, err := f.Write([]byte("http: Accept error: x\n")); err == nil || n != 0 {
		t.Fatalf("Write = %d, %v; want 0 and the writer's error", n, err)
	}
}

func TestServerErrorLogMatchesNetHTTPDefault(t *testing.T) {
	l := ServerErrorLog()
	if l.Flags() != log.LstdFlags || l.Prefix() != "" {
		t.Fatalf("flags=%d prefix=%q, want net/http's default logger shape", l.Flags(), l.Prefix())
	}
	f, ok := l.Writer().(*AcceptErrorFilter)
	if !ok || f.out != os.Stderr || f.interval != acceptErrorLogInterval {
		t.Fatalf("writer = %#v, want an AcceptErrorFilter on stderr", l.Writer())
	}
}
