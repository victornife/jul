// Copyright 2026 Victor Niharra <vniharrafe@gmail.com>
// SPDX-License-Identifier: agpl

//go:build stream

package stream

import (
	"bufio"
)

// SNI routing inspects the TLS ClientHello to read the requested server name
// without terminating TLS. The bytes are peeked (not consumed) so the full
// ClientHello is still relayed to the backend verbatim, preserving end-to-end
// TLS (passthrough). A ClientHello may span several handshake records; parsing
// remains bounded by both a byte cap and a record-count cap. A malformed hello,
// a hello above either cap, or a hello without SNI yields an empty host, and the
// caller falls back to the catch-all/default route.

const (
	tlsRecordMax             = 16384
	tlsClientHelloMax        = 16384 // handshake header plus declared body
	tlsClientHelloMaxRecords = 64
	tlsInspectMax            = tlsClientHelloMax + 5*tlsClientHelloMaxRecords
)

// peekSNI returns the SNI host name from the buffered TLS ClientHello, or an
// empty string when the prefix is not a TLS handshake or carries no SNI. It
// never consumes bytes from br.
func peekSNI(br *bufio.Reader) string {
	var hello [tlsClientHelloMax]byte
	helloLen := 0
	want := 0
	offset := 0

	for records := 0; records < tlsClientHelloMaxRecords; records++ {
		hdr, err := br.Peek(offset + 5)
		if err != nil {
			return ""
		}
		hdr = hdr[offset:]
		// Record type 0x16 = handshake. The legacy record version must be a
		// defined SSLv3/TLS version; ClientHello.legacy_version is checked by
		// the message parser separately.
		if hdr[0] != 0x16 || hdr[1] != 0x03 || hdr[2] > 0x04 {
			return ""
		}
		recLen := int(hdr[3])<<8 | int(hdr[4])
		if recLen == 0 || recLen > tlsRecordMax || offset+5+recLen > tlsInspectMax {
			return ""
		}
		full, err := br.Peek(offset + 5 + recLen)
		if err != nil {
			return ""
		}
		payload := full[offset+5 : offset+5+recLen]

		copyLen := len(payload)
		if want > 0 && helloLen+copyLen > want {
			copyLen = want - helloLen
		}
		if copyLen < 0 || helloLen+copyLen > len(hello) {
			return ""
		}
		copy(hello[helloLen:], payload[:copyLen])
		helloLen += copyLen

		if want == 0 && helloLen >= 4 {
			if hello[0] != 0x01 { // ClientHello
				return ""
			}
			want = 4 + int(hello[1])<<16 + int(hello[2])<<8 + int(hello[3])
			if want < 4 || want > len(hello) {
				return ""
			}
		}
		if want > 0 && helloLen >= want {
			return parseClientHelloSNI(hello[:want])
		}
		offset += 5 + recLen
	}
	return ""
}

// parseClientHelloSNI walks a TLS handshake message and returns the host_name
// from the server_name extension, or "" if absent or malformed.
func parseClientHelloSNI(b []byte) string {
	c := cursor{b: b}
	if c.u8() != 0x01 { // ClientHello
		return ""
	}
	declared := c.u24()
	if c.err || declared != len(b)-4 {
		return ""
	}
	version := c.u16()
	if version < 0x0300 || version > 0x0304 {
		return ""
	}
	c.skip(32) // random
	sessionLen := c.u8()
	if sessionLen > 32 {
		return ""
	}
	c.skip(sessionLen)
	cipherLen := c.u16()
	if cipherLen < 2 || cipherLen%2 != 0 {
		return ""
	}
	c.skip(cipherLen)
	compressionLen := c.u8()
	if compressionLen < 1 {
		return ""
	}
	c.skip(compressionLen)
	if c.err {
		return ""
	}
	if c.pos == len(c.b) { // extensions are optional in old ClientHello forms
		return ""
	}
	extTotal := int(c.u16())
	end := c.pos + extTotal
	if c.err || end != len(c.b) {
		return ""
	}
	sni := ""
	sawSNI := false
	for c.pos < end && !c.err {
		extType := c.u16()
		extLen := int(c.u16())
		if c.err || c.pos+extLen > end {
			return ""
		}
		ext := c.take(extLen)
		if extType == 0x0000 { // server_name
			if sawSNI {
				return ""
			}
			sawSNI = true
			sni = parseSNIExtension(ext)
			if sni == "" {
				return ""
			}
		}
	}
	if c.err || c.pos != end {
		return ""
	}
	return sni
}

// parseSNIExtension extracts the first host_name entry from a server_name
// extension body.
func parseSNIExtension(b []byte) string {
	c := cursor{b: b}
	listLen := c.u16()
	if c.err || listLen != len(b)-2 {
		return ""
	}
	host := ""
	for !c.err && c.pos < len(c.b) {
		nameType := c.u8()
		nameLen := int(c.u16())
		name := c.take(nameLen)
		if c.err {
			return ""
		}
		if nameType == 0x00 { // host_name
			if host != "" || len(name) == 0 || len(name) > 255 {
				return ""
			}
			for _, ch := range name {
				if ch == 0 || ch > 0x7f {
					return ""
				}
			}
			host = string(name)
		}
	}
	if c.err || c.pos != len(c.b) {
		return ""
	}
	return host
}

// cursor is a minimal bounds-checked big-endian byte reader. Any out-of-range
// access sets err and makes subsequent reads return zero values.
type cursor struct {
	b   []byte
	pos int
	err bool
}

func (c *cursor) u8() int {
	if c.err || c.pos+1 > len(c.b) {
		c.err = true
		return 0
	}
	v := int(c.b[c.pos])
	c.pos++
	return v
}

func (c *cursor) u16() int {
	if c.err || c.pos+2 > len(c.b) {
		c.err = true
		return 0
	}
	v := int(c.b[c.pos])<<8 | int(c.b[c.pos+1])
	c.pos += 2
	return v
}

func (c *cursor) u24() int {
	if c.err || c.pos+3 > len(c.b) {
		c.err = true
		return 0
	}
	v := int(c.b[c.pos])<<16 | int(c.b[c.pos+1])<<8 | int(c.b[c.pos+2])
	c.pos += 3
	return v
}

func (c *cursor) skip(n int) {
	if c.err || n < 0 || c.pos+n > len(c.b) {
		c.err = true
		return
	}
	c.pos += n
}

func (c *cursor) take(n int) []byte {
	if c.err || n < 0 || c.pos+n > len(c.b) {
		c.err = true
		return nil
	}
	v := c.b[c.pos : c.pos+n]
	c.pos += n
	return v
}
