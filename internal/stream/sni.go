//go:build stream

package stream

import (
	"bufio"
)

// SNI routing inspects the TLS ClientHello to read the requested server name
// without terminating TLS. The bytes are peeked (not consumed) so the full
// ClientHello is still relayed to the backend verbatim, preserving end-to-end
// TLS (passthrough). Only the first TLS record is examined; a ClientHello that
// spans multiple records or omits SNI yields an empty host, and the caller
// falls back to the catch-all/default route.

const tlsRecordMax = 16384

// peekSNI returns the SNI host name from the buffered TLS ClientHello, or an
// empty string when the prefix is not a TLS handshake or carries no SNI. It
// never consumes bytes from br.
func peekSNI(br *bufio.Reader) string {
	hdr, err := br.Peek(5)
	if err != nil {
		return ""
	}
	// Record type 0x16 = handshake; otherwise this is not a TLS connection.
	if hdr[0] != 0x16 {
		return ""
	}
	recLen := int(hdr[3])<<8 | int(hdr[4])
	if recLen < 4 || recLen > tlsRecordMax {
		return ""
	}
	full, err := br.Peek(5 + recLen)
	if err != nil {
		return "" // full record not yet available
	}
	return parseClientHelloSNI(full[5 : 5+recLen])
}

// parseClientHelloSNI walks a TLS handshake message and returns the host_name
// from the server_name extension, or "" if absent or malformed.
func parseClientHelloSNI(b []byte) string {
	c := cursor{b: b}
	if c.u8() != 0x01 { // ClientHello
		return ""
	}
	c.skip(3)            // handshake length
	c.skip(2)            // client_version
	c.skip(32)           // random
	c.skip(int(c.u8()))  // session_id
	c.skip(int(c.u16())) // cipher_suites
	c.skip(int(c.u8()))  // compression_methods
	if c.err {
		return ""
	}
	extTotal := int(c.u16())
	end := c.pos + extTotal
	for c.pos < end && !c.err {
		extType := c.u16()
		extLen := int(c.u16())
		if extType == 0x0000 { // server_name
			return parseSNIExtension(c.take(extLen))
		}
		c.skip(extLen)
	}
	return ""
}

// parseSNIExtension extracts the first host_name entry from a server_name
// extension body.
func parseSNIExtension(b []byte) string {
	c := cursor{b: b}
	c.skip(2) // server_name_list length
	for !c.err && c.pos < len(c.b) {
		nameType := c.u8()
		nameLen := int(c.u16())
		name := c.take(nameLen)
		if c.err {
			return ""
		}
		if nameType == 0x00 { // host_name
			return string(name)
		}
	}
	return ""
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
