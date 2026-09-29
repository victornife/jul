//go:build stream

package stream

import (
	"errors"
	"io"
	"testing"
)

type truncatedDatagramWriter struct{}

func (truncatedDatagramWriter) Write(p []byte) (int, error) { return len(p) - 1, nil }

func TestShortUDPDatagramWriteFails(t *testing.T) {
	if err := writeUDPDatagram(truncatedDatagramWriter{}, []byte("packet")); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short datagram write = %v; want io.ErrShortWrite", err)
	}
}
