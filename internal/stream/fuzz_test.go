// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bufio"
	"bytes"
	"testing"
)

// FuzzPeekSNI exercises the TLS ClientHello SNI pecker against adversarial
// byte streams. The oracle: no panic; result is either a non-empty host or
// empty string (never uninitialised memory).
func FuzzPeekSNI(f *testing.F) {
	// Real ClientHello captured for "example.com" (minimal, deterministic).
	f.Add(clientHelloBytesForFuzz())
	// Not TLS
	f.Add([]byte("GET / HTTP/1.1\r\n"))
	// TLS record header but truncated
	f.Add([]byte{0x16, 0x03, 0x01, 0x00, 0x05})
	// TLS record with garbage handshake
	f.Add(append([]byte{0x16, 0x03, 0x01, 0x00, 0x10}, make([]byte, 16)...))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		br := bufio.NewReaderSize(bytes.NewReader(data), 4096)
		_ = peekSNI(br)
	})
}

// clientHelloBytesForFuzz returns a deterministic TLS ClientHello with SNI.
func clientHelloBytesForFuzz() []byte {
	// Minimal synthetic ClientHello (TLS 1.2, SNI = "a.b")
	// Record header
	hello := []byte{
		0x16,       // handshake
		0x03, 0x01, // version TLS 1.0
	}
	// Handshake: ClientHello
	body := []byte{
		0x01,             // ClientHello
		0x00, 0x00, 0x00, // length (placeholder)
		0x03, 0x03, // client_version TLS 1.2
	}
	// 32 bytes random
	body = append(body, make([]byte, 32)...)
	body = append(body, 0x00) // session_id length
	// cipher_suites
	body = append(body, 0x00, 0x02, 0x00, 0xff) // length + NULL
	// compression_methods
	body = append(body, 0x01, 0x00) // length + null
	// extensions length (placeholder)
	extLenPos := len(body)
	body = append(body, 0x00, 0x00)

	// SNI extension
	sni := make([]byte, 10)
	sni[0], sni[1] = 0x00, 0x00 // server_name
	// sni[2:4] extension length (patched later)
	// sni[4:6] server_name_list length
	// sni[6] = host_name type
	// sni[7:9] = name length
	name := []byte("a.b")
	u := uint16(len(name) + 3)
	sni[4] = byte(u >> 8)
	sni[5] = byte(u)
	v := uint16(len(name))
	sni[7] = byte(v >> 8)
	sni[8] = byte(v)
	sni = append(sni[:10], name...)
	w := uint16(len(sni) - 5)
	sni[2] = byte(w >> 8)
	sni[3] = byte(w)
	body = append(body, sni...)

	// Patch extensions length
	x := uint16(len(body) - extLenPos - 2)
	body[extLenPos] = byte(x >> 8)
	body[extLenPos+1] = byte(x)
	// Patch handshake length
	y := uint32(len(body) - 4)
	body[1] = byte(y >> 16)
	body[2] = byte(y >> 8)
	body[3] = byte(y)
	// Record length
	recLen := len(body)
	hello = append(hello, byte(recLen>>8), byte(recLen))
	return append(hello, body...)
}
