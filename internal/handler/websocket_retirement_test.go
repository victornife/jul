// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

package handler

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

type websocketRetirementBackend struct {
	conn   chan net.Conn
	closed chan []byte
	after  chan []byte
}

func newWebsocketRetirementBackend() *websocketRetirementBackend {
	return &websocketRetirementBackend{
		conn: make(chan net.Conn, 1), closed: make(chan []byte, 1), after: make(chan []byte, 1),
	}
}

func (b *websocketRetirementBackend) serve(w http.ResponseWriter, r *http.Request) {
	conn, rw, err := http.NewResponseController(w).Hijack()
	if err != nil {
		b.closed <- nil
		return
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	sum := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(sum[:]))
	if err := rw.Flush(); err != nil {
		b.closed <- nil
		return
	}
	b.conn <- conn
	initial := make([]byte, 7)
	if _, err := io.ReadFull(rw, initial); err != nil {
		b.closed <- nil
		return
	}
	if _, err := rw.Write([]byte{0x82, 1, initial[6] ^ initial[2]}); err != nil {
		b.closed <- nil
		return
	}
	if err := rw.Flush(); err != nil {
		b.closed <- nil
		return
	}
	closeFrame := make([]byte, 8)
	if _, err := io.ReadFull(rw, closeFrame); err != nil {
		b.closed <- nil
		return
	}
	b.closed <- closeFrame
	data, _ := io.ReadAll(rw)
	b.after <- data
}

func (b *websocketRetirementBackend) afterClose(t *testing.T) {
	t.Helper()
	select {
	case frame := <-b.closed:
		if len(frame) != 8 || frame[0] != 0x88 || frame[1] != 0x82 ||
			frame[6]^frame[2] != 0x03 || frame[7]^frame[3] != 0xf0 {
			t.Fatalf("backend Close=%x, want masked 1008", frame)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backend did not receive retirement Close")
	}
	conn := <-b.conn
	if _, err := conn.Write([]byte{0x82, 1, 'z', 0x88, 2, 0x03, 0xe9}); err != nil {
		t.Fatalf("send backend post-Close frames: %v", err)
	}
}

func (b *websocketRetirementBackend) verify(t *testing.T) {
	t.Helper()
	select {
	case data := <-b.after:
		if len(data) != 0 {
			t.Fatalf("backend received bytes after Jul's Close: %x", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backend remained open after withdrawal")
	}
}

func maskedRetirementTestFrames() []byte {
	return bytes.Join([][]byte{
		{0x82, 0x81, 1, 2, 3, 4, 'x' ^ 1},
		{0x88, 0x82, 1, 2, 3, 4, 0x03 ^ 1, 0xe9 ^ 2},
	}, nil)
}
