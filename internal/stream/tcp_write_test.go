// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bytes"
	"io"
	"testing"
)

type shortStreamWriter struct{ bytes.Buffer }

func (w *shortStreamWriter) Write(p []byte) (int, error) {
	if len(p) > 2 {
		p = p[:2]
	}
	return w.Buffer.Write(p)
}

type stalledStreamWriter struct{}

func (stalledStreamWriter) Write([]byte) (int, error) { return 0, nil }

func TestStreamWriteCompletesShortWrites(t *testing.T) {
	var w shortStreamWriter
	if n, err := writeStreamChunk(&w, []byte("abcdef")); err != nil || n != 6 || w.String() != "abcdef" {
		t.Fatalf("stream write = %d, %v, %q", n, err, w.String())
	}
	if n, err := writeStreamChunk(stalledStreamWriter{}, []byte("x")); n != 0 || err != io.ErrShortWrite {
		t.Fatalf("stalled write = %d, %v", n, err)
	}
}
